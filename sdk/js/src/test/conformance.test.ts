// Cross-language conformance: token normalization + cost, driven by shared fixtures.
//
// The expectations live in sdk/conformance/usage_pricing.json and are read by this suite,
// the Python SDK's test_conformance.py and the Go backend's conformance_test.go.
//
// The point is drift. Three implementations of this logic exist, and nothing in any of them
// forces them to agree; each language's own tests would keep passing while the three
// quietly diverged. Since the boards rank on cost efficiency, a divergence would land as a
// silent bias in a public ranking rather than a visible bug — an agent scored cheaper or
// dearer for choosing a different SDK. Sharing one set of expectations makes that a
// failing test.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import { extractUsage } from "../instrument.js";
import { estimateCost, PRICING_VERSION } from "../pricing.js";

interface Expect {
  prompt_tokens: number;
  completion_tokens: number;
  cached_read_tokens: number;
  cached_write_tokens: number;
  reasoning_tokens: number;
  cost_usd: number;
  cost_breakdown: string;
}

interface Case {
  name: string;
  why: string;
  model: string;
  provider: string;
  response: unknown;
  expect: Expect;
}

// Tests run from dist/, so climb to the package root and across to the shared directory.
// Resolved from this module's own URL rather than process.cwd(), which differs between a
// bare `node --test` and an npm script.
const here = dirname(fileURLToPath(import.meta.url));
const fixturePath = resolve(here, "..", "..", "..", "conformance", "usage_pricing.json");

// A missing fixture file must fail loudly rather than silently skip: an empty conformance
// suite that reports "passed" is the exact failure this guards against.
const doc = JSON.parse(readFileSync(fixturePath, "utf8")) as {
  pricing_version: string;
  cases: Case[];
};

test("fixture file is not empty", () => {
  // A suite that silently finds zero cases would report success while testing nothing.
  assert.ok(doc.cases.length >= 7, `only ${doc.cases.length} conformance cases loaded`);
});

test("pricing version matches the fixtures", () => {
  // The fixture costs were computed from one dated table. If the SDK's table moves and the
  // fixtures do not, every cost below is being checked against stale rates.
  assert.equal(PRICING_VERSION, doc.pricing_version);
});

for (const c of doc.cases) {
  test(`conformance: ${c.name} — usage`, () => {
    const info = extractUsage(c.response);
    assert.ok(info !== null, `extractor returned nothing — ${c.why}`);
    assert.deepEqual(
      {
        prompt_tokens: info.promptTokens,
        completion_tokens: info.completionTokens,
        cached_read_tokens: info.cachedTokens,
        cached_write_tokens: info.cachedWriteTokens,
        reasoning_tokens: info.reasoningTokens,
      },
      {
        prompt_tokens: c.expect.prompt_tokens,
        completion_tokens: c.expect.completion_tokens,
        cached_read_tokens: c.expect.cached_read_tokens,
        cached_write_tokens: c.expect.cached_write_tokens,
        reasoning_tokens: c.expect.reasoning_tokens,
      },
      c.why,
    );
  });

  test(`conformance: ${c.name} — subset invariant`, () => {
    // read + write <= prompt, in every case. Violating it means pricing silently clamps the
    // excess to zero, which reads as a cheaper call rather than as an error.
    const info = extractUsage(c.response)!;
    assert.ok(
      info.cachedTokens + info.cachedWriteTokens <= info.promptTokens,
      `cache tokens exceed the prompt total, so pricing would discard the excess as if it had never been billed`,
    );
  });

  test(`conformance: ${c.name} — cost`, () => {
    const info = extractUsage(c.response)!;
    const got = estimateCost(c.model, {
      promptTokens: info.promptTokens,
      completionTokens: info.completionTokens,
      cachedTokens: info.cachedTokens,
      cachedWriteTokens: info.cachedWriteTokens,
      reasoningTokens: info.reasoningTokens,
      provider: c.provider,
    });
    assert.ok(
      Math.abs(got - c.expect.cost_usd) < 1e-9,
      `cost ${got} != ${c.expect.cost_usd} (${c.expect.cost_breakdown})`,
    );
  });
}
