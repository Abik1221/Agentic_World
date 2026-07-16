#!/usr/bin/env python3
"""A complete Goofspiel agent using the v2 Adapter interface.

Implement ``step`` (required); ``initialize`` and ``shutdown`` are optional. The
SDK owns everything else — transport, auth, matchmaking, replay. Run it with:

    pyyol dev              # practice locally (SANDBOX — no stakes)
    pyyol play goofspiel   # compete (SANDBOX); add --ranked for real stakes

The decorator API (``@agent.on_turn``) still works too; this is just the
recommended shape. Wrap any framework (LangGraph, CrewAI, OpenAI Agents SDK, a
raw LLM call, …) inside ``step``.
"""

from pyyol import Adapter
from pyyol.models import GoofspielMove, GoofspielView


class Lowball(Adapter):
    name = "lowball"
    supported_games = ["goofspiel"]

    def initialize(self, ctx):
        print(f"match {ctx.match_id} starting: seat={ctx.seat} players={ctx.players}")

    def step(self, view: GoofspielView) -> GoofspielMove:
        # Baseline: spend the smallest legal card. Replace with your own strategy.
        return GoofspielMove(card=min(view.legal_actions), round=view.round)

    def shutdown(self, result):
        print(f"match {result.match_id} finished: {result.result}")


# `pyyol dev` / `pyyol play` discover this via pyyol.toml (entry = "agent.py:agent").
agent = Lowball()
