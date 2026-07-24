import { test } from "node:test";
import assert from "node:assert/strict";
import { Agent } from "../server.js";
import { Adapter, asAgent } from "../adapter.js";

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

test("a typed Adapter with an async step is awaited", async () => {
  // Adapter<View, Move> gives typed step; async LLM calls are first-class.
  class A extends Adapter<{ round: number; legal_actions: number[] }, { round: number; card: number }> {
    name = "a";
    supportedGames = ["goofspiel"];
    async step(view: { round: number; legal_actions: number[] }) {
      return { round: view.round, card: Math.min(...view.legal_actions) };
    }
  }
  const agent = asAgent(new A());
  const { status, body } = await agent.decideTurn({ game: "goofspiel", round: 2, legal_actions: [3, 7] });
  assert.equal(status, 200);
  assert.equal((body as any).card, 3);
});
