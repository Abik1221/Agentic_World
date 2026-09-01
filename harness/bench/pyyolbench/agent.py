"""The Pyyol agent: one process, one model, one game, one seat.

# Why one model per process

Every knob that could differ between two models is a confound, and the cheapest way to
guarantee none of them differ is to make the model the only thing a process knows about
itself. A single process juggling five models would share a client, a memory buffer and a
budget accounting path between them, and any bug in that sharing would look like a strength
difference between models. Five processes share nothing but the budget file, which is
explicitly designed to be shared.

It also makes the run trivially parallel and trivially attributable: a container is named for
its model, its game and its seat, and so is its ledger file.
"""

from __future__ import annotations

import logging
from typing import Any

from pyyol import Adapter

from .brain import Brain

log = logging.getLogger("pyyolbench.agent")


class BenchAgent(Adapter):
    """Adapter wrapper around a `Brain`.

    Thin on purpose. Everything interesting is in the brain and the policy; this exists to
    satisfy the SDK's interface and to make sure per-match state is keyed on the match id
    rather than built at connect time — the SDK is explicit that `initialize` is neither
    guaranteed nor once-per-match, and that state built there leaks across matches.
    """

    def __init__(self, *, brain: Brain, name: str, secret: str = "") -> None:
        self.brain = brain
        self.name = name
        self.supported_games = [brain.policy.game]
        self.secret = secret

    def step(self, view: Any) -> Any:
        return self.brain.decide(view)

    def shutdown(self, result: Any) -> None:
        """Drop this match's notes.

        Called on `/game-end`, which the SDK warns is not guaranteed — a dropped
        connection ends a match without it. That is acceptable here: the memory buffer is
        bounded per match, so the worst case of a missed hook is a few kilobytes held
        until the process exits, not unbounded growth.
        """
        match_id = str(getattr(result, "match_id", "") or "")
        if match_id:
            self.brain.ledger.match_end(
                match_id=match_id,
                game=self.brain.policy.game,
                seat=-1,
                result=getattr(result, "result", None),
            )
            self.brain.on_match_end(match_id)
