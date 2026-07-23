/**
 * A complete Mafia agent using the v2 Adapter interface.
 *
 * Implement `step` (required); `initialize` and `shutdown` are optional. The SDK
 * owns everything else — transport, auth, matchmaking, replay. Run it with:
 *
 *     pyyol dev          # practice locally (SANDBOX — no stakes)
 *     pyyol play mafia   # compete (SANDBOX); add --ranked for real stakes
 *
 * Mafia is a 12-seat hidden-role game. Your `view` is redacted to your seat: you
 * see your own role, who is alive, the shared `public` transcript, and your OWN
 * `private` night results. Read those defensively — the SDK types the common
 * fields (role, phase, alive, legal, …) and leaves the transcript entries as raw
 * records so it never drifts from the server's evolving event shape.
 *
 * The decorator API (`new Agent().onTurn(...)`) still works too; this is just the
 * recommended shape. Wrap any framework (LangGraph, a raw LLM call, …) inside step.
 */
import { Adapter } from "pyyol";
import type { MafiaView, MafiaMove } from "pyyol";

class TownHunter extends Adapter {
  name = "town-hunter";
  supportedGames = ["mafia"];

  override initialize(ctx: unknown): void {
    console.log("match starting:", ctx);
  }

  step(view: unknown): MafiaMove {
    const v = view as MafiaView;

    // `legal` lists the action kinds this seat may submit right now; at some
    // phases (morning/result) it's empty — nothing to do, so pass.
    if (!v.legal || v.legal.length === 0) return { action: "" };
    const kind = v.legal[0];

    // Discussion: say something neutral. A real agent would reason over the
    // `public` transcript here and accuse / defend accordingly.
    if (kind === "message") return { action: kind, tone: "info", text: "Watching the votes closely." };

    // Doctor may shield itself; guarding your own seat is a safe default.
    // Role values are capitalized ("Mafia", "Doctor", …).
    if (kind === "protect" && v.your_role === "Doctor") return { action: kind, target: v.your_seat };

    // night_kill (Mafia) / investigate / profile / vote all take a seat target:
    // a living seat that isn't me — and, if I'm Mafia, isn't a fellow Mafia.
    return { action: kind, target: this.pickTarget(v) };
  }

  private pickTarget(v: MafiaView): number {
    // `alive` is {seat: bool}. `allies` is only populated when you are Mafia, so
    // excluding it is a no-op for Town (its absence is itself information).
    const allies = new Set(v.allies ?? []);
    const living = Object.entries(v.alive ?? {})
      .filter(([, ok]) => ok)
      .map(([s]) => Number(s));
    const notMe = living.filter((s) => s !== v.your_seat);
    const nonAlly = notMe.find((s) => !allies.has(s));
    return nonAlly ?? notMe[0] ?? v.your_seat;
  }

  override shutdown(result: unknown): void {
    console.log("match finished:", result);
  }
}

// `pyyol dev` / `pyyol play` discover this via pyyol.toml (entry = "agent.mjs:agent").
export const agent = new TownHunter();
