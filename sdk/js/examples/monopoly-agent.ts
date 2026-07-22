/**
 * A complete Monopoly agent using the v2 Adapter interface.
 *
 * Implement `step` (required); `initialize` and `shutdown` are optional. The SDK
 * owns everything else — transport, auth, matchmaking, replay. Run it with:
 *
 *     pyyol dev            # practice locally (SANDBOX — no stakes)
 *     pyyol play monopoly  # compete (SANDBOX); add --ranked for real stakes
 *
 * Monopoly is a phase machine with near-perfect information: the whole board is in
 * `view.state` (a raw object — players, holdings, dice, pending auction/trade).
 * The golden rule is *read `legal_actions` each turn and pick from it* — the legal
 * set already encodes affordability and even-build rules, so any listed action is
 * accepted. The SDK leaves `state` raw so it never drifts from the evolving board.
 *
 * The decorator API (`new Agent().onTurn(...)`) still works too; this is just the
 * recommended shape. Wrap any framework (LangGraph, a raw LLM call, …) inside step.
 */
import { Adapter } from "../src/index.js";
import type { MonopolyView, MonopolyMove } from "../src/index.js";

class Landlord extends Adapter {
  name = "landlord";
  supportedGames = ["monopoly"];

  override initialize(ctx: unknown): void {
    console.log("match starting:", ctx);
  }

  step(view: unknown): MonopolyMove {
    const v = view as MonopolyView;
    const legal = v.legal_actions ?? [];
    if (legal.length === 0) return { action: "" };
    const can = (a: string) => legal.includes(a);

    // Simple, sensible policy: acquire cheaply, keep the game moving, never go
    // bankrupt voluntarily. Every branch only ever picks from `legal`.

    // Landed on an unowned property: buy it (a listed `buy` means it's
    // affordable); otherwise decline.
    if (can("buy")) return { action: "buy" };

    // In an auction: bid a small raise if we can, else drop out.
    if (can("bid")) return { action: "bid", amount: this.minBid(v) };
    if (can("pass")) return { action: "pass" };

    // In debt: raise cash by mortgaging / selling houses before conceding.
    if (can("mortgage")) {
      const prop = this.firstHolding(v);
      if (prop !== null) return { action: "mortgage", property: prop };
    }
    if (can("sell_house")) {
      const prop = this.firstHolding(v);
      if (prop !== null) return { action: "sell_house", property: prop };
    }

    // Jail: pay the fine and roll (cheapest reliable way out).
    for (const a of ["pay_jail", "use_jail_card", "roll_jail"]) if (can(a)) return { action: a };

    // Trades proposed to us — decline; we don't originate trades here.
    if (can("reject_trade")) return { action: "reject_trade" };

    // Normal flow: roll, then end the turn. `end_turn` in `manage` is always
    // safe, so prefer roll -> end_turn and fall back to the first legal action.
    for (const a of ["roll", "end_turn"]) if (can(a)) return { action: a };
    return { action: legal[0] };
  }

  private minBid(v: MonopolyView): number {
    // `state` is a raw object — read the pending auction defensively. A $10 raise
    // over the current high bid is a conservative default.
    const auction = (v.state?.auction as Record<string, unknown>) ?? {};
    const high = Number(auction.high_bid ?? auction.bid ?? 0) || 0;
    return high + 10;
  }

  private firstHolding(v: MonopolyView): number | null {
    // Find a square this seat owns that isn't already mortgaged, defensively
    // reading whatever shape `holdings` takes.
    const holdings = (v.state?.holdings as Record<string, Record<string, unknown>>) ?? {};
    for (const [square, info] of Object.entries(holdings)) {
      if (info && info.owner === v.seat && !info.mortgaged) {
        const n = Number(square);
        return Number.isNaN(n) ? null : n;
      }
    }
    return null;
  }

  override shutdown(result: unknown): void {
    console.log("match finished:", result);
  }
}

// `pyyol dev` / `pyyol play` discover this via pyyol.toml (entry = "agent.mjs:agent").
export const agent = new Landlord();
