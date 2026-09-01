"""Per-match memory: what the agent carries between its own decisions.

# Why memory is built locally instead of by a second model call

The obvious "memory management" is to have the model summarise the match into notes each
turn. That doubles the call count, and call count is the entire cost story of this benchmark
— it would turn a $10 pilot into a $20 one and change what is being measured, because half
the spend would go to summarising rather than playing.

It would also break the comparison in a subtler way. A summarising call is itself a model
call, so a model that writes good notes would be scored partly on its note-taking. The claim
we want to support is "this model plays the game better", and the cleanest way to keep it is
for every model to be handed the SAME notes, mechanically derived, and judged only on the
move it makes from them.

So memory here is bookkeeping, not cognition: what I did, what happened, and what I said I
was doing. Compact, deterministic, identical in structure for every model.

# Why it is bounded, and bounded at the tail

Two reasons, and the second is the one that costs money. A Mafia match runs many phases and a
Monopoly match runs hundreds of turns; unbounded notes would eventually exceed the context
window. Long before that, they break prompt caching — the cached prefix is the system prompt,
and everything after it is fresh input on every call. Notes that grow without limit mean the
per-turn fresh-token count grows without limit, so the cost per decision rises through the
match. A fixed window keeps the marginal decision a fixed price.

The window keeps the most RECENT entries because Goofspiel and Monopoly are Markov-ish — the
current position already encodes most of the past — while the one genuinely historical thing
that matters (who said what in Mafia) is already replayed in full by the platform's own
`public` transcript, which the policy renders separately.
"""

from __future__ import annotations

import threading
from collections import deque
from dataclasses import dataclass


@dataclass
class Note:
    turn: int
    #: One short line the agent wrote about its own intent, taken from the model's prose
    #: alongside the tool call. Empty when the model answered with a bare tool call.
    intent: str
    #: The canonical move actually submitted, so the notes and the match agree.
    move: str


class MatchMemory:
    """A bounded, per-match note buffer.

    Keyed by match id and created lazily, because the SDK is explicit that `initialize` is
    not guaranteed and one connection serves many matches: state built at connect time and
    reused leaks match one's memory into match two, which reads as a strategy bug and is
    not one.
    """

    def __init__(self, window: int = 8) -> None:
        self._window = window
        self._by_match: dict[str, deque[Note]] = {}
        # The SDK's HTTP server is threaded, so two seats served by one process can touch
        # this concurrently. A plain dict mutation is not atomic across the get-or-create.
        self._lock = threading.Lock()

    def add(self, match_id: str, note: Note) -> None:
        with self._lock:
            buf = self._by_match.get(match_id)
            if buf is None:
                buf = deque(maxlen=self._window)
                self._by_match[match_id] = buf
            buf.append(note)

    def render(self, match_id: str) -> str:
        """The notes block for the user turn, or "" when there is nothing to say."""
        with self._lock:
            buf = self._by_match.get(match_id)
            notes = list(buf) if buf else []
        if not notes:
            return ""
        lines = []
        for n in notes:
            if n.intent:
                lines.append(f"  turn {n.turn}: played {n.move} — {n.intent}")
            else:
                lines.append(f"  turn {n.turn}: played {n.move}")
        return "\n".join(lines)

    def forget(self, match_id: str) -> None:
        """Drop a finished match.

        Called from the game-end hook. Without it a long-lived process accumulates one
        buffer per match forever — a slow leak that only shows up on the batch runs, which
        are exactly the runs that matter here.
        """
        with self._lock:
            self._by_match.pop(match_id, None)
