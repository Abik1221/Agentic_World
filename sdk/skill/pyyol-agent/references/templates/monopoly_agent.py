"""Monopoly agent — 2–8 players, board, near-perfect information.

Read references/games/monopoly.md first. Drive off `phase` + `legal_actions`; the
board lives in the raw `state` dict.

Replace `decide`; leave the rest.
"""

from __future__ import annotations

from typing import Any, Dict, Tuple

from _shared import MatchMemory
from pyyol import Adapter
from pyyol.models import MonopolyMove, MonopolyView


class MonopolyAgent(Adapter):
    name = "atlas-monopoly"
    supported_games = ["monopoly"]

    def __init__(self) -> None:
        self.mem = MatchMemory()

    def step(self, view: MonopolyView) -> MonopolyMove:
        legal = view.legal_actions or []
        if not legal:
            return MonopolyMove(action="", rationale="nothing legal this phase")
        fallback = "end_turn" if "end_turn" in legal else legal[0]

        # Monopoly has no round number — the phase plus the board's turn counter is
        # the closest thing, so key on both.
        turn_no = (view.state or {}).get("turn", 0)
        if self.mem.already_answered(view.match_id, (turn_no, view.phase)):
            return MonopolyMove(action=fallback, rationale="replayed turn")

        try:
            action, prop, amount, why = self.decide(view)
        except Exception as e:  # noqa: BLE001
            return MonopolyMove(action=fallback, rationale=f"fallback: {e}")

        if action not in legal:
            action, prop, amount, why = fallback, 0, 0, f"illegal action; {why}"

        return MonopolyMove(
            action=action, property=prop, amount=amount, rationale=why[:200]
        )

    # --- your strategy -----------------------------------------------------

    def decide(self, view: MonopolyView) -> Tuple[str, int, int, str]:
        """Return (action, property, amount, reason).

        Read `phase` for the situation and `legal_actions` for what is allowed —
        do not assume fixed field names in `state`.
        """
        legal = view.legal_actions or []
        me: Dict[str, Any] = (
            (view.state or {}).get("players", {}).get(str(view.seat), {})
        )
        cash = int(me.get("cash", 0) or 0)

        if view.phase == "acquire" and "buy" in legal:
            # Keep a reserve: bankruptcy is the only true loss condition, and it is
            # usually caused by buying into a rent spike.
            if cash > 400:
                return "buy", 0, 0, f"buying with {cash} cash in hand"
            return (
                "decline" if "decline" in legal else legal[0],
                0,
                0,
                f"declining, only {cash} cash",
            )

        if view.phase == "auction" and "pass" in legal:
            return "pass", 0, 0, "not overpaying at auction"

        if view.phase == "roll" and "roll" in legal:
            return "roll", 0, 0, "rolling"

        if "end_turn" in legal:
            return "end_turn", 0, 0, "nothing worth doing this phase"
        return legal[0], 0, 0, "first legal action"


agent = MonopolyAgent()
