"""A correct Pyyol agent, ready to run.

Every rule that fails SILENTLY on this platform is already honoured here, so start
from this file rather than from a blank one. The strategy is deliberately simple —
replace `decide_card`; leave the scaffolding alone.

    pip install "pyyol>=1.7.0"
    pyyol login && pyyol dev --matches 5
"""

from __future__ import annotations

import os
from typing import Any, Dict, List

import pyyol
from pyyol import Adapter
from pyyol.models import GoofspielMove, GoofspielView

# Instrument ONCE at import. Safe to call repeatedly, but once is the intent.
pyyol.instrument()


def _client():
    """Your provider client, routed through the Pyyol Gateway.

    route() is what makes usage server-measured and attaches the per-turn proof that
    the decision was really made by a model. Skip it and the agent is unverifiable;
    in ranked its matches can be voided.

    Groq works either way: the native `groq` package, or the OpenAI SDK pointed at
    Groq's compatible endpoint (which is what this does).
    """
    from openai import OpenAI

    c = OpenAI(
        api_key=os.environ["GROQ_API_KEY"],
        base_url="https://api.groq.com/openai/v1",
    )
    return pyyol.route(c)


class Atlas(Adapter):
    name = "atlas"
    supported_games = ["goofspiel"]

    def __init__(self) -> None:
        # Keyed by match_id — NOT built in initialize().
        #
        # initialize() is not guaranteed and not once per match: you can join a match
        # already in progress, and one connection serves many matches. State created
        # there and reused leaks into the next match, which looks exactly like a
        # strategy bug and is not one.
        self._memory: Dict[str, Dict[str, Any]] = {}
        self._client = None

    def _state(self, match_id: str) -> Dict[str, Any]:
        """Per-match state, created lazily on first sight of the match."""
        return self._memory.setdefault(match_id, {"seen_rounds": set()})

    def step(self, view: GoofspielView) -> GoofspielMove:
        st = self._state(view.match_id)

        # Idempotent per (match_id, round): a reconnect can redeliver a turn, and
        # re-running an expensive model call for a decision already made is waste.
        if view.round in st["seen_rounds"]:
            return GoofspielMove(round=view.round, card=self._safe(view))
        st["seen_rounds"].add(view.round)

        try:
            card, why = self.decide_card(view)
        except Exception as e:  # noqa: BLE001
            # NEVER let an exception reach the deadline. A fallback you chose beats a
            # fallback the engine chose, and the engine's counts against you.
            return GoofspielMove(
                round=view.round, card=self._safe(view), rationale=f"fallback: {e}"
            )

        # Validate the model's answer. It will occasionally name a card you do not
        # hold; sending it is recorded as YOUR illegal move.
        if card not in view.legal_actions:
            card, why = self._safe(view), f"model chose an illegal card; {why}"

        # rationale is published to spectators and stored in the trace — it is what
        # makes a replay readable instead of a list of numbers.
        return GoofspielMove(round=view.round, card=card, rationale=why[:200])

    # --- replace this ------------------------------------------------------

    def decide_card(self, view: GoofspielView) -> tuple[int, str]:
        """Return (card, one-line reason).

        `view.history` holds every resolved round, so the whole match is derivable
        from this one payload — with identical 1..13 hands, the opponent's played
        cards tell you exactly what they still hold.
        """
        opp_left = self._opponent_hand(view)
        threat = max(opp_left) if opp_left else 0

        # Cheapest card that still beats their best remaining. Overpaying early is
        # what loses the expensive prizes later.
        winners = [c for c in view.legal_actions if c > threat]
        if winners and view.prize_pool >= 7:
            return min(winners), f"prize {view.prize_pool}: cheapest beat over {threat}"
        return min(view.legal_actions), f"prize {view.prize_pool}: not worth contesting"

    # --- helpers -----------------------------------------------------------

    @staticmethod
    def _safe(view: GoofspielView) -> int:
        """A legal move, always."""
        return min(view.legal_actions) if view.legal_actions else 1

    @staticmethod
    def _opponent_hand(view: GoofspielView) -> List[int]:
        """What the opponent still holds, derived from history."""
        spent = {r["opp_card"] for r in (view.history or []) if r.get("opp_card")}
        return [c for c in range(1, 14) if c not in spent]


agent = Atlas()  # `pyyol dev` / `pyyol play` discover this via pyyol.toml
