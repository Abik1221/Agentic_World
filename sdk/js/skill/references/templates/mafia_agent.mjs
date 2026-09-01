/**
 * Mafia agent — social deduction, hidden roles, day/night phases.
 *
 * Read references/games/mafia.md first. Replace `decide`; leave the rest.
 */

import { Adapter } from "pyyol";

import { MatchMemory } from "./_shared.mjs";

export class MafiaAgent extends Adapter {
  name = "atlas-mafia";
  supportedGames = ["mafia"];

  #mem = new MatchMemory();

  step(view) {
    const legal = view.legal ?? [];
    if (legal.length === 0) return { action: "", rationale: "nothing legal this phase" };
    const fallback = legal.includes("pass") ? "pass" : legal[0];

    if (this.#mem.alreadyAnswered(view.match_id, [view.day, view.phase])) {
      return { action: fallback, rationale: "replayed turn" };
    }

    let action, target, text, why;
    try {
      ({ action, target = -1, text = "", why = "" } = this.decide(view));
    } catch (e) {
      return { action: fallback, rationale: `fallback: ${e instanceof Error ? e.message : e}` };
    }

    if (!legal.includes(action)) {
      [action, target, why] = [fallback, -1, `illegal action; ${why}`];
    }
    // NEVER target yourself, and never let a forgotten target become seat 0 — target defaults
    // to -1 ("no target") because seat 0 is a real player.
    if (target === view.your_seat) {
      target = -1;
      why = `refused to target myself; ${why}`;
    }

    return { action, target, text, rationale: String(why).slice(0, 200) };
  }

  // --- your strategy -------------------------------------------------------

  /**
   * Return { action, target, text, why }.
   *
   * `text` is your PUBLIC speech and rides along with the action — one model call produces both
   * the decision and what the table hears. `why` is private and never published, which matters
   * at night: publishing your reasoning would leak the mafia's plan to the town.
   */
  decide(view) {
    const legal = view.legal ?? [];
    const alive = Object.entries(view.alive ?? {})
      .filter(([seat, isAlive]) => isAlive && Number(seat) !== view.your_seat)
      .map(([seat]) => Number(seat));

    if (legal.includes("message")) {
      return { action: "message", text: "Watching who avoids committing.", why: "gathering reads" };
    }
    if (alive.length && (legal.includes("vote") || legal.includes("kill"))) {
      const action = legal.includes("vote") ? "vote" : "kill";
      return { action, target: alive[0], why: "no read yet; first living seat" };
    }
    return { action: legal[0], why: "first legal action" };
  }
}

export default MafiaAgent;
