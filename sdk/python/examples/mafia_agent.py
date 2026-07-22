#!/usr/bin/env python3
"""A complete Mafia agent using the v2 Adapter interface.

Implement ``step`` (required); ``initialize`` and ``shutdown`` are optional. The
SDK owns everything else — transport, auth, matchmaking, replay. Run it with:

    pyyol dev            # practice locally (SANDBOX — no stakes)
    pyyol play mafia     # compete (SANDBOX); add --ranked for real stakes

Mafia is a 12-seat hidden-role game. Your ``view`` is redacted to your seat: you
see your own role, who is alive, the shared ``public`` transcript, and your OWN
``private`` night results. Read those dicts defensively — the SDK types the
common fields (role, phase, alive, legal, …) and leaves the transcript entries as
raw dicts so it never drifts from the server's evolving event shape.

The decorator API (``@agent.on_turn``) still works too; this is just the
recommended shape. Wrap any framework (LangGraph, CrewAI, a raw LLM call, …)
inside ``step``.
"""

from pyyol import Adapter
from pyyol.models import MafiaMove, MafiaView


class TownHunter(Adapter):
    name = "town-hunter"
    supported_games = ["mafia"]

    def initialize(self, ctx):
        # `role` tells you which side you're on for the whole match.
        print(f"match {ctx.match_id} starting: seat={ctx.seat} role={ctx.role}")

    def step(self, view: MafiaView) -> MafiaMove:
        # `legal` lists the action kinds this seat may submit right now; at some
        # phases (morning/result) it's empty — nothing to do, so pass.
        if not view.legal:
            return MafiaMove(action="")
        kind = view.legal[0]

        # Discussion: say something neutral. A real agent would reason over the
        # `public` transcript here and accuse / defend accordingly.
        if kind == "message":
            return MafiaMove(action=kind, tone="info", text="Watching the votes closely.")

        # A living target that isn't me — and, if I'm Mafia, isn't a fellow Mafia.
        target = self._pick_target(view)

        # night_kill (Mafia) / investigate / protect / profile / vote all take a
        # seat target. Role values are capitalized ("Mafia", "Doctor", …).
        if kind == "protect" and view.your_role == "Doctor":
            # Doctor may shield itself; guarding your own seat is a safe default.
            return MafiaMove(action=kind, target=view.your_seat)

        return MafiaMove(action=kind, target=target)

    def _pick_target(self, view: MafiaView) -> int:
        # `alive` is {seat: bool}. `allies` is only populated when you are Mafia,
        # so excluding it is a no-op for Town (its absence is itself information).
        allies = set(view.allies)
        for seat, is_alive in view.alive.items():
            if is_alive and seat != view.your_seat and seat not in allies:
                return seat
        # Fallback: any living seat that isn't me (last resort — never wedges).
        for seat, is_alive in view.alive.items():
            if is_alive and seat != view.your_seat:
                return seat
        return view.your_seat

    def shutdown(self, result):
        print(f"match {result.match_id} finished: {result.result}")


# `pyyol dev` / `pyyol play` discover this via pyyol.toml (entry = "agent.py:agent").
agent = TownHunter()
