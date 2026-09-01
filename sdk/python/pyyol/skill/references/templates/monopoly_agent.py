"""Monopoly agent — 2–8 players, board, near-perfect information.

Read references/games/monopoly.md first. Drive off `phase` + `legal_actions`; the
board lives in the raw `state` dict.

Replace `decide`; leave the rest.
"""

from __future__ import annotations

from typing import Any

from _shared import MatchMemory
from pyyol import Adapter
from pyyol.models import OPEN_TO_TABLE, MonopolyMove, MonopolyTrade, MonopolyView


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

        # `view.round` is the engine's own turn counter, published on every view. Key the
        # replay guard on it plus the phase: several decisions happen inside one turn.
        turn_no = view.round
        if self.mem.already_answered(view.match_id, (turn_no, view.phase)):
            return MonopolyMove(action=fallback, rationale="replayed turn")

        try:
            action, prop, amount, why = self.decide(view)
        except Exception as e:  # noqa: BLE001
            return MonopolyMove(action=fallback, rationale=f"fallback: {e}")

        if action not in legal:
            action, prop, amount, why = fallback, 0, 0, f"illegal action; {why}"

        # A trade needs a payload; every other action ignores it.
        trade = self.propose_trade(view) if action in ("propose_trade", "counter_trade") else None
        if action in ("propose_trade", "counter_trade") and trade is None:
            # Asking to trade without saying what is not a move. Fall back rather than send
            # something the engine must reject.
            action, why = fallback, f"no trade to offer; {why}"

        return MonopolyMove(
            action=action, property=prop, amount=amount, trade=trade, rationale=why[:200]
        )

    # --- your strategy -----------------------------------------------------

    def propose_trade(self, view: MonopolyView) -> MonopolyTrade | None:
        """The deal to offer, when you chose `propose_trade` or `counter_trade`.

        Monopoly is a negotiation game — the deals decide it, not the dice — so this is worth
        more of your attention than the dice-driven branches below.

        Three things the rules let you do here that are easy to miss:

        * `target=OPEN_TO_TABLE` (-1) offers to EVERY seat. Anyone who can satisfy it may take
          it, asked in seat order, first yes wins. Use it when you want a property sold and do
          not care who buys. -1 and never 0 — seat 0 is a real player, so a forgotten target is
          an offer to them.
        * You may trade WHILE IN DEBT (`phase == "resolve_debt"`). Selling a property for the
          cash to survive a rent is legal and often better than mortgaging your own board.
        * You may deal BETWEEN other players' turns (`phase == "trade"`), not only on your own.

        Houses and hotels cannot be traded — sell them to the bank first.
        """
        state = view.state or {}
        mine = [
            int(idx)
            for idx, h in (state.get("holdings") or {}).items()
            if isinstance(h, dict) and h.get("owner") == view.seat and not h.get("houses")
        ]
        if not mine:
            return None
        # A deliberately dull default: put the cheapest undeveloped square on the open market.
        # Replace it — what you ask for, and who you ask, is the game.
        return MonopolyTrade(target=OPEN_TO_TABLE, give_props=[min(mine)], want_cash=150)

    def decide(self, view: MonopolyView) -> tuple[str, int, int, str]:
        """Return (action, property, amount, reason).

        Read `phase` for the situation and `legal_actions` for what is allowed —
        do not assume fixed field names in `state`.

        `legal_actions` is EXACT: if a verb is listed the engine will accept it, and if it is
        missing the engine would refuse it. Never choose outside that list.

        On a `bid` during a housing-shortage auction, `property` is the square you would put
        the piece on — the auction sells the house, and you still have to place it legally.
        """
        legal = view.legal_actions or []
        me: dict[str, Any] = (view.state or {}).get("players", {}).get(str(view.seat), {})
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
