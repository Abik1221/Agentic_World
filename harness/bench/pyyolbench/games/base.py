"""The contract every game policy implements.

# Why a policy object rather than three agents

The benchmark's causal claim depends on one thing: the harness must be IDENTICAL across
models so that the harness cancels and the difference is the model. The SDK enforces a
version of that mechanically — it fingerprints the system prompt, the tool names and the
sampling parameters with the model excluded, and two agents whose fingerprints differ cannot
be paired.

That makes the split here structural rather than stylistic. Everything that varies by MODEL
(the wire dialect, the cache markers, the credential) lives in the brain. Everything that
varies by GAME (rules, strategy, state rendering, legality) lives in a policy. Nothing varies
by both. So one brain times three policies times five models is fifteen containers running
one harness, and a change to Goofspiel strategy cannot accidentally reach only four of the
five models.

# The system prompt is a constant, deliberately

`system_prompt()` takes no arguments and closes over nothing. If it interpolated the seat,
the match id or the rule set, the fingerprint would churn every match and the agent would be
excluded from paired comparison — the SDK reports exactly that case as unstable. All variable
state goes through `render_state`, into the user turn, where it belongs.
"""

from __future__ import annotations

from typing import Any, Protocol


class GamePolicy(Protocol):
    """Rules, strategy and legality for one game."""

    #: The Pyyol game slug: "goofspiel" | "mafia" | "monopoly".
    game: str

    def system_prompt(self) -> str:
        """The standing instructions. MUST be constant for the life of the process."""
        ...

    def render_state(self, view: Any, memory: str) -> str:
        """The user turn: this decision's state, plus any carried memory.

        `memory` is the brain's rolling summary of the match so far. It is passed in
        rather than built here so the memory policy is one implementation shared by all
        three games, and so a policy cannot accidentally put it in the system prompt.
        """
        ...

    def fallback(self, view: Any) -> dict[str, Any]:
        """A legal move to play when the model cannot be reached.

        Never raises and never returns an illegal action. This is what keeps a match
        finishing when the budget is exhausted or a provider is down: an abandoned match
        is an unfinished row the board discards, which wastes every decision already paid
        for in it. Playing on unbound is honest — the turn is simply unverified.
        """
        ...

    def coerce(self, args: dict[str, Any], view: Any) -> tuple[dict[str, Any], str]:
        """Turn the model's tool arguments into a legal move.

        Returns `(move, source)` where source is "model" if the model's choice was legal
        as given, or "repair" if it had to be corrected. The distinction is recorded per
        decision: a model that needs frequent repair is worse at the game than one that
        does not, and averaging that away would flatter it.

        Repairing rather than rejecting is the right default because an illegal action
        still tells us what the model WANTED, and the alternative — forfeiting the turn —
        would punish the model twice for one mistake and distort the win rate more than
        the repair does.
        """
        ...
