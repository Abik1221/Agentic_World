/**
 * Monopoly agent — 2–8 players, board, near-perfect information.
 *
 * Read references/games/monopoly.md first. Drive off `phase` + `legal_actions`; the board lives
 * in the raw `state` object.
 *
 * Replace `decide` and `proposeTrade`; leave the rest.
 */

import { Adapter, OPEN_TO_TABLE } from "pyyol";

import { MatchMemory } from "./_shared.mjs";

export class MonopolyAgent extends Adapter {
  name = "atlas-monopoly";
  supportedGames = ["monopoly"];

  #mem = new MatchMemory();

  step(view) {
    const legal = view.legal_actions ?? [];
    if (legal.length === 0) return { action: "", rationale: "nothing legal this phase" };
    const fallback = legal.includes("end_turn") ? "end_turn" : legal[0];

    // `view.round` is the engine's own turn counter, published on every view. Key the replay
    // guard on it plus the phase: several decisions happen inside one turn.
    if (this.#mem.alreadyAnswered(view.match_id, [view.round, view.phase])) {
      return { action: fallback, rationale: "replayed turn" };
    }

    let action, property, amount, why;
    try {
      ({ action, property = 0, amount = 0, why = "" } = this.decide(view));
    } catch (e) {
      return { action: fallback, rationale: `fallback: ${e instanceof Error ? e.message : e}` };
    }

    if (!legal.includes(action)) {
      [action, property, amount, why] = [fallback, 0, 0, `illegal action; ${why}`];
    }

    // A trade needs a payload; every other action ignores it.
    const wantsTrade = action === "propose_trade" || action === "counter_trade";
    const trade = wantsTrade ? this.proposeTrade(view) : undefined;
    if (wantsTrade && !trade) {
      // Asking to trade without saying what is not a move. Fall back rather than send
      // something the engine must reject.
      action = fallback;
      why = `no trade to offer; ${why}`;
    }

    return { action, property, amount, trade, rationale: String(why).slice(0, 200) };
  }

  // --- your strategy -------------------------------------------------------

  /**
   * The deal to offer, when you chose `propose_trade` or `counter_trade`.
   *
   * Monopoly is a negotiation game — the deals decide it, not the dice — so this is worth more
   * of your attention than the dice-driven branches below.
   *
   * Three things the rules let you do here that are easy to miss:
   *
   *  - `target: OPEN_TO_TABLE` (-1) offers to EVERY seat. Anyone who can satisfy it may take
   *    it, asked in seat order, first yes wins. Use it when you want a property sold and do not
   *    care who buys. -1 and never 0 — seat 0 is a real player, so a forgotten target is an
   *    offer to them.
   *  - You may trade WHILE IN DEBT (`phase === "resolve_debt"`). Selling a property for the cash
   *    to survive a rent is legal and often better than mortgaging your own board.
   *  - You may deal BETWEEN other players' turns (`phase === "trade"`), not only on your own.
   *
   * Houses and hotels cannot be traded — sell them to the bank first.
   */
  proposeTrade(view) {
    const holdings = view.state?.holdings ?? {};
    const mine = Object.entries(holdings)
      .filter(([, h]) => h && h.owner === view.seat && !h.houses)
      .map(([idx]) => Number(idx));
    if (mine.length === 0) return undefined;
    // A deliberately dull default: put the cheapest undeveloped square on the open market.
    // Replace it — what you ask for, and who you ask, is the game.
    return { target: OPEN_TO_TABLE, give_props: [Math.min(...mine)], want_cash: 150 };
  }

  /**
   * Return { action, property, amount, why }.
   *
   * Read `phase` for the situation and `legal_actions` for what is allowed — do not assume
   * fixed field names in `state`.
   *
   * `legal_actions` is EXACT: if a verb is listed the engine will accept it, and if it is
   * missing the engine would refuse it. Never choose outside that list.
   *
   * On a `bid` during a housing-shortage auction, `property` is the square you would put the
   * piece on — the auction sells the house, and you still have to place it legally.
   */
  decide(view) {
    const legal = view.legal_actions ?? [];
    const me = view.state?.players?.[String(view.seat)] ?? {};
    const cash = Number(me.cash ?? 0);

    if (view.phase === "acquire" && legal.includes("buy")) {
      // Keep a reserve: bankruptcy is the only true loss condition, and it is usually caused
      // by buying into a rent spike.
      if (cash > 400) return { action: "buy", why: `buying with ${cash} cash in hand` };
      return {
        action: legal.includes("decline") ? "decline" : legal[0],
        why: `declining, only ${cash} cash`,
      };
    }

    if (view.phase === "auction" && legal.includes("bid")) {
      // You may mortgage mid-auction to fund a bid, and `sell_house` is withheld during a
      // housing-shortage auction because it would change the supply being fought over.
      return { action: "pass", why: "no valuation model yet" };
    }

    if (legal.includes("end_turn")) return { action: "end_turn", why: "nothing worth doing" };
    return { action: legal[0], why: "first legal action" };
  }
}

export default MonopolyAgent;
