"""Keepalive must outlast a decision.

The handler runs inline on the receive loop, so while a model is thinking nothing on
that socket is serviced. A keepalive tighter than the move window makes a healthy
agent tear down its own connection mid-match — which showed up as repeated
"no close frame received or sent" reconnects and matches played entirely by fallback.
"""

import inspect

from pyyol import runtime

# The longest per-decision budget the platform allows (Monopoly).
LONGEST_MOVE_WINDOW_S = 60


def _connect_kwargs() -> dict:
    """Pull the keepalive settings out of the real connect call rather than
    duplicating them here — a test that restates the numbers cannot catch them
    drifting apart from the code."""
    src = inspect.getsource(runtime)
    line = next(ln for ln in src.splitlines() if "_ws_connect(self.url" in ln)
    out = {}
    for key in ("ping_interval", "ping_timeout", "open_timeout"):
        if f"{key}=" in line:
            out[key] = int(line.split(f"{key}=")[1].split(",")[0].split(")")[0])
    return out


def test_ping_timeout_outlasts_the_longest_decision():
    kw = _connect_kwargs()
    assert kw["ping_timeout"] > LONGEST_MOVE_WINDOW_S, (
        f"ping_timeout={kw['ping_timeout']}s would fire during a legitimate "
        f"{LONGEST_MOVE_WINDOW_S}s decision and drop the match"
    )


def test_keepalive_still_detects_a_dead_link_promptly():
    """Generous is not infinite — a genuinely dropped TCP link must still be noticed."""
    kw = _connect_kwargs()
    assert kw["ping_timeout"] <= 120, "keepalive so long that a dead socket lingers"
    assert kw["ping_interval"] < kw["ping_timeout"]
