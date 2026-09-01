"""An append-only local record of every decision the benchmark makes.

# Why a local ledger exists when the gateway already records everything

The gateway's `agent_model_calls` row is the AUTHORITATIVE record — it is what the model
board reads and the only thing the published numbers may be computed from. This file does
not compete with it. It exists so that a discrepancy is DETECTABLE.

The failure this is built against already happened once on this platform: every OpenRouter
call in a harness run came back 429 or 401 with zero tokens, and every one of them was
written `bound = true` because binding is decided from the turn proof before the upstream is
called. Nothing on the row said the provider had not answered, so two models were given a
win rate having never emitted a token. The fix went into the attribution query, but the
general lesson is that a single source of truth cannot be checked against itself.

So each process writes what it OBSERVED, locally, before and after each call. Afterwards
`reconcile.py` joins this against `agent_model_calls` and reports three numbers that should
all be zero: calls we made that the database never saw, database rows we have no record of,
and rows where the token counts disagree. Any of them being non-zero means data was lost,
and we would rather know than publish.

# Why JSON Lines, and why fsync

One JSON object per line, appended, flushed and fsynced. Append-only means a crash mid-write
costs at most the final line rather than the file; JSONL means a partially written last line
is discarded by the reader without taking the rest with it. The fsync costs perhaps a
millisecond against model calls that take seconds, and buys the property that a container
killed by the orchestrator still has every completed decision on disk.

Each process writes its OWN file (`{run}/{model}-{game}-{seat}.jsonl`) so no two containers
ever contend for a write and there is no lock on the hot path.
"""

from __future__ import annotations

import json
import os
import threading
import time
from pathlib import Path
from typing import Any


class Ledger:
    """Append-only JSONL sink for one agent process."""

    def __init__(self, path: str | os.PathLike[str]) -> None:
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        # One process may serve several seats on threads (the SDK's server is threaded), so
        # the append itself is guarded. Cheap, uncontended, and it keeps lines whole.
        self._lock = threading.Lock()

    def write(self, kind: str, **fields: Any) -> None:
        """Append one record. Never raises.

        A ledger that could break a turn would be worse than no ledger: the agent's job is
        to play the match, and a full disk must not forfeit it. Failures are swallowed
        here and surface later as a reconciliation gap, which is exactly the signal this
        file exists to produce.
        """
        rec = {"ts": round(time.time(), 6), "kind": kind, **fields}
        try:
            line = json.dumps(rec, separators=(",", ":"), default=str)
        except (TypeError, ValueError):
            line = json.dumps({"ts": rec["ts"], "kind": kind, "error": "unserializable"})
        try:
            with self._lock, open(self.path, "a", encoding="utf-8") as fh:
                fh.write(line + "\n")
                fh.flush()
                os.fsync(fh.fileno())
        except OSError:
            return

    # --- the three events worth recording ------------------------------------------

    def call_start(self, *, match_id: str, turn: int, model: str, game: str, seat: int) -> None:
        """Written BEFORE the request leaves.

        The important one, and the reason the ledger can detect loss at all. A record with
        a `call_start` and no matching `call_end` is a call that left this process and
        never came back — a timeout, a kill, a crash. Those are invisible in a
        write-after-success design, which is precisely the class of loss being guarded
        against.
        """
        self.write(
            "call_start", match_id=match_id, turn=turn, model=model, game=game, seat=seat
        )

    def call_end(
        self,
        *,
        match_id: str,
        turn: int,
        model: str,
        game: str,
        seat: int,
        status: int,
        prompt_tokens: int,
        completion_tokens: int,
        reasoning_tokens: int,
        cached_read: int,
        cost_usd: float,
        latency_ms: int,
        bound_move: str | None,
        submitted_move: str | None,
        error: str = "",
    ) -> None:
        """Written after the response is parsed, success or failure.

        `bound_move` is the canonical form the gateway will bind — computed locally with
        the SDK's own `movetools.bound_move`, so it is byte-identical to what the server
        derives. Recording BOTH it and the move actually submitted is what lets the
        reconciler prove the agent never substituted: if they ever disagree on an honest
        run, either the agent has a bug or the canonicaliser drifted between SDK and
        gateway, and both are things to find here rather than in a rejected match.
        """
        self.write(
            "call_end",
            match_id=match_id,
            turn=turn,
            model=model,
            game=game,
            seat=seat,
            status=status,
            prompt_tokens=prompt_tokens,
            completion_tokens=completion_tokens,
            reasoning_tokens=reasoning_tokens,
            cached_read=cached_read,
            cost_usd=round(cost_usd, 8),
            latency_ms=latency_ms,
            bound_move=bound_move,
            submitted_move=submitted_move,
            error=error,
        )

    def decision(
        self,
        *,
        match_id: str,
        turn: int,
        game: str,
        seat: int,
        source: str,
        move: Any,
    ) -> None:
        """The move actually returned to the platform, and where it came from.

        `source` is one of "model" (the model's bound tool call), "fallback" (the
        heuristic, because the call failed or the budget was exhausted) or "repair" (the
        model named an illegal action and the policy corrected it). The split is the
        headline honesty number of the whole run: a model whose decisions are 40% fallback
        is not being measured, it is being averaged with a heuristic, and the report has to
        be able to say so rather than quietly crediting the model for the heuristic's play.
        """
        self.write(
            "decision", match_id=match_id, turn=turn, game=game, seat=seat, source=source, move=move
        )

    def match_end(self, *, match_id: str, game: str, seat: int, result: Any) -> None:
        self.write("match_end", match_id=match_id, game=game, seat=seat, result=result)
