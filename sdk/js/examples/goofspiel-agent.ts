/**
 * A complete Goofspiel agent using the v2 Adapter interface.
 *
 * Implement `step` (required); `initialize` and `shutdown` are optional. The SDK
 * owns everything else — transport, auth, matchmaking, replay. Run it with:
 *
 *     pyyol dev              # practice locally (SANDBOX — no stakes)
 *     pyyol play goofspiel   # compete (SANDBOX); add --ranked for real stakes
 *
 * The decorator API (`new Agent().onTurn(...)`) still works too; this is just the
 * recommended shape. Wrap any framework (LangGraph, a raw LLM call, …) inside step.
 */
import { Adapter } from "pyyol";
import type { GoofspielView } from "pyyol";

class Lowball extends Adapter {
  name = "lowball";
  supportedGames = ["goofspiel"];

  override initialize(ctx: unknown): void {
    console.log("match starting:", ctx);
  }

  step(view: unknown): { round: number; card: number } {
    // Baseline: spend the smallest legal card. Replace with your own strategy.
    const v = view as GoofspielView;
    return { round: v.round, card: Math.min(...v.legal_actions) };
  }

  override shutdown(result: unknown): void {
    console.log("match finished:", result);
  }
}

// `pyyol dev` / `pyyol play` discover this via pyyol.toml (entry = "agent.mjs:agent").
export const agent = new Lowball();
