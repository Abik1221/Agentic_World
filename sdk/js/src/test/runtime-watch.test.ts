/**
 * `pyyol run` offers the watch choice without ever blocking the match.
 *
 * Mirrors `sdk/python/tests/test_run_watch_offer.py`. This is the path a developer is
 * actually on: they typed a command, a staked match appeared, and until now it simply
 * began. The prompt has to fit into that WITHOUT delaying the acknowledgement or the
 * first turn — a courtesy that costs a stake is not a courtesy.
 */

import assert from "node:assert/strict";
import { test } from "node:test";

import { RuntimeConnector } from "../runtime.js";

/** Reach the private offerWatch without widening the public surface for a test. */
function offer(c: RuntimeConnector, payload: Record<string, unknown>): Promise<void> {
  return (c as unknown as { offerWatch(p: Record<string, unknown>): Promise<void> }).offerWatch(payload);
}

function connector(): RuntimeConnector {
  // The agent is never touched on this path — offerWatch reads only the frame payload.
  const agent = { supportedGames: ["goofspiel"] } as unknown as ConstructorParameters<
    typeof RuntimeConnector
  >[0];
  return new RuntimeConnector(agent, { url: "wss://x", agentId: "ag_1", token: "t" });
}

test("the dispatch loop is not blocked by the prompt", async () => {
  // The load-bearing test. offerWatch is called from the frame-dispatch loop and is
  // deliberately NOT awaited there; if it were, every frame behind it would stall —
  // including the heartbeat keeping the connection alive and the first turn of the
  // match. A developer who stepped away for coffee would return to a forfeited stake.
  //
  // PYYOL_WATCH is unset here on purpose so the real prompt path runs. There is no TTY
  // under `node --test`, so askWatch returns immediately without reading anything —
  // which is itself the CI guarantee, asserted directly in watch.test.ts.
  const c = connector();
  const t0 = Date.now();
  const p = offer(c, { match_id: "m_1", game: "goofspiel" });
  const sync = Date.now() - t0;
  assert.ok(sync < 500, `offerWatch blocked its caller for ${sync}ms before yielding`);
  await p;
});

test("offered once per connection, not once per match", async () => {
  // A long run plays many matches; asking before each is what you learn to dread. Also
  // correctness: a timed-out prompt leaves a reader parked on stdin, so a second ask
  // would find its answer swallowed by the first.
  process.env.PYYOL_WATCH = "terminal";
  try {
    const c = connector();
    const seen: boolean[] = [];
    for (let i = 0; i < 5; i++) {
      await offer(c, { match_id: `m_${i}`, game: "goofspiel" });
      seen.push(true);
    }
    // The observable proof is that repeated calls are cheap and silent; the guard
    // itself is the private watchOffered flag.
    const flag = (c as unknown as { watchOffered: boolean }).watchOffered;
    assert.equal(flag, true);
    assert.equal(seen.length, 5);
  } finally {
    delete process.env.PYYOL_WATCH;
  }
});

test("a game with no viewer route is silent", async () => {
  // No link rather than a wrong one — a tab onto someone else's live match is worse
  // than no tab at all.
  const c = connector();
  await offer(c, { match_id: "m_1", game: "not-a-game" });
  const flag = (c as unknown as { watchOffered: boolean }).watchOffered;
  assert.equal(flag, true, "still marked offered, so a bad game does not re-ask forever");
});

test("a payload with no match id does not throw into the run loop", async () => {
  // Watching is a courtesy; the match is not.
  const c = connector();
  await offer(c, {});
  await offer(c, { match_id: "", game: "" });
});
