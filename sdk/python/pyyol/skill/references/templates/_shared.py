"""Scaffolding every Pyyol agent needs, regardless of game.

Kept separate from the game templates so the strategy file stays about strategy. You
should not need to change anything here.
"""

from __future__ import annotations

import os
from typing import Any, Dict

import pyyol

# Instrument ONCE at import. Without this nothing is measured and the agent cannot be
# verified; in ranked, unverified decisions can have a match voided.
pyyol.instrument()


def routed_client() -> Any:
    """Your provider client, routed through the Pyyol Gateway.

    route() is what makes usage server-measured and attaches the per-turn proof that a
    decision was really made by a model. It warns loudly if it cannot identify the
    client — pass provider= explicitly if you see that.

    Groq works either way: the native `groq` package, or the OpenAI SDK pointed at
    Groq's OpenAI-compatible endpoint (below).
    """
    from openai import OpenAI

    return pyyol.route(
        OpenAI(
            api_key=os.environ["GROQ_API_KEY"],
            base_url="https://api.groq.com/openai/v1",
        )
    )


class MatchMemory:
    """Per-match state, created lazily and keyed on match_id.

    THE most expensive mistake on this platform is building per-match state in
    initialize() and reusing it. initialize() is neither guaranteed nor once per
    match — a match can be joined in progress, and one connection serves many. Reused
    state means the agent plays match two with match one's memory, which looks exactly
    like a strategy bug and is not one.
    """

    def __init__(self) -> None:
        self._m: Dict[str, Dict[str, Any]] = {}

    def get(self, match_id: str) -> Dict[str, Any]:
        return self._m.setdefault(match_id, {"seen": set(), "notes": {}})

    def already_answered(self, match_id: str, turn_key: Any) -> bool:
        """True if this exact turn was already handled — a reconnect can redeliver it,
        and re-running an expensive model call for a decision already made is waste."""
        seen = self.get(match_id)["seen"]
        if turn_key in seen:
            return True
        seen.add(turn_key)
        return False
