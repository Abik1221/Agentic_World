#!/usr/bin/env python3
"""What $10 buys, per game and per model pairing.

    python3 plan.py --budget 10

Projects the cost of a run BEFORE it happens, from the same price table the budget guard
uses, so the run plan is arithmetic rather than optimism. Every number here is a projection
and every one of them is wrong in the same direction: token estimates come from measured
prompt sizes plus a reasoning allowance, and a model that thinks harder than the allowance
costs more. Treat the output as a floor.

# The two facts that dominate the arithmetic

Reasoning tokens bill at the OUTPUT rate. That is the most expensive number on the invoice
and it is the one the `reasoning` effort setting moves, which is why medium rather than high
is the default and why raising it should be a deliberate decision with this script re-run.

Prompt caching turns the system prompt from a per-decision cost into a per-match one. The
rules block is ~1100 tokens for Goofspiel and more for the others; without caching a 13-round
match pays for it 13 times. The `cached` column below shows the difference, and it is the
reason a match is a much better unit to buy than a decision.
"""

from __future__ import annotations

import argparse

from pyyolbench.models import PILOT, RANKED, Model

# Measured from the rendered prompts in this repo, not guessed: the system prompt for each
# game plus a representative user turn at mid-match, tokenised at the usual ~4 chars/token.
# Re-measure with `python3 plan.py --measure` after editing a system prompt.
GAME_SHAPE = {
    #                 system   user    out    decisions/seat/match
    "goofspiel": {"system": 1150, "user": 700, "out": 700, "decisions": 13},
    # Mafia's user turn carries a growing public transcript; 1800 is a mid-match average
    # across the window the policy sends. Decisions are phases a seat actually acts in.
    "mafia": {"system": 1450, "user": 1800, "out": 700, "decisions": 22},
    # Monopoly's decision count is the killer, not its prompt. A match that runs to the turn
    # cap is hundreds of turns times several phases; 400 per seat is a MODERATE match and a
    # long one is several times that.
    "monopoly": {"system": 1350, "user": 1100, "out": 600, "decisions": 400},
}


def per_match_cost(m: Model, game: str, *, cached: bool = True) -> float:
    """Cost for ONE SEAT to play one match.

    With caching on, the system prompt is a fresh write on the first decision and a cached
    read on every one after it. That is exactly how the vendors bill it and it is why the
    write premium (1.25x on Anthropic and OpenAI) is affordable: it is paid once against
    twelve or more reads.
    """
    s = GAME_SHAPE[game]
    n = s["decisions"]
    if not cached:
        return n * m.cost_usd(s["system"] + s["user"], s["out"])
    first = m.cost_usd(s["system"] + s["user"], s["out"], cached_write=s["system"])
    rest = (n - 1) * m.cost_usd(s["system"] + s["user"], s["out"], cached_read=s["system"])
    return first + rest


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--budget", type=float, default=10.0)
    args = ap.parse_args()

    print(f"=== per-seat cost of ONE match (reasoning: medium) ===\n")
    print(f"{'model':<18} {'game':<11} {'uncached':>10} {'cached':>10} {'saving':>8}")
    print("-" * 62)
    for key, m in {**RANKED, **PILOT}.items():
        for game in ("goofspiel", "mafia", "monopoly"):
            u = per_match_cost(m, game, cached=False)
            c = per_match_cost(m, game, cached=True)
            print(f"{key:<18} {game:<11} ${u:>9.4f} ${c:>9.4f} {100*(1-c/u):>7.1f}%")
        print()

    print("=== what the budget buys: Goofspiel, both seats, head to head ===\n")
    print(f"{'pairing':<38} {'$/match':>9} {'matches':>9}")
    print("-" * 58)
    ranked = list(RANKED.items())
    pairs = [(a, b) for i, (a, _) in enumerate(ranked) for b, _ in ranked[i + 1:]]
    full_rr = 0.0
    for a, b in pairs:
        cost = per_match_cost(RANKED[a], "goofspiel") + per_match_cost(RANKED[b], "goofspiel")
        full_rr += cost
        print(f"{a + ' vs ' + b:<38} ${cost:>8.4f} {int(args.budget / cost):>9d}")

    # 30-50 matches per pairing is what it takes for a Bradley-Terry interval to exclude
    # 50%. Below that the board can rank the models and the ranking means nothing.
    target = 30
    need = full_rr * target
    print(f"\n  full round-robin, all {len(pairs)} pairings x {target} matches: ${need:.2f}")
    print(f"  your budget: ${args.budget:.2f}  →  "
          + (f"AFFORDABLE" if need <= args.budget
             else f"SHORT by ${need - args.budget:.2f} ({args.budget / need * 100:.0f}% of a full run)"))

    print("\n=== the same round-robin on pilot models ===\n")
    pilot = list(PILOT.items())
    ppairs = [(a, b) for i, (a, _) in enumerate(pilot) for b, _ in pilot[i + 1:]]
    ptotal = sum(
        per_match_cost(PILOT[a], "goofspiel") + per_match_cost(PILOT[b], "goofspiel")
        for a, b in ppairs
    )
    print(f"  {len(ppairs)} pairings x {target} matches: ${ptotal * target:.2f}")
    print("  This is the run to do FIRST. It exercises every line of code a ranked run does")
    print("  — binding, attribution, coverage, the board fit — for a fraction of the money,")
    print("  and a binding bug found here costs cents instead of the whole budget.")

    print("\n=== Monopoly warning ===\n")
    worst = max(RANKED.values(), key=lambda m: per_match_cost(m, "monopoly"))
    print(f"  One 4-seat Monopoly match on {worst.key} alone: "
          f"${4 * per_match_cost(worst, 'monopoly'):.2f}")
    print("  A match that runs to the turn cap costs several times that. Monopoly is the")
    print("  arena to enter last, with the cheap models, and never as an exploratory run.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
