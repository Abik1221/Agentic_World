/**
 * Goofspiel agent — 2 players, 13 rounds, simultaneous bidding.
 *
 * Read references/games/goofspiel.md first. Replace `decideCard`; leave the rest.
 *
 *     pyyol login && pyyol dev --matches 5
 */

import { Adapter } from "pyyol";

import { MatchMemory } from "./_shared.mjs";

export class GoofspielAgent extends Adapter {
  name = "atlas-goofspiel";
  supportedGames = ["goofspiel"];

  #mem = new MatchMemory();

  step(view) {
    const legal = view.legal_actions ?? [];
    const safe = legal.length ? Math.min(...legal) : 1;

    if (this.#mem.alreadyAnswered(view.match_id, view.round)) {
      return { round: view.round, card: safe, rationale: "replayed turn" };
    }

    let card, why;
    try {
      ({ card, why } = this.decideCard(view));
    } catch (e) {
      // Never let the deadline decide.
      return {
        round: view.round,
        card: safe,
        rationale: `fallback: ${e instanceof Error ? e.message : e}`,
      };
    }

    // The model will occasionally name a card you do not hold. Sending it is recorded as YOUR
    // illegal move.
    if (!legal.includes(card)) {
      card = safe;
      why = `model chose an illegal card; ${why}`;
    }

    return { round: view.round, card, rationale: String(why).slice(0, 200) };
  }

  // --- your strategy -------------------------------------------------------

  /**
   * Return { card, why }.
   *
   * Bid against `prize_pool`, not `current_prize` — ties carry, so the pool is what is
   * actually on the table.
   */
  decideCard(view) {
    const legal = view.legal_actions ?? [];
    const pool = view.prize_pool ?? view.current_prize ?? 0;
    // A dull, honest baseline: spend in proportion to what is at stake. Replace it.
    const ranked = [...legal].sort((a, b) => a - b);
    const idx = Math.min(ranked.length - 1, Math.floor((pool / 13) * ranked.length));
    return { card: ranked[Math.max(0, idx)], why: `pool ${pool}, spending proportionally` };
  }
}

export default GoofspielAgent;
