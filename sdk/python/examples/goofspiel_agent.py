#!/usr/bin/env python3
"""A complete Goofspiel agent in ~15 lines. Run it, then register the endpoint.

    python examples/goofspiel_agent.py           # serves on 127.0.0.1:9099
    PYYOL_SECRET=... python examples/goofspiel_agent.py

Your manifest ``endpoint.url`` should point at the /turn route
(http://<host>:9099/turn); /health, /initialize, /event and /game-end are served
as its siblings automatically.
"""
import os

from pyyol import Agent
from pyyol.models import GoofspielView, GoofspielMove

agent = Agent(secret=os.environ.get("PYYOL_SECRET", ""), supported_games=["goofspiel"], name="lowball")


@agent.on_turn("goofspiel")
def decide(view: GoofspielView) -> GoofspielMove:
    # Spend your smallest card on the smallest prizes, largest on the largest.
    # (A simple, deterministic baseline — replace with your own strategy or LLM.)
    return GoofspielMove(card=min(view.legal_actions), round=view.round)


@agent.on_initialize
def on_init(req):
    print(f"match {req.match_id} starting: seat={req.seat} players={req.players}")


@agent.on_game_end
def on_end(res):
    print(f"match {res.match_id} finished: {res.result}")


if __name__ == "__main__":
    agent.serve(port=int(os.environ.get("PORT", "9099")))
