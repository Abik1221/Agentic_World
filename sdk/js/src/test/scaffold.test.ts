// Scaffold fingerprints: shared fixtures, asserted identically by the Python and JS SDKs.
//
// A fingerprint is a hash, so any disagreement between the two SDKs is total: the same agent
// switching SDKs would look like a brand-new scaffold, its history would split into two
// epochs, and it would silently drop out of every paired model comparison. Nothing about
// that failure looks like a bug — the agent just quietly stops appearing on the model board.
//
// The expected values in the fixture file were produced by the Python implementation. This
// suite asserting them is what makes the two implementations one contract rather than two.

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, resolve } from "node:path";

import {
  canonical,
  diagnose,
  eligibleForPairing,
  extract,
  fromRequest,
  SCAFFOLD_VERSION,
} from "../scaffold.js";

interface Case {
  name: string;
  why: string;
  endpoint: string;
  request: Record<string, unknown>;
  expect?: { fingerprint?: string; canonical?: string; canonical_contains?: string };
  same_fingerprint_as?: string;
  differs_from?: string;
}

const here = dirname(fileURLToPath(import.meta.url));
const fixturePath = resolve(here, "..", "..", "..", "conformance", "scaffold.json");
const doc = JSON.parse(readFileSync(fixturePath, "utf8")) as {
  scaffold_version: string;
  cases: Case[];
};
const byName = new Map(doc.cases.map((c) => [c.name, c]));

const fp = (c: Case) => fromRequest(c.request, c.endpoint);
const canon = (c: Case) => canonical(extract(c.request, c.endpoint));

test("fixture file is not empty", () => {
  // A suite that silently finds zero cases would report success while testing nothing.
  assert.ok(doc.cases.length >= 15, `only ${doc.cases.length} scaffold cases loaded`);
});

test("scaffold version matches the fixtures", () => {
  assert.equal(SCAFFOLD_VERSION, doc.scaffold_version);
});

for (const c of doc.cases) {
  if (c.expect) {
    test(`scaffold: ${c.name}`, () => {
      if (c.expect!.fingerprint !== undefined) assert.equal(fp(c), c.expect!.fingerprint, c.why);
      if (c.expect!.canonical !== undefined) assert.equal(canon(c), c.expect!.canonical, c.why);
      if (c.expect!.canonical_contains !== undefined) {
        assert.ok(
          canon(c).includes(c.expect!.canonical_contains),
          `${c.why}\ncanonical was: ${JSON.stringify(canon(c))}`,
        );
      }
    });
  }
  if (c.same_fingerprint_as) {
    test(`scaffold: ${c.name} shares a fingerprint with ${c.same_fingerprint_as}`, () => {
      const other = byName.get(c.same_fingerprint_as!)!;
      const mine = fp(c);
      assert.ok(mine, "an empty fingerprint cannot satisfy a sameness claim");
      assert.equal(mine, fp(other), c.why);
    });
  }
  if (c.differs_from) {
    test(`scaffold: ${c.name} differs from ${c.differs_from}`, () => {
      assert.notEqual(fp(c), fp(byName.get(c.differs_from!)!), c.why);
    });
  }
}

test("the model is never part of the fingerprint", () => {
  // The single property the whole design rests on, asserted directly rather than only through
  // fixtures: swapping the model must not move the fingerprint, or no model change is ever
  // pairable and the model board cannot exist.
  const base = {
    system: "You play Goofspiel.",
    messages: [{ role: "user", content: "bid" }],
    temperature: 0.3,
  };
  const a = fromRequest({ ...base, model: "claude-opus-4" }, "x");
  const b = fromRequest({ ...base, model: "gpt-5.2" }, "x");
  const c = fromRequest({ ...base, model: "llama-3.3-70b" }, "x");
  assert.ok(a);
  assert.equal(a, b);
  assert.equal(b, c);
});

test("credentials never enter the hash", () => {
  // Beyond the obvious: a key rotation would otherwise split an agent's history for a reason
  // that has nothing to do with its harness.
  const base = { system: "S", messages: [{ role: "user", content: "u" }] };
  assert.equal(fromRequest(base, "x"), fromRequest({ ...base, apiKey: "sk-secret", baseURL: "http://x" }, "x"));
  assert.ok(!canonical(extract({ ...base, apiKey: "sk-secret" }, "x")).includes("sk-secret"));
});

test("pairing eligibility refuses unknowns", () => {
  // Treating "we could not tell" as "the same as the others" is how a confounded comparison
  // gets published as a clean one.
  assert.ok(eligibleForPairing(["sc_a", "sc_a"]));
  assert.ok(!eligibleForPairing(["sc_a", "sc_b"]));
  assert.ok(!eligibleForPairing(["sc_a", ""]));
  assert.ok(!eligibleForPairing([undefined]));
  assert.ok(!eligibleForPairing([]));
});

test("a prompt in the user turn is not a scaffold", () => {
  // The flaw this rule exists to close, found by reading our own shipped example. With no
  // system prompt the hashable surface is client + roles + sampling, none of which move when
  // the developer rewrites the instructions they actually steer the model with. A fingerprint
  // there is a FALSE CERTIFICATE: the agent could replace its whole strategy mid-season, keep
  // reporting one scaffold id, and have the gain credited to a model swap.
  const a = { messages: [{ role: "user", content: "Bid low early." }] };
  const b = { messages: [{ role: "user", content: "Always bid your highest card." }] };
  assert.equal(fromRequest(a, "openai.chat.completions"), "");
  assert.equal(fromRequest(b, "openai.chat.completions"), "");
});

test("the developer is told why and what to do", () => {
  // An agent that silently fails to qualify files a support ticket; one that is told "move
  // your instructions into a system message" fixes it in a line.
  assert.ok(diagnose({ messages: [{ role: "user", content: "Bid low." }] }, "x").includes("system message"));
  // And no note once it is fixed, so the field is a signal rather than decoration.
  assert.equal(
    diagnose(
      { messages: [{ role: "system", content: "Bid low." }, { role: "user", content: "state" }] },
      "x",
    ),
    "",
  );
});

test("moving the prompt into a system message makes a rewrite visible", () => {
  // The payoff of the rule: once instructions are in a system message, changing them
  // correctly registers as a NEW scaffold instead of hiding inside an unchanged id.
  const state = { role: "user", content: "Round 3." };
  const one = fromRequest({ messages: [{ role: "system", content: "Bid low early." }, state] }, "x");
  const two = fromRequest({ messages: [{ role: "system", content: "Bid high always." }, state] }, "x");
  assert.ok(one && two);
  assert.notEqual(one, two);
});
