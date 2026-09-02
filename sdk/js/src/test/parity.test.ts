// The JS half of the cross-SDK parity guard.
//
// sdk/python/tests/test_sdk_parity.py compares the two export surfaces and owns the exemption
// lists. This file pins the part that can only be checked by RUNNING the JS code: that
// promptFor produces byte-for-byte the same string as Python's prompt_for, from the shared
// fixture both suites read.
//
// Why it matters that these agree exactly: Python's json.dumps defaults to ", "/": " and JS's
// JSON.stringify emits neither, so the two SDKs built DIFFERENT prompts from the same view. The
// prompt is never scored — but the model board compares one scaffold across models, and a paired
// comparison that silently swaps the prompt when you switch SDK is comparing two things.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import { promptFor, moveToolName, canonMove, boundMove, boundPlan } from "../movetools.js";
import { canonical } from "../pricing.js";
import { SDK_VERSION } from "../version.js";

interface PromptCase {
  name: string;
  view: Record<string, unknown>;
  prompt: string;
}

const here = dirname(fileURLToPath(import.meta.url));
const fixturePath = resolve(here, "..", "..", "..", "conformance", "prompt_for.json");
const doc = JSON.parse(readFileSync(fixturePath, "utf8")) as { cases: PromptCase[] };

test("prompt fixture is not empty", () => {
  // A suite that finds zero cases would report success while testing nothing.
  // Floor, not a count: the guard exists so an EMPTY fixture cannot report success. It was 5
  // until the Monopoly case was removed with that arena.
  assert.ok(doc.cases.length >= 4, `only ${doc.cases.length} prompt cases loaded`);
});

for (const c of doc.cases) {
  test(`promptFor matches Python byte for byte: ${c.name}`, () => {
    assert.equal(promptFor(c.view), c.prompt);
  });
}

test("promptFor never throws on a value that cannot be serialized", () => {
  // A helper that breaks the turn it was meant to help is worse than no helper. A cycle is the
  // easy case to hit by accident: a view holding a back-reference to its match.
  const cyclic: Record<string, unknown> = { game: "goofspiel" };
  cyclic.self = cyclic;
  const out = promptFor(cyclic);
  assert.ok(out.startsWith("You are playing a match in the Pyyol arena."));
  assert.ok(out.endsWith("Do not answer in prose."));
});

test("promptFor prefers an explicit serializer over raw fields", () => {
  // Mirrors the Python helper's attempt order, so an SDK view object with a toJSON renders the
  // same in both languages rather than leaking private fields in one of them.
  const view = { _private: "hidden", toJSON: () => ({ game: "mafia", round: 2 }) };
  assert.ok(promptFor(view).includes('{"game":"mafia","round":2}'));
  assert.ok(!promptFor(view).includes("hidden"));
});

// The symbols the Python SDK gained for parity — asserted here so the JS side is not merely
// assumed to have them. If one of these is renamed, this fails next to the Python test.
test("the shared move-tool surface is callable", () => {
  assert.equal(moveToolName("goofspiel"), "play_card");
  assert.equal(canonMove("goofspiel", { card: 7 }), "card:7");
  const resp = {
    choices: [
      { message: { tool_calls: [{ function: { name: "play_card", arguments: '{"card": 7}' } }] } },
    ],
  };
  assert.equal(boundMove("goofspiel", resp), "card:7");
  // A single move IS a one-round plan at the proven round — verified to be what the Python SDK
  // returns for the identical response, which is the only thing that makes this assertion worth
  // writing. (I expected null here; both SDKs disagreed with me, and they agree with each other.)
  assert.deepEqual(boundPlan("goofspiel", resp, 1), [{ round: 1, move: "card:7" }]);
});

test("canonical is public here and in Python", () => {
  assert.equal(typeof canonical, "function");
  assert.equal(canonical("gpt-4o-2024-08-06"), canonical("gpt-4o-2024-08-06"));
});

test("SDK_VERSION is generated from package.json, never hand-edited", () => {
  // genversion.mjs runs in build and prepack, so the PUBLISHED constant always matches. The
  // committed src/version.ts can lag a release-please bump, which is confusing to read but
  // cannot reach a user. This asserts the invariant that actually ships.
  const pkgPath = resolve(here, "..", "..", "package.json");
  const pkg = JSON.parse(readFileSync(pkgPath, "utf8")) as { version: string };
  assert.equal(
    SDK_VERSION,
    pkg.version,
    "run `npm run genversion` — SDK_VERSION is reported in /handshake and by `pyyol --version`",
  );
});
