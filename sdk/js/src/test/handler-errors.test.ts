import { test } from "node:test";
import assert from "node:assert/strict";
import { Agent } from "../server.js";

test("a crashing step() is surfaced (error + real message), not swallowed", async () => {
  const a = new Agent({ supportedGames: ["goofspiel"], name: "t" });
  a.onTurn("goofspiel", () => {
    throw new Error("my strategy bug");
  });
  const { status, body } = await a.decideTurn({ game: "goofspiel", round: 1, legal_actions: [1, 2] });
  assert.equal(status, 500);
  assert.equal((body as any).error, "handler_error");
  assert.match(String((body as any).message), /my strategy bug/); // real error, not generic
});
