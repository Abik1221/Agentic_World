"""A hard spend ceiling shared by every agent process in a benchmark run.

# Why this is a file and not a counter in the agent

The run is many containers: five models times three games, each an independent process,
each holding the same API key. A per-process budget would multiply by the number of
processes, which is exactly the failure that turns a $10 experiment into a $150 one. The
ceiling has to be enforced on something all of them can see, and the only thing they share
is the mounted state directory.

So: one JSON file, one `flock`, read-modify-write under the lock. Slow and boring, which is
what a money control should be. At the call rates here (a few per second across the whole
run) the lock is never contended enough to matter.

# Reserve-then-settle, not spend-after

A call's true cost is only known from the response, which arrives after the money is spent.
Charging afterwards means the ceiling is discovered one call too late — and with N processes
in flight, N calls too late. So a caller RESERVES a projected worst-case cost before the
request and SETTLES the observed cost after it. The reservation is what the ceiling is
checked against, so the worst case is that the run stops slightly early, never that it
overruns. A crash between reserve and settle leaves the reservation standing, which is the
safe direction: the run under-spends rather than over-spends.

# Why the ceiling refuses rather than warns

A warning in a container log at 3am is not a control. `Budget.reserve` raises
`BudgetExhausted`, the brain catches it, and the agent falls back to its heuristic move and
keeps playing — the match completes and its data is kept, it is simply no longer a model
measurement from that point. That is strictly better than a half-finished match: an
abandoned match yields an unfinished row that the board excludes anyway, so the money
already spent on its earlier rounds is wasted.
"""

from __future__ import annotations

import json
import os
import time
from contextlib import contextmanager
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Iterator

# Not imported at module scope on Windows-ish platforms; the harness is Linux/macOS only and
# a missing fcntl should be a loud import error rather than a silently unlocked budget.
import fcntl


class BudgetExhausted(RuntimeError):
    """Raised by `reserve` when the projected cost would breach the ceiling."""


@dataclass
class BudgetState:
    limit_usd: float
    reserved_usd: float = 0.0
    settled_usd: float = 0.0
    calls: int = 0
    #: Per-model settled spend, so the run report can say where the money went without
    #: re-reading the whole ledger.
    by_model: dict[str, float] = field(default_factory=dict)

    @property
    def committed_usd(self) -> float:
        """What must be treated as spent: settled cost plus outstanding reservations."""
        return self.settled_usd + self.reserved_usd

    @property
    def remaining_usd(self) -> float:
        return max(0.0, self.limit_usd - self.committed_usd)


class Budget:
    """A ceiling shared across processes through one lock file."""

    def __init__(self, path: str | os.PathLike[str], limit_usd: float) -> None:
        self.path = Path(path)
        self.path.parent.mkdir(parents=True, exist_ok=True)
        self.limit_usd = float(limit_usd)
        # Create the file if absent, but never truncate an existing one: a second container
        # starting up must join the existing budget, not reset it. This is the difference
        # between a shared ceiling and five independent ones.
        if not self.path.exists():
            with self._locked() as fh:
                if fh.read().strip() == "":
                    self._write(fh, BudgetState(limit_usd=self.limit_usd))

    @contextmanager
    def _locked(self) -> Iterator[Any]:
        # "a+" so the file is created if missing and never truncated if present.
        with open(self.path, "a+", encoding="utf-8") as fh:
            fcntl.flock(fh.fileno(), fcntl.LOCK_EX)
            try:
                fh.seek(0)
                yield fh
            finally:
                fh.flush()
                os.fsync(fh.fileno())
                fcntl.flock(fh.fileno(), fcntl.LOCK_UN)

    def _read(self, fh: Any) -> BudgetState:
        fh.seek(0)
        raw = fh.read().strip()
        if not raw:
            return BudgetState(limit_usd=self.limit_usd)
        try:
            d = json.loads(raw)
        except json.JSONDecodeError:
            # A corrupt state file must not be silently reset to zero spend — that would
            # hand the run a fresh $10 it has already spent. Treat the ceiling as reached
            # and make an operator look.
            raise BudgetExhausted(
                f"budget state at {self.path} is corrupt; refusing to spend. "
                "Inspect it and reset deliberately if the run should continue."
            ) from None
        st = BudgetState(
            limit_usd=float(d.get("limit_usd", self.limit_usd)),
            reserved_usd=float(d.get("reserved_usd", 0.0)),
            settled_usd=float(d.get("settled_usd", 0.0)),
            calls=int(d.get("calls", 0)),
            by_model=dict(d.get("by_model", {})),
        )
        # The configured limit wins over the stored one, so raising the ceiling is a
        # restart rather than a file edit. Lowering it below what is already spent is
        # allowed and simply stops the run, which is the intent of lowering it.
        st.limit_usd = self.limit_usd
        return st

    def _write(self, fh: Any, st: BudgetState) -> None:
        fh.seek(0)
        fh.truncate()
        json.dump(
            {
                "limit_usd": st.limit_usd,
                "reserved_usd": round(st.reserved_usd, 8),
                "settled_usd": round(st.settled_usd, 8),
                "calls": st.calls,
                "by_model": {k: round(v, 8) for k, v in sorted(st.by_model.items())},
                "updated_at": time.time(),
            },
            fh,
        )

    def snapshot(self) -> BudgetState:
        with self._locked() as fh:
            return self._read(fh)

    def reserve(self, projected_usd: float, *, model: str) -> None:
        """Claim headroom for a call, or raise `BudgetExhausted`.

        Note the ceiling is checked against `committed_usd`, which includes other
        processes' outstanding reservations. That is what makes five concurrent containers
        share one $10 rather than each spending it.
        """
        with self._locked() as fh:
            st = self._read(fh)
            if st.committed_usd + projected_usd > st.limit_usd:
                raise BudgetExhausted(
                    f"budget ceiling ${st.limit_usd:.2f} reached "
                    f"(settled ${st.settled_usd:.4f}, reserved ${st.reserved_usd:.4f}); "
                    f"call for {model} projected at ${projected_usd:.4f} refused"
                )
            st.reserved_usd += projected_usd
            self._write(fh, st)

    def settle(self, projected_usd: float, actual_usd: float, *, model: str) -> None:
        """Release a reservation and record what the call actually cost.

        Always call this for a reservation that was granted, including when the request
        FAILED — a 429 costs nothing but its reservation is still outstanding, and leaking
        reservations would strangle the run long before the money ran out. Pass
        `actual_usd=0.0` in that case.
        """
        with self._locked() as fh:
            st = self._read(fh)
            st.reserved_usd = max(0.0, st.reserved_usd - projected_usd)
            st.settled_usd += actual_usd
            st.calls += 1
            if actual_usd:
                st.by_model[model] = st.by_model.get(model, 0.0) + actual_usd
            self._write(fh, st)

    @contextmanager
    def spend(self, projected_usd: float, *, model: str) -> Iterator[list[float]]:
        """Reserve, run the body, settle whatever the body reports.

        The body appends its observed cost to the yielded single-element list. A list
        rather than a return value because a context manager cannot receive one, and a
        mutable box keeps the settle path on the `finally` where a raised exception still
        releases the reservation.
        """
        self.reserve(projected_usd, model=model)
        box: list[float] = []
        try:
            yield box
        finally:
            self.settle(projected_usd, box[0] if box else 0.0, model=model)
