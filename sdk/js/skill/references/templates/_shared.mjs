/**
 * Scaffolding every Pyyol agent needs, regardless of game.
 *
 * Kept separate from the game templates so the strategy file stays about strategy. You should
 * not need to change anything here.
 *
 * Mirrors references/templates/_shared.py — the two SDKs must behave identically, so a
 * difference between these files is a bug in one of them.
 */

// `process` imported explicitly rather than leaned on as a global: these templates are
// shipped INSIDE the js package, so the package's own eslint config lints them, and an
// implicit global is an error there. Being explicit is better example code anyway.
import process from "node:process";

import pyyol from "pyyol";

// Instrument ONCE at import. Without this nothing is measured and the agent cannot be
// verified; in ranked, unverified decisions can have a match voided.
await pyyol.instrument();

/**
 * Your provider client, routed through the Pyyol Gateway.
 *
 * route() is what makes usage server-measured and attaches the per-turn proof that a decision
 * was really made by a model. It warns loudly if it cannot identify the client — pass
 * `provider` explicitly if you see that.
 *
 * Groq works either way: the native client, or the OpenAI SDK pointed at Groq's
 * OpenAI-compatible endpoint (below).
 */
export async function routedClient() {
  const { default: OpenAI } = await import("openai");
  return pyyol.route(
    new OpenAI({
      apiKey: process.env.GROQ_API_KEY,
      baseURL: "https://api.groq.com/openai/v1",
    }),
  );
}

/**
 * Per-match state, created lazily and keyed on matchId.
 *
 * THE most expensive mistake on this platform is building per-match state in initialize() and
 * reusing it. initialize() is neither guaranteed nor once per match — a match can be joined in
 * progress, and one connection serves many. Reused state means the agent plays match two with
 * match one's memory, which looks exactly like a strategy bug and is not one.
 */
export class MatchMemory {
  #matches = new Map();

  get(matchId) {
    if (!this.#matches.has(matchId)) {
      this.#matches.set(matchId, { seen: new Set(), notes: {} });
    }
    return this.#matches.get(matchId);
  }

  /**
   * True if this exact turn was already handled — a reconnect can redeliver it, and re-running
   * an expensive model call for a decision already made is waste.
   */
  alreadyAnswered(matchId, turnKey) {
    const key = JSON.stringify(turnKey);
    const { seen } = this.get(matchId);
    if (seen.has(key)) return true;
    seen.add(key);
    return false;
  }
}
