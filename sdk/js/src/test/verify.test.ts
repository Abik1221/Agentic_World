/** Coverage for signature edge cases, ReplayGuard eviction, and multi-game
 *  parseView — the paths the cross-language + lifecycle tests don't exercise. */
import assert from "node:assert/strict";
import test from "node:test";

import {
  MAFIA,
  MONOPOLY,
  ReplayGuard,
  VerificationError,
  computeSignature,
  parseView,
  verifyRequest,
} from "../index.js";

const SECRET = "test-secret";
const BODY = Buffer.from('{"game":"goofspiel"}');

function signedHeaders(ts: string, nonce = "n1") {
  const sig = computeSignature(SECRET, ts, nonce, "POST", "/turn", BODY);
  return {
    "x-arena-signature": `v1=${sig}`,
    "x-arena-timestamp": ts,
    "x-arena-request-id": nonce,
  };
}

test("stale timestamp is rejected (outside skew window)", () => {
  const now = 1_800_000_000; // fixed epoch seconds
  const ts = new Date((now - 3600) * 1000).toISOString(); // 1h old, skew is 300s
  assert.throws(
    () => verifyRequest(SECRET, signedHeaders(ts), "POST", "/turn", BODY, { now }),
    (e: unknown) => e instanceof VerificationError && e.reason === "stale_timestamp",
  );
});

test("a timestamp inside the skew window passes", () => {
  const now = 1_800_000_000;
  const ts = new Date((now - 10) * 1000).toISOString();
  assert.doesNotThrow(() => verifyRequest(SECRET, signedHeaders(ts), "POST", "/turn", BODY, { now }));
});

test("missing signature headers are rejected", () => {
  assert.throws(
    () => verifyRequest(SECRET, {}, "POST", "/turn", BODY),
    (e: unknown) => e instanceof VerificationError && e.reason === "missing_signature",
  );
});

test("unsupported signature version is rejected", () => {
  const ts = new Date().toISOString();
  const h = { ...signedHeaders(ts), "x-arena-signature": "v2=abc" };
  assert.throws(
    () => verifyRequest(SECRET, h, "POST", "/turn", BODY, { now: Date.parse(ts) / 1000 }),
    (e: unknown) => e instanceof VerificationError && e.reason === "unsupported_version",
  );
});

test("ReplayGuard evicts nonces past the TTL window", () => {
  const rg = new ReplayGuard(10 /* ttlSeconds */);
  const now = 1_000;
  assert.equal(rg.checkAndStore("a", now), true);
  assert.equal(rg.checkAndStore("a", now), false, "same nonce within TTL is a replay");
  // Advance beyond the TTL: the old nonce is evicted, so it's fresh again.
  assert.equal(rg.checkAndStore("a", now + 11), true, "nonce is reusable after TTL eviction");
});

test("ReplayGuard evicts the oldest nonce when the size cap is reached", () => {
  const rg = new ReplayGuard(1_000_000 /* ttl: no time eviction */, 2 /* maxSize */);
  const now = 1_000;
  assert.equal(rg.checkAndStore("a", now), true);
  assert.equal(rg.checkAndStore("b", now), true);
  // Cap is 2 — adding "c" evicts the oldest ("a") to make room, still accepting "c".
  assert.equal(rg.checkAndStore("c", now), true);
  assert.equal(rg.checkAndStore("b", now), false, "b still remembered");
  assert.equal(rg.checkAndStore("c", now), false, "c still remembered");
  // "a" was evicted, so it reads as fresh again (safe: it's outside any skew window).
  assert.equal(rg.checkAndStore("a", now), true);
});

test("parseView maps a monopoly view", () => {
  const v = parseView({
    game: "monopoly",
    match_id: "m1",
    seat: 2,
    phase: "buy",
    legal_actions: ["buy", "pass"],
    state: { cash: 1500 },
  }) as any;
  assert.equal(v.game, MONOPOLY);
  assert.equal(v.match_id, "m1");
  assert.equal(v.seat, 2);
  assert.equal(v.phase, "buy");
  assert.deepEqual(v.legal_actions, ["buy", "pass"]);
  assert.equal(v.state.cash, 1500);
});

test("parseView maps a mafia view (alive keys coerced to numbers)", () => {
  const v = parseView({
    game: "mafia",
    match_id: "x1",
    your_seat: 3,
    your_role: "mafia",
    day: 2,
    phase: "night",
    alive: { "0": true, "3": true, "5": false },
    allies: [7],
    legal: ["kill:5", "noop"],
  }) as any;
  assert.equal(v.game, MAFIA);
  assert.equal(v.your_seat, 3);
  assert.equal(v.your_role, "mafia");
  assert.equal(v.alive[0], true);
  assert.equal(v.alive[5], false);
  assert.deepEqual(v.allies, [7]);
  assert.deepEqual(v.legal, ["kill:5", "noop"]);
});
