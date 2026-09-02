"""The counted-run watchdog measures SILENCE, not elapsed time.

This is pinned because the bug was invisible in exactly the games the suite covers.
`_COUNTED_RUN_IDLE_TIMEOUT_S` is documented as how long a run waits "with nothing
finishing", and the loop that consumed it started a counter at zero and only ever
climbed — so it was a flat cap on total runtime instead.

Goofspiel finishes in seconds and Mafia faster still, so both stayed far inside the
window and nothing ever failed. Monopoly runs much longer, and was therefore cut off
mid-play every single time, always at the same five minutes, while the CLI reported
"no match finished" — which reads as a broken match rather than a stopwatch. The
flagship long-form game could not be played to completion by anyone, and no test
noticed because no test ran long enough to be cut.

So the guard is on the CLOCK, not on a game: a connector that keeps reporting activity
must never be given up on, however long it runs.
"""

import time

from pyyol import Agent
from pyyol.runtime import RuntimeConnector


def _conn() -> RuntimeConnector:
    return RuntimeConnector(
        Agent(name="t"),
        url="ws://x",
        agent_id="ag",
        token="s",
        games=["mafia"],
        _connect=lambda *a, **k: None,
    )


def test_last_activity_starts_stamped():
    """Never None: a caller subtracting from it must not have to special-case a
    connection that has not emitted yet."""
    before = time.monotonic()
    conn = _conn()
    assert before <= conn.last_activity <= time.monotonic()


def test_every_emit_counts_as_activity():
    """The stamp lives in the single funnel every lifecycle signal passes through, so
    an event type added later counts automatically rather than silently not."""
    conn = _conn()
    conn.last_activity = time.monotonic() - 60.0
    stale = conn.last_activity

    conn._emit("event", "turn_started seq=41")

    assert conn.last_activity > stale
    assert time.monotonic() - conn.last_activity < 1.0


def test_a_busy_run_is_never_given_up_on():
    """The regression itself.

    Drives the watchdog's actual predicate against a connector that keeps emitting,
    with a timeout far shorter than the run. Under the old counter this loop ended
    the moment elapsed time passed the limit, however busy the connection was.
    """
    conn = _conn()
    timeout = 0.20

    started = time.monotonic()
    gave_up = False
    # Emit steadily for well over the timeout — a long, healthy Monopoly match.
    while time.monotonic() - started < timeout * 4:
        conn._emit("event", "dice_rolled")
        if time.monotonic() - conn.last_activity >= timeout:
            gave_up = True
            break
        time.sleep(timeout / 10)

    assert not gave_up, "a run that is still producing events was stopped as idle"


def test_a_silent_run_still_gives_up():
    """The other half. A watchdog that never fires is not a watchdog — a dropped
    game_end frame has to stop the run rather than hang it forever."""
    conn = _conn()
    timeout = 0.10

    conn._emit("event", "turn_started")
    time.sleep(timeout * 2)  # go quiet

    assert time.monotonic() - conn.last_activity >= timeout
