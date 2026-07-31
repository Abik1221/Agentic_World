"""Mafia agent — 12 seats, hidden roles, phase machine.

Read references/games/mafia.md first. Note the view uses `legal`, NOT `legal_actions`,
and `day`/`phase` rather than `round`.

Replace `decide`; leave the rest.
"""

from __future__ import annotations

from typing import Tuple

from _shared import MatchMemory
from pyyol import Adapter
from pyyol.models import MafiaMove, MafiaView


class MafiaAgent(Adapter):
    name = "atlas-mafia"
    supported_games = ["mafia"]

    def __init__(self) -> None:
        self.mem = MatchMemory()

    def step(self, view: MafiaView) -> MafiaMove:
        # `legal` — not `legal_actions`. Getting this wrong means every move is
        # rejected and the engine plays for you.
        legal = view.legal or []
        if not legal:
            return MafiaMove(action="", rationale="nothing legal this phase")
        fallback = legal[0]

        # A turn is (day, phase) here — there is no round number.
        if self.mem.already_answered(view.match_id, (view.day, view.phase)):
            return MafiaMove(action=fallback, rationale="replayed turn")

        try:
            action, target, text, why = self.decide(view)
        except Exception as e:  # noqa: BLE001
            return MafiaMove(action=fallback, rationale=f"fallback: {e}")

        if action not in legal:
            action, target, text, why = fallback, -1, "", f"illegal action; {why}"

        # target stays -1 when unused: seat 0 is a REAL player, so a forgotten target
        # would otherwise silently act on them.
        return MafiaMove(
            action=action, target=target, text=text[:400], rationale=why[:200]
        )

    # --- your strategy -----------------------------------------------------

    def decide(self, view: MafiaView) -> Tuple[str, int, str, str]:
        """Return (action, target, text, reason).

        `view.public` is the table transcript; `view.private` carries what only you
        know (a Detective's finding arrives there and nowhere else). Rebuild your
        read of each seat from them every turn.
        """
        notes = self.mem.get(view.match_id)["notes"]
        living = [
            s for s, ok in (view.alive or {}).items() if ok and s != view.your_seat
        ]
        suspect = max(living, key=lambda s: notes.get(s, 0), default=-1)

        if "message" in (view.legal or []):
            return (
                "message",
                -1,
                f"Seat {suspect} has been quiet. Thoughts?",
                "opening a line on the current suspect",
            )
        if "vote" in (view.legal or []) and suspect >= 0:
            return "vote", suspect, "", f"voting {suspect}, my standing read"
        for act in ("investigate", "protect", "night_kill", "profile"):
            if act in (view.legal or []) and suspect >= 0:
                return act, suspect, "", f"{act} on {suspect}"
        return (view.legal or [""])[0], -1, "", "no better option this phase"


agent = MafiaAgent()
