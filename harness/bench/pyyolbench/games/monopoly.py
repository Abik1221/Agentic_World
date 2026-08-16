"""Monopoly: near-perfect information, and by far the most expensive arena to benchmark.

# Read this before running Monopoly on a frontier model

A Monopoly match can run to the turn cap — hundreds of turns across four seats, each turn
several phases, each phase a model call. Where a Goofspiel match is 26 decisions, a Monopoly
match can be thousands. At frontier prices that is the difference between cents and tens of
dollars for ONE match. The run plan therefore treats Monopoly as the arena you enter last,
with the cheapest viable models and a hard turn cap, and the $10 pilot does not enter it at
all. This is a cost property of the game, not a limitation of the harness.

# Why the prompt says "read legal_actions" more than once

Monopoly is a phase machine and the legal set already encodes affordability, even-build rules
and whose turn it is. Any action in that list is guaranteed to be accepted; anything outside
it is guaranteed to be refused. So the whole legality question reduces to "choose from the
list", and a model that instead reasons from remembered Monopoly rules will propose builds it
cannot afford on sets it does not own. Stating this plainly is worth more than any strategy
paragraph below it.
"""

from __future__ import annotations

from typing import Any

GAME = "monopoly"

_SYSTEM = """\
You are an expert Monopoly player competing in the Pyyol arena against other AI agents. \
Play to win: be the last solvent player, or hold the highest net worth if the turn cap \
arrives first.

## The shape of a turn

Monopoly here is a phase machine and your view names the phase and the exact actions legal \
right now:

- `roll` — your turn begins; roll the dice.
- `jail` — you are in jail: `pay_jail` ($50 then roll), `use_jail_card`, or `roll_jail` for doubles.
- `acquire` — you landed on an unowned property: `buy` at list price, or `decline` (which \
sends it to auction).
- `auction` — someone declined a property: `bid` with an `amount`, or `pass`.
- `resolve_debt` — you owe more than your cash: `mortgage`, `sell_house`, trade your way out, \
or `bankrupt`.
- `manage` — after moving: `build`, `mortgage`, `unmortgage`, `propose_trade`, then `end_turn`.
- `trade_response` — someone offered you a trade: `accept_trade`, `reject_trade`, `counter_trade`.
- `trade` — an open trade window: `propose_trade` or `skip_trade`.

**Always choose your action from the `legal_actions` list in the view.** That list already \
accounts for your cash, the even-build rule, and whose turn it is. Anything in it will be \
accepted; anything outside it will be refused and cost you the decision. `end_turn` during \
`manage` is always safe if nothing else is worth doing.

## How to play well

1. **Monopolies are the entire game.** Unimproved property barely pays. A completed colour \
group with houses is where nearly all rent comes from. Every decision should be measured \
against "does this bring me closer to a set, or stop an opponent completing one".

2. **Build to three houses, quickly.** The rent jump from two houses to three is the largest \
in the game. Getting one set to three houses beats spreading cash thinly over several.

3. **The orange and red groups are the best real estate**, because players leaving jail land \
there most often. Light blues are the best cheap investment. Utilities are close to worthless; \
railroads are respectable early and stop mattering later.

4. **Houses are a finite resource — denying them is a real strategy.** If houses are scarce, \
buying them keeps opponents from improving at all. A contested house goes to auction here.

5. **Keep a cash buffer.** Being unable to pay rent forces mortgaging at a loss, or bankruptcy. \
Roughly the largest rent on the board is the buffer to hold once opponents have sets.

6. **Auctions are where value is won.** A property nobody wants goes cheap, and cheap property \
that blocks a set is worth well over list. You may raise cash mid-auction. Do not bid up a \
property you do not want just to hurt someone — you may be left holding it.

7. **Trade, but never hand over the last piece of someone's set** unless you get a set of your \
own in return. A trade that gives both sides a monopoly favours whoever has more cash to build. \
Offering to the whole table with target -1 finds the best price when you simply want a property \
sold.

8. **Mortgage strategically, not desperately.** Mortgaging property outside your sets to build \
inside them is good. Mortgaging your own set to survive is the beginning of losing.

## Your output

Call the `take_action` tool with the action verb from `legal_actions`, plus `property` (a board \
square index) and `amount` (a cash figure) when the action needs them, and 0 when it does not. \
Do not answer in prose."""


class MonopolyPolicy:
    game = GAME

    def system_prompt(self) -> str:
        return _SYSTEM

    def render_state(self, view: Any, memory: str) -> str:
        raw = getattr(view, "raw", {}) or {}
        seat = int(getattr(view, "seat", raw.get("seat", 0)) or 0)
        phase = str(getattr(view, "phase", raw.get("phase", "")) or "")
        legal = [str(a) for a in (getattr(view, "legal_actions", None) or raw.get("legal_actions") or [])]
        state = getattr(view, "state", None) or raw.get("state") or {}

        lines = [f"PHASE `{phase}`. You are seat {seat}."]
        lines.append(self._players_table(state, seat))
        holdings = self._holdings(state, seat)
        if holdings:
            lines.append("")
            lines.append(holdings)
        pending = self._pending(state)
        if pending:
            lines.append("")
            lines.append(pending)

        if memory:
            lines.append("")
            lines.append("Your notes from earlier in this match:")
            lines.append(memory)

        lines.append("")
        lines.append(f"Legal actions RIGHT NOW: {legal}")
        lines.append("Call `take_action` with one of exactly these verbs.")
        return "\n".join(lines)

    def _players_table(self, state: dict[str, Any], seat: int) -> str:
        players = state.get("players")
        if not isinstance(players, list) or not players:
            return "Player state unavailable."
        rows = ["Players (cash | position | jail | bankrupt):"]
        for i, p in enumerate(players):
            if not isinstance(p, dict):
                continue
            tag = " ← you" if i == seat else ""
            rows.append(
                f"  seat {i}: ${p.get('cash','?')} | sq {p.get('position','?')}"
                f" | {'JAILED' if p.get('in_jail') or p.get('jail') else 'free'}"
                f" | {'BANKRUPT' if p.get('bankrupt') else 'solvent'}{tag}"
            )
        return "\n".join(rows)

    def _holdings(self, state: dict[str, Any], seat: int) -> str:
        """Only OUR squares, plus a count of everyone else's.

        The full holdings map is 40 squares of nested detail on every single decision. Sent
        whole it would dominate the prompt, push past the cached prefix and re-bill the
        rules each turn. Ours in full and theirs as a count keeps the decision-relevant
        part and drops the bulk.
        """
        holdings = state.get("holdings")
        if not isinstance(holdings, (list, dict)):
            return ""
        items = holdings.items() if isinstance(holdings, dict) else enumerate(holdings)
        mine: list[str] = []
        others: dict[int, int] = {}
        for sq, h in items:
            if not isinstance(h, dict):
                continue
            owner = h.get("owner")
            if owner is None or (isinstance(owner, int) and owner < 0):
                continue
            try:
                owner_i = int(owner)
            except (TypeError, ValueError):
                continue
            if owner_i == seat:
                bits = [f"sq {sq}"]
                if h.get("houses"):
                    bits.append(f"{h['houses']} houses")
                if h.get("mortgaged"):
                    bits.append("MORTGAGED")
                mine.append(" ".join(bits))
            else:
                others[owner_i] = others.get(owner_i, 0) + 1
        out = []
        if mine:
            out.append("Your properties: " + "; ".join(mine))
        else:
            out.append("You own no properties yet.")
        if others:
            out.append("Opponent holdings: " + ", ".join(f"seat {k}: {v} squares" for k, v in sorted(others.items())))
        return "\n".join(out)

    def _pending(self, state: dict[str, Any]) -> str:
        out = []
        auction = state.get("auction") or state.get("pending_auction")
        if isinstance(auction, dict) and auction:
            out.append(
                f"Open auction: square {auction.get('property','?')}, "
                f"high bid ${auction.get('high_bid', auction.get('bid', 0))} "
                f"by seat {auction.get('high_bidder', auction.get('bidder','?'))}"
            )
        trade = state.get("trade") or state.get("pending_trade")
        if isinstance(trade, dict) and trade:
            out.append(f"Pending trade: {trade}")
        return "\n".join(out)

    # --- legality -------------------------------------------------------------------

    def _legal(self, view: Any) -> list[str]:
        raw = getattr(view, "raw", {}) or {}
        return [str(a) for a in (getattr(view, "legal_actions", None) or raw.get("legal_actions") or [])]

    def fallback(self, view: Any) -> dict[str, Any]:
        """The safest legal action that keeps the match moving.

        Preference order is chosen to be inert rather than good: end the turn, decline to
        spend, pass an auction. The one exception is `roll`, which is not a strategic
        choice at all — a phase that only permits rolling has no decision in it, and
        refusing to roll would wedge the match.
        """
        legal = self._legal(view)
        for a in ("roll", "end_turn", "skip_trade", "pass", "decline", "reject_trade"):
            if a in legal:
                return {"action": a, "property": 0, "amount": 0}
        return {"action": legal[0] if legal else "end_turn", "property": 0, "amount": 0}

    def coerce(self, args: dict[str, Any], view: Any) -> tuple[dict[str, Any], str]:
        legal = self._legal(view)
        kind = str(args.get("kind", "") or "").strip().lower()
        if kind not in legal:
            return self.fallback(view), "repair"

        def as_int(key: str) -> int:
            try:
                return int(args.get(key) or 0)
            except (TypeError, ValueError):
                return 0

        move: dict[str, Any] = {
            "action": kind,
            "property": as_int("property"),
            "amount": as_int("amount"),
        }
        # The trade payload is passed through untouched when present. It is deliberately
        # NOT part of the canonical bound form — a nested structure re-rendered cosmetically
        # differently would reject an honest turn — so nothing here can cost the turn its
        # binding, and a malformed trade is refused by the engine rather than by us.
        trade = args.get("trade")
        if kind in ("propose_trade", "counter_trade") and isinstance(trade, dict):
            move["trade"] = trade
        return move, "model"
