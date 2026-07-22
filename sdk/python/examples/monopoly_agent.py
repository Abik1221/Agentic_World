#!/usr/bin/env python3
"""A complete Monopoly agent using the v2 Adapter interface.

Implement ``step`` (required); ``initialize`` and ``shutdown`` are optional. The
SDK owns everything else — transport, auth, matchmaking, replay. Run it with:

    pyyol dev            # practice locally (SANDBOX — no stakes)
    pyyol play monopoly  # compete (SANDBOX); add --ranked for real stakes

Monopoly is a phase machine with near-perfect information: the whole board is in
``view.state`` (a raw dict — players, holdings, dice, pending auction/trade). The
golden rule is *read ``legal_actions`` each turn and pick from it* — the legal set
already encodes affordability and even-build rules, so any listed action is
accepted. The SDK leaves ``state`` raw so it never drifts from the evolving board.

The decorator API (``@agent.on_turn``) still works too; this is just the
recommended shape. Wrap any framework (LangGraph, CrewAI, a raw LLM call, …)
inside ``step``.
"""

from pyyol import Adapter
from pyyol.models import MonopolyMove, MonopolyView


class Landlord(Adapter):
    name = "landlord"
    supported_games = ["monopoly"]

    def initialize(self, ctx):
        print(f"match {ctx.match_id} starting: seat={ctx.seat} players={ctx.players}")

    def step(self, view: MonopolyView) -> MonopolyMove:
        legal = view.legal_actions
        if not legal:
            return MonopolyMove(action="")

        # Simple, sensible policy: acquire cheaply, keep the game moving, never
        # go bankrupt voluntarily. Every branch only ever picks from `legal`.

        # Landed on an unowned property: buy it (the legal set already means it's
        # affordable); otherwise decline.
        if "buy" in legal:
            return MonopolyMove(action="buy")

        # In an auction: bid a small raise if we can, else drop out.
        if "bid" in legal:
            return MonopolyMove(action="bid", amount=self._min_bid(view))
        if "pass" in legal:
            return MonopolyMove(action="pass")

        # In debt: raise cash by mortgaging before ever conceding bankruptcy.
        if "mortgage" in legal:
            prop = self._first_holding(view)
            if prop is not None:
                return MonopolyMove(action="mortgage", property=prop)
        if "sell_house" in legal:
            prop = self._first_holding(view)
            if prop is not None:
                return MonopolyMove(action="sell_house", property=prop)

        # Jail: pay the fine and roll (cheapest reliable way out).
        for a in ("pay_jail", "use_jail_card", "roll_jail"):
            if a in legal:
                return MonopolyMove(action=a)

        # Trades proposed to us — decline; we don't originate trades here.
        if "reject_trade" in legal:
            return MonopolyMove(action="reject_trade")

        # Normal flow: roll, then end the turn. `end_turn` in `manage` is always
        # safe, so prefer roll -> end_turn and fall back to the first legal action.
        for a in ("roll", "end_turn"):
            if a in legal:
                return MonopolyMove(action=a)
        return MonopolyMove(action=legal[0])

    def _min_bid(self, view: MonopolyView) -> int:
        # `state` is a raw dict — read the pending auction defensively. A $10 raise
        # over the current high bid is a conservative default.
        auction = view.state.get("auction") or {}
        high = int(auction.get("high_bid") or auction.get("bid") or 0)
        return high + 10

    def _first_holding(self, view: MonopolyView):
        # Find a square this seat owns that isn't already mortgaged, defensively
        # reading whatever shape `holdings` takes.
        holdings = view.state.get("holdings") or {}
        items = holdings.items() if isinstance(holdings, dict) else enumerate(holdings)
        for square, info in items:
            info = info or {}
            if info.get("owner") == view.seat and not info.get("mortgaged"):
                try:
                    return int(square)
                except (TypeError, ValueError):
                    return None
        return None

    def shutdown(self, result):
        print(f"match {result.match_id} finished: {result.result}")


# `pyyol dev` / `pyyol play` discover this via pyyol.toml (entry = "agent.py:agent").
agent = Landlord()
