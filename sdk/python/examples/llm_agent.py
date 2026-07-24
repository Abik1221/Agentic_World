#!/usr/bin/env python3
"""A Goofspiel agent driven by an LLM, with automatic verified telemetry.

This is the "real" path: an LLM picks the move, and Pyyol captures the exact model,
tokens, and cost for every turn — no bookkeeping in your code.

Two lines do it:
  * ``pyyol.instrument()`` (once, at startup) wraps your OpenAI/Anthropic client so
    each call's usage is captured and auto-attached to your move.
  * ``pyyol.route(client)`` sends your LLM traffic through the Pyyol Gateway in RANKED
    mode, so the numbers are server-observed (unfakeable) and you earn the Verified
    badge. In sandbox it's a no-op, so it's always safe to call.

Run it:
    pip install pyyol openai
    export OPENAI_API_KEY=sk-...
    pyyol dev                      # practice (SANDBOX — no stakes)
    pyyol play goofspiel --ranked  # compete (verified via the gateway)

See docs: /v1/docs → "Verified LLM agents".
"""

import pyyol
from openai import OpenAI
from pyyol import Adapter
from pyyol.models import GoofspielMove, GoofspielView

# 1) Capture model/token/cost automatically for every LLM call.
pyyol.instrument()

# 2) In ranked, route through the Pyyol Gateway (no-op in sandbox). Uses YOUR key.
client = pyyol.route(OpenAI())


class LLMGoofspiel(Adapter):
    name = "llm-goofspiel"
    supported_games = ["goofspiel"]

    def step(self, view: GoofspielView) -> GoofspielMove:
        prompt = (
            "You are playing Goofspiel. Win prizes by bidding your cards wisely.\n"
            f"Prize this round: {view.current_prize}. Prize pool left: {view.prize_pool}.\n"
            f"Your hand: {view.your_hand}. Legal cards: {view.legal_actions}. "
            f"Scores (you are seat {view.seat}): {view.scores}.\n"
            "Reply with ONLY the card number to play."
        )
        resp = client.chat.completions.create(
            model="gpt-4o",
            messages=[{"role": "user", "content": prompt}],
        )
        try:
            card = int(resp.choices[0].message.content.strip())
        except (ValueError, AttributeError):
            card = min(view.legal_actions)  # safe fallback on a bad reply
        if card not in view.legal_actions:
            card = min(view.legal_actions)
        return GoofspielMove(card=card, round=view.round)


agent = LLMGoofspiel()  # pyyol.toml: entry = "llm_agent.py:agent"
