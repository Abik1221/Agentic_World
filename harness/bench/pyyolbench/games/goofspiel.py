"""Goofspiel: the cheapest arena to measure a model in, and the least forgiving.

# Why this game carries the benchmark

Goofspiel is 13 decisions per seat with no hidden state except the opponent's simultaneous
bid, and — the property that makes it a good instrument — the opponent's HAND IS DEDUCIBLE.
Both seats start with an identical 1..13, and history records every card both players have
spent. So a strong player always knows exactly which cards the opponent still holds, and a
weak one reasons as though the opponent's hand were unknown. That is a sharp, cheap
discriminator between models, and it is why the system prompt states the deduction explicitly
rather than hoping the model finds it: we are measuring whether a model can PLAY given the
rules, not whether it can rediscover them under time pressure.

# The one rule that decides most losses

Bid against `prize_pool`, never `current_prize`. Under the default `carry` tie rule a tied
round puts its pool on the next round, so the two diverge exactly when the stakes are
highest. A model that bids off `current_prize` systematically underbids the biggest pots in
the game. It is in the rules doc, it is in the view, and models still get it wrong — which
makes it worth a sentence of its own.
"""

from __future__ import annotations

from typing import Any

GAME = "goofspiel"

# The full deck each seat is dealt. Stated as a constant because the opponent-hand deduction
# below is arithmetic against it, and a hard-coded 13 elsewhere would silently break a match
# configured with a different card count.
FULL_HAND = tuple(range(1, 14))

_SYSTEM = """\
You are an expert Goofspiel player competing in the Pyyol arena against another AI agent. \
You play to win the match, not to look reasonable.

## The rules, exactly

Two players. Each holds an IDENTICAL hand of the cards 1..13. Each round one prize card is \
revealed and both players SIMULTANEOUSLY and secretly bid one card from hand. The higher bid \
takes the round's entire pool. Both bid cards are then discarded from both hands, whether or \
not they won. After all rounds the higher total of prize points wins; equal totals draw.

Tie rule (default `carry`): if both players bid the SAME card, nobody scores and the pool \
carries into the next round, stacking on top of the next prize. A run of ties therefore \
creates one very large pot. If the match ends with a pool still carrying, nobody wins it. \
Two other tie rules exist and the state will name which is active: `split` gives each seat \
half of a tied pool with an odd point carried forward, and `discard` throws a tied pool away \
entirely.

Bids are one-shot and simultaneous. You never see the opponent's bid before committing.

## How to play well

1. **Bid against the POOL, not the prize card.** `prize_pool` is what is actually at stake \
this round and includes anything carried from tied rounds. `current_prize` is only the newly \
revealed card. When a pool has carried, these differ and the pool is the number that matters. \
Underbidding a stacked pot is the single most expensive mistake in this game.

2. **You always know the opponent's exact hand — deduce it.** Both seats started with 1..13 \
and the history lists every card each of you has already spent. The opponent holds exactly \
the cards not yet in their spent list. Never reason as if their hand were unknown; it is \
fully determined and it is given to you. Use it: if their highest remaining card is a 6, \
anything above a 6 wins for certain and anything above a 7 is waste.

3. **Spend in proportion, then adjust.** As a baseline, match your card rank to the pool's \
rank among the prizes that remain. Then adjust for what the deduction tells you: bid the \
minimum card that beats what they can plausibly commit, not the biggest card you hold.

4. **Total points win, not rounds.** Winning seven cheap rounds loses to winning the three \
expensive ones. Concede a low pool with your lowest card rather than contest it — a 1 spent \
on a 2-point pool is a card correctly thrown away.

5. **Count the endgame.** Once few cards remain the game is fully determined and can be \
solved rather than estimated. Do that arithmetic: with two cards each and two prizes left, \
work out both orderings and pick the one that wins.

6. **Track the score, and change gear when behind.** If you are behind with few rounds left, \
a safe proportional bid loses slowly. Take the variance: contest the pools that would close \
the gap and concede the ones that would not, even if that means overpaying.

7. **Under `carry`, forcing a tie is a real weapon.** A tie costs you the card but denies the \
opponent the pool and stacks it into a round you may be better placed to win. Under `discard` \
the same move destroys the value instead, so the active tie rule changes whether this is a \
tactic or a blunder.

## Your output

Decide one card and report it by calling the `play_card` tool. Do not answer in prose. \
The card must be one you actually still hold. Reason first if it helps, but the tool call \
is the move that counts."""


class GoofspielPolicy:
    game = GAME

    def system_prompt(self) -> str:
        return _SYSTEM

    # --- state rendering ------------------------------------------------------------

    def render_state(self, view: Any, memory: str) -> str:
        """The user turn.

        Deliberately a compact, labelled block rather than the raw view JSON. The raw view
        is what `movetools.prompt_for` sends and it is the honest minimum, but it makes the
        model do three pieces of bookkeeping — deducing the opponent's hand, separating its
        own score from the seat-indexed array, and reading the pool out from under the
        prize — before it can think about the game. Doing that arithmetic here is not
        giving the model the answer; it is removing clerical work that measures typing
        rather than play, and it is applied identically to every model so it cannot favour
        one.
        """
        seat = int(getattr(view, "seat", 0))
        raw = getattr(view, "raw", {}) or {}
        hand = sorted(int(c) for c in (getattr(view, "your_hand", []) or []))
        legal = sorted(int(c) for c in (getattr(view, "legal_actions", []) or hand))
        scores = list(getattr(view, "scores", []) or [0, 0])
        history = list(getattr(view, "history", []) or [])
        pool = int(getattr(view, "prize_pool", 0) or 0)
        prize = int(getattr(view, "current_prize", 0) or 0)
        rnd = int(getattr(view, "round", 0) or 0)

        me = scores[seat] if seat < len(scores) else 0
        opp_seat = 1 - seat
        opp = scores[opp_seat] if opp_seat < len(scores) else 0

        opp_hand = self._deduce_opponent_hand(history)
        prizes_left = self._prizes_left(history, prize)

        lines = [
            f"ROUND {rnd}. You are seat {seat}.",
            f"Pool at stake THIS round: {pool}"
            + (f"  (newly revealed prize card: {prize}" f"{'; a tie has carried value in' if pool > prize else ''})"),
            f"Score — you {me}, opponent {opp}"
            + (f"  (you are {'ahead' if me > opp else 'behind' if me < opp else 'level'}"
               f" by {abs(me - opp)})" if me != opp else "  (level)"),
            "",
            f"Your hand ({len(hand)} cards): {hand}",
            f"Opponent's hand, deduced ({len(opp_hand)} cards): {opp_hand}",
        ]
        if opp_hand:
            lines.append(
                f"  → their highest is {max(opp_hand)}; any card above it wins this round outright."
            )
        if prizes_left:
            lines.append(f"Prize cards still to come after this round: {sorted(prizes_left)}")
        tie_rule = self._tie_rule(raw)
        if tie_rule:
            lines.append(f"Tie rule in force: {tie_rule}")
        lines.append("")
        lines.append(self._history_table(history, seat))
        if memory:
            lines.append("")
            lines.append("Your notes from earlier in this match:")
            lines.append(memory)
        lines.append("")
        lines.append(f"Legal cards you may bid: {legal}")
        lines.append("Call `play_card` with the card you bid.")
        return "\n".join(lines)

    def _deduce_opponent_hand(self, history: list[dict[str, Any]]) -> list[int]:
        """Which cards the opponent still holds.

        Both seats start with the identical full hand, so the remainder is the full hand
        minus everything `history` shows them spending. Exact, not an estimate — which is
        why it is computed rather than described.
        """
        spent = set()
        for h in history:
            c = h.get("opp_card")
            if isinstance(c, (int, float)):
                spent.add(int(c))
        return sorted(set(FULL_HAND) - spent)

    def _prizes_left(self, history: list[dict[str, Any]], current: int) -> list[int]:
        """Prize cards not yet revealed.

        The prize deck is the same 1..13, so what remains is the deck minus everything
        already revealed minus the one on the table. Under `shuffled` fairness their ORDER
        is secret, but their identity is not — knowing a 13 is still to come changes how
        freely you spend your own 13.
        """
        seen = {int(h["prize"]) for h in history if isinstance(h.get("prize"), (int, float))}
        seen.add(int(current))
        return sorted(set(FULL_HAND) - seen)

    def _tie_rule(self, raw: dict[str, Any]) -> str:
        for key in ("tie_rule", "tieRule"):
            v = raw.get(key)
            if isinstance(v, str) and v:
                return v
        cfg = raw.get("config")
        if isinstance(cfg, dict):
            v = cfg.get("tie_rule") or cfg.get("tieRule")
            if isinstance(v, str) and v:
                return v
        return ""

    def _history_table(self, history: list[dict[str, Any]], seat: int) -> str:
        if not history:
            return "No rounds resolved yet — this is the opening bid."
        rows = ["Resolved rounds (pool | your bid | their bid | winner):"]
        for h in history[-13:]:
            w = h.get("winner")
            who = "tie" if w is None or int(w) < 0 else ("you" if int(w) == seat else "them")
            rows.append(
                f"  r{h.get('round','?')}: pool {h.get('prize_pool', h.get('prize','?'))}"
                f" | you {h.get('your_card','?')} | them {h.get('opp_card','?')} | {who}"
            )
        return "\n".join(rows)

    # --- legality -------------------------------------------------------------------

    def _legal(self, view: Any) -> list[int]:
        legal = [int(c) for c in (getattr(view, "legal_actions", []) or [])]
        return legal or [int(c) for c in (getattr(view, "your_hand", []) or [])]

    def fallback(self, view: Any) -> dict[str, Any]:
        """Concede the round with the lowest card.

        The same move the engine plays for a seat that never answers, chosen so an
        unreachable model and a silent one produce identical play and the fallback rate
        cannot quietly flatter a model that fails often. Lowest rather than proportional
        because a fallback must never look like strategy: if a heuristic were strong, the
        run would be measuring the heuristic.
        """
        legal = self._legal(view)
        card = min(legal) if legal else 1
        return {"card": card, "round": int(getattr(view, "round", 0) or 0)}

    def coerce(self, args: dict[str, Any], view: Any) -> tuple[dict[str, Any], str]:
        legal = self._legal(view)
        rnd = int(getattr(view, "round", 0) or 0)
        raw = args.get("card")
        try:
            card = int(raw)
        except (TypeError, ValueError):
            return self.fallback(view), "repair"
        if card in legal:
            return {"card": card, "round": rnd}, "model"
        # The model named a card it does not hold. Snap to the nearest legal card rather
        # than to the lowest: the intent ("bid about this high") survives, where a reset to
        # the minimum would turn one arithmetic slip into a thrown round and overstate the
        # gap between a careless model and a weak one.
        if legal:
            nearest = min(legal, key=lambda c: (abs(c - card), c))
            return {"card": nearest, "round": rnd}, "repair"
        return self.fallback(view), "repair"
