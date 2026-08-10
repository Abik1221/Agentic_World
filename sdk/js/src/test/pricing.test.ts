import { test } from "node:test";
import assert from "node:assert/strict";

import { cacheWriteRate, canonical, estimateCost, isKnown, PRICING_VERSION, rateFor } from "../pricing.js";

test("canonical model mapping", () => {
  const cases: Array<[string, string]> = [
    ["gpt-4o", "gpt-4o"],
    ["gpt-4o-2024-08-06", "gpt-4o"],
    ["gpt-4o-mini", "gpt-4o-mini"], // specific rule wins over "4o"
    ["GPT-4O-MINI", "gpt-4o-mini"], // case-insensitive
    ["gpt-4.1-mini", "gpt-4.1-mini"],
    ["o1-mini", "o1-mini"],
    ["us.anthropic.claude-opus-4-1-20250805", "claude-opus"],
    ["claude-3-5-sonnet-20241022", "claude-sonnet"],
    ["claude-haiku-4-5", "claude-haiku"],
    ["gemini-2.5-flash", "gemini-flash"],
    ["gemini-1.5-pro", "gemini-pro"],
    ["meta-llama/Llama-3.3-70B", "llama"],
    ["deepseek-chat", "deepseek"],
  ];
  for (const [raw, expected] of cases) assert.equal(canonical(raw), expected, raw);
});

test("unknown model uses fallback, never silently zero", () => {
  assert.equal(canonical("totally-made-up"), null);
  assert.equal(isKnown("totally-made-up"), false);
  const cost = estimateCost("totally-made-up", { promptTokens: 1_000_000, completionTokens: 1_000_000 });
  assert.ok(Math.abs(cost - (0.5 + 1.5)) < 1e-9);
});

test("opus > sonnet > haiku (the split the old heuristic lacked)", () => {
  const a = { promptTokens: 1_000_000, completionTokens: 1_000_000 };
  const opus = estimateCost("claude-opus-4", a);
  const sonnet = estimateCost("claude-sonnet-4", a);
  const haiku = estimateCost("claude-haiku-4-5", a);
  assert.ok(opus > sonnet && sonnet > haiku);
});

test("cost math splits input vs output", () => {
  const cost = estimateCost("gpt-4o", { promptTokens: 1000, completionTokens: 500 });
  const expected = (1000 * 2.5 + 500 * 10.0) / 1_000_000;
  assert.ok(Math.abs(cost - expected) < 1e-9);
});

test("cached tokens billed at cached rate, clamped to prompt", () => {
  const cost = estimateCost("gpt-4o", { promptTokens: 1000, cachedTokens: 400 });
  const expected = (600 * 2.5 + 400 * 1.25) / 1_000_000;
  assert.ok(Math.abs(cost - expected) < 1e-9);

  const clamped = estimateCost("gpt-4o", { promptTokens: 100, cachedTokens: 500 });
  assert.ok(Math.abs(clamped - (100 * 1.25) / 1_000_000) < 1e-9);
});

test("open-weight models are free; zero tokens is zero", () => {
  assert.equal(estimateCost("llama-3.3-70b", { promptTokens: 1_000_000, completionTokens: 1_000_000 }), 0);
  assert.equal(estimateCost("gpt-4o", {}), 0);
});

test("pricing version is stamped", () => {
  assert.equal(typeof PRICING_VERSION, "string");
  assert.ok(PRICING_VERSION.length > 0);
});

// --- Prompt-cache accounting ---------------------------------------------------
//
// Mirrors sdk/python/tests/test_pricing.py. The bug: cache WRITES were never captured or
// priced, and cache tokens were assumed to be a subset of the reported input count on
// every provider. Both errors pushed cost DOWN, hardest for the agents that cache most.

test("a cache write is billed above input on Anthropic", () => {
  const r = rateFor("claude-opus-4");
  assert.equal(cacheWriteRate("claude-opus-4"), r.input * 1.25);
  assert.ok(cacheWriteRate("claude-opus-4") > r.input);
  assert.ok(cacheWriteRate("claude-opus-4") > (r.cachedInput ?? r.input));
});

test("a cache write is free on OpenAI", () => {
  // OpenAI's prompt caching is automatic and writes are not billed; applying Anthropic's
  // surcharge here would invent a charge that does not exist.
  assert.equal(cacheWriteRate("gpt-4o"), 0);
});

test("an unknown model's cache writes are not silently free", () => {
  // Free is the flattering direction: it would let an unrecognised model look cheaper
  // than every model we do know.
  assert.ok(cacheWriteRate("some-unreleased-model-2027") > 0);
});

test("reads and writes partition the input without double billing", () => {
  const r = rateFor("claude-opus-4");
  const prompt = 2520, read = 1500, write = 600, completion = 90;
  const got = estimateCost("claude-opus-4", {
    promptTokens: prompt,
    completionTokens: completion,
    cachedTokens: read,
    cachedWriteTokens: write,
  });
  const full = prompt - read - write; // 420 at the full input rate
  const want =
    (full * r.input +
      read * (r.cachedInput ?? r.input) +
      write * cacheWriteRate("claude-opus-4") +
      completion * r.output) /
    1_000_000;
  assert.ok(Math.abs(got - want) < 1e-9, `cost ${got} != ${want}`);
});

test("the old cache accounting understated a real call by over 3x", () => {
  // Token counts observed from a live gateway call: Anthropic reported input 420,
  // output 90, cache read 1500, cache write 600.
  //
  // The old path never read cache_creation_input_tokens, and treated reads as a subset of
  // the raw input count — so min(1500, 420) billed 420 tokens at the cheap read rate and
  // discarded the other 1080 reads. Both errors understated cost.
  const old = estimateCost("claude-opus-4", { promptTokens: 420, completionTokens: 90, cachedTokens: 420 });
  const updated = estimateCost("claude-opus-4", {
    promptTokens: 2520,
    completionTokens: 90,
    cachedTokens: 1500,
    cachedWriteTokens: 600,
  });
  assert.ok(Math.abs(old - (420 * 1.5 + 90 * 75.0) / 1_000_000) < 1e-9);
  assert.ok(Math.abs(updated - (420 * 15.0 + 1500 * 1.5 + 600 * 18.75 + 90 * 75.0) / 1_000_000) < 1e-9);
  assert.ok(updated > 3.5 * old, `${updated} should exceed 3.5x ${old}`);
});

test("self-hosted caching is still free", () => {
  // A multiplier on a $0 input rate must stay $0 — a locally served model has no bill of
  // any kind, cache or otherwise.
  assert.equal(cacheWriteRate("llama-3.3-70b", "ollama"), 0);
  assert.equal(
    estimateCost("llama-3.3-70b", {
      promptTokens: 2520,
      completionTokens: 90,
      cachedTokens: 1500,
      cachedWriteTokens: 600,
      provider: "ollama",
    }),
    0,
  );
});
