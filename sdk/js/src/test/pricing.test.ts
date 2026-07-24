import { test } from "node:test";
import assert from "node:assert/strict";

import { canonical, estimateCost, isKnown, PRICING_VERSION } from "../pricing.js";

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
