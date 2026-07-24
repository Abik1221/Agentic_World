/**
 * The v2 agent interface (mirrors the Python SDK): subclass `Adapter` and implement
 * `step` (the only required method); `initialize` and `shutdown` are optional. The
 * SDK owns everything else (transport, auth, matchmaking, replay). Wrap any
 * framework inside `step`.
 *
 *     import { Adapter } from "pyyol";
 *
 *     import { Adapter } from "pyyol";
 *     import type { GoofspielView, GoofspielMove } from "pyyol";
 *
 *     // Typed: `view` is a GoofspielView and the return is checked.
 *     class Atlas extends Adapter<GoofspielView, GoofspielMove> {
 *       name = "atlas";
 *       supportedGames = ["goofspiel"];
 *       step(view) { return { round: view.round, card: Math.min(...view.legal_actions) }; }
 *     }
 *
 *     export default new Atlas();   // pyyol dev / play discover this via pyyol.toml
 *
 * The type params are optional and default to `unknown` (so `extends Adapter` keeps
 * working); supply `Adapter<View, Move>` to get a typed `view` and a checked return.
 */
import { SUPPORTED_GAMES } from "./models.js";
import { Agent } from "./server.js";

export abstract class Adapter<View = unknown, Move = unknown> {
  name = "pyyol-agent";
  supportedGames: string[] = [...SUPPORTED_GAMES];
  secret = "";

  /** Called once at match start (optional). Return an ack object or nothing. */
  initialize(_ctx: unknown): unknown {
    return undefined;
  }

  /** Decide one move for `view` and return it. REQUIRED. Async is supported. */
  abstract step(view: View): Move | Promise<Move>;

  /** Called once when the match ends (optional). */
  shutdown(_result: unknown): void {
    /* no-op by default */
  }

  /** Build the underlying Agent that drives the real transport. */
  toAgent(): Agent {
    const a = new Agent({
      secret: this.secret || process.env.PYYOL_SECRET || "",
      supportedGames: this.supportedGames,
      name: this.name,
    });
    a.onTurn((v) => this.step(v as View) as never);
    a.onInitialize((r) => this.initialize(r));
    a.onGameEnd((r) => this.shutdown(r));
    a.onEvent(() => undefined);
    return a;
  }
}

/** Normalize a developer's exported object into an Agent: accepts an Agent, an
 *  Adapter instance, or an Adapter subclass (constructed with no args).
 *
 *  Detection is DUCK-TYPED, not `instanceof`: the CLI and the developer's project
 *  can resolve different copies of the `pyyol` module (monorepo / `npm link` /
 *  version skew), so the Adapter/Agent the developer extended may be a different
 *  class object than the one here. We match by shape (toAgent / the transport
 *  methods) so those setups still work. */
export function asAgent(obj: unknown): Agent {
  if (obj instanceof Agent) return obj;
  if (obj instanceof Adapter) return obj.toAgent();
  const o = obj as { toAgent?: unknown; prototype?: { toAgent?: unknown }; decideTurn?: unknown; handle?: unknown };
  if (o && typeof o.toAgent === "function") return (o.toAgent as () => Agent)();
  if (typeof obj === "function" && o.prototype && typeof o.prototype.toAgent === "function") {
    return new (obj as new () => Adapter)().toAgent();
  }
  if (o && typeof o.decideTurn === "function" && typeof o.handle === "function") return obj as Agent;
  throw new TypeError(
    "expected a pyyol Agent or Adapter; export one as the variable named in pyyol.toml `entry`",
  );
}
