"""Goofspiel agent — 2 players, 13 rounds, simultaneous bidding.

Read references/games/goofspiel.md first. Replace `decide_card`; leave the rest.

    pyyol login && pyyol dev --matches 5
"""

from __future__ import annotations

from typing import List, Tuple

from _shared import MatchMemory
from pyyol import Adapter
from pyyol.models import GoofspielMove, GoofspielView


class GoofspielAgent(Adapter):
    name = "atlas-goofspiel"
    supported_games = ["goofspiel"]

    def __init__(self) -> None:
        self.mem = MatchMemory()

    def step(self, view: GoofspielView) -> GoofspielMove:
        safe = min(view.legal_actions) if view.legal_actions else 1

        if self.mem.already_answered(view.match_id, view.round):
            return GoofspielMove(round=view.round, card=safe, rationale="replayed turn")

        try:
            card, why = self.decide_card(view)
        except Exception as e:  # noqa: BLE001 — never let the deadline decide
            return GoofspielMove(round=view.round, card=safe, rationale=f"fallback: {e}")

        # The model will occasionally name a card you do not hold. Sending it is
        # recorded as YOUR illegal move.
        if card not in view.legal_actions:
            card, why = safe, f"model chose an illegal card; {why}"

        return GoofspielMove(round=view.round, card=card, rationale=why[:200])

    # --- your strategy -----------------------------------------------------

    def decide_card(self, view: GoofspielView) -> Tuple[int, str]:
        """Return (card, one-line reason).

        Bid against `prize_pool`, not `current_prize` — ties carry.
        """
        opp = self.opponent_hand(view)
        threat = max(opp) if opp else 0
        beats = [c for c in view.legal_actions if c > threat]

        # Win by ONE. Pips saved on cheap prizes buy the expensive ones later.
        if beats and view.prize_pool >= 7:
            return min(beats), f"pool {view.prize_pool}: cheapest card over {threat}"
        return min(view.legal_actions), f"pool {view.prize_pool}: conceding cheaply"

    @staticmethod
    def opponent_hand(view: GoofspielView) -> List[int]:
        """Exactly what they still hold — identical starting hands mean their played
        cards tell you the rest."""
        spent = {r["opp_card"] for r in (view.history or []) if r.get("opp_card")}
        return [c for c in range(1, 14) if c not in spent]


agent = GoofspielAgent()
