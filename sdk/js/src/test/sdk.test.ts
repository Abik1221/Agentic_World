/** Tests for the onavion JS/TS SDK. Run: npm run build && npm test. */
import assert from "node:assert/strict";
import test from "node:test";

import {
  Agent,
  ReplayGuard,
  VerificationError,
  computeSignature,
  simulateGoofspiel,
  verifyRequest,
} from "../index.js";

// A fixed vector shared with the Go platform and the Python SDK — all three agree.
const XLANG = {
  args: ["shared-secret", "2026-07-06T12:00:00Z", "req_abc", "POST", "/turn"] as const,
  body: Buffer.from('{"x":1}'),
  expected: "d0da90bddbc2cef9aec939e18c6b1c8e7d2f706cd21da8b1b3d7eb34a6275976",
};

function agent(secret = "test-secret"): Agent {
  const a = new Agent({ secret, supportedGames: ["goofspiel"], name: "lowball" });
  a.onTurn("goofspiel", (v: any) => ({ round: v.round, card: Math.min(...v.legal_actions) }));
  return a;
}

test("signature matches the cross-language vector", () => {
  assert.equal(computeSignature(...XLANG.args, XLANG.body), XLANG.expected);
});

test("simulate runs the full lifecycle", async () => {
  const a = agent();
  const seen = { init: 0, event: 0, end: 0 };
  a.onInitialize(() => { seen.init++; });
  a.onEvent(() => { seen.event++; });
  a.onGameEnd(() => { seen.end++; });
  const res = await simulateGoofspiel(a, { handSize: 13, seed: 3 });
  assert.equal(res.rounds, 13);
  assert.equal(res.scores.agent + res.scores.baseline, (13 * 14) / 2);
  assert.deepEqual(seen, { init: 1, event: 13, end: 1 });
});

test("health is unsigned", async () => {
  const { status, body } = await agent().handle("GET", "/health", {}, Buffer.alloc(0));
  assert.equal(status, 200);
  assert.equal((body as any).status, "healthy");
});

test("tampered signature is rejected", async () => {
  const { status, body } = await agent().handle(
    "POST", "/turn",
    { "x-arena-signature": "v1=deadbeef", "x-arena-timestamp": new Date().toISOString(), "x-arena-request-id": "x" },
    Buffer.from('{"game":"goofspiel"}'),
  );
  assert.equal(status, 401);
  assert.equal((body as any).reason, "bad_signature");
});

test("replayed nonce is rejected", () => {
  const ts = new Date().toISOString();
  const body = Buffer.from('{"game":"goofspiel"}');
  const sig = computeSignature("test-secret", ts, "n1", "POST", "/turn", body);
  const hdr = { "x-arena-signature": `v1=${sig}`, "x-arena-timestamp": ts, "x-arena-request-id": "n1" };
  const rg = new ReplayGuard();
  verifyRequest("test-secret", hdr, "POST", "/turn", body, { replayGuard: rg });
  assert.throws(
    () => verifyRequest("test-secret", hdr, "POST", "/turn", body, { replayGuard: rg }),
    (e: unknown) => e instanceof VerificationError && e.reason === "replayed_nonce",
  );
});
