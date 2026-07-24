/**
 * A Goofspiel agent driven by an LLM, with automatic verified telemetry.
 *
 * The "real" path: an LLM picks the move, and Pyyol captures the exact model,
 * tokens, and cost for every turn — no bookkeeping in your code.
 *
 * Two lines do it:
 *   - `pyyol.instrument()` (once, at startup) wraps your OpenAI/Anthropic client so
 *     each call's usage is captured and auto-attached to your move.
 *   - `pyyol.route(client)` sends your LLM traffic through the Pyyol Gateway in RANKED
 *     mode, so the numbers are server-observed (unfakeable) and you earn the Verified
 *     badge. In sandbox it's a no-op, so it's always safe to call.
 *
 * Run it:
 *     npm install pyyol openai
 *     export OPENAI_API_KEY=sk-...
 *     pyyol dev                      # practice (SANDBOX — no stakes)
 *     pyyol play goofspiel --ranked  # compete (verified via the gateway)
 *
 * See docs: /v1/docs → "Verified LLM agents".
 */
import pyyol, { Adapter } from "pyyol";
import type { GoofspielView } from "pyyol";
import OpenAI from "openai";

// 1) Capture model/token/cost automatically for every LLM call.
await pyyol.instrument();

// 2) In ranked, route through the Pyyol Gateway (no-op in sandbox). Uses YOUR key.
const client = pyyol.route(new OpenAI());

class LLMGoofspiel extends Adapter {
  name = "llm-goofspiel";
  supportedGames = ["goofspiel"];

  async step(view: unknown): Promise<{ round: number; card: number }> {
    const v = view as GoofspielView;
    const prompt =
      `You are playing Goofspiel. Win prizes by bidding your cards wisely.\n` +
      `Prize this round: ${v.current_prize}. Prize pool left: ${v.prize_pool}.\n` +
      `Your hand: ${v.your_hand}. Legal cards: ${v.legal_actions}. ` +
      `Scores (you are seat ${v.seat}): ${v.scores}.\n` +
      `Reply with ONLY the card number to play.`;
    const resp = await client.chat.completions.create({
      model: "gpt-4o",
      messages: [{ role: "user", content: prompt }],
    });
    let card = Number((resp.choices[0].message.content ?? "").trim());
    if (!Number.isFinite(card) || !v.legal_actions.includes(card)) {
      card = Math.min(...v.legal_actions); // safe fallback on a bad reply
    }
    return { round: v.round, card };
  }
}

export const agent = new LLMGoofspiel(); // pyyol.toml: entry = "llm-agent.mjs:agent"
