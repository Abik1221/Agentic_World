"""`pyyol run` offers the watch choice without ever blocking the match.

This is the path a developer is actually on: they typed a command, a staked match
appeared, and until now it simply began. The prompt has to fit into that WITHOUT
delaying the acknowledgement or the first turn — a courtesy that costs a stake is not
a courtesy.
"""

from __future__ import annotations

import threading
import time

from pyyol.runtime import RuntimeConnector


def _connector() -> RuntimeConnector:
    return RuntimeConnector(agent=object(), url="wss://x", agent_id="a_1", token="t", name="n")


def test_the_dispatch_thread_is_not_blocked_by_the_prompt(monkeypatch):
    """The load-bearing test.

    _offer_watch is called from the frame-dispatch loop. If it waited for a keystroke
    there, every frame behind it would stall — including the heartbeat that keeps the
    connection alive and the first turn of the match. A developer who stepped away for
    coffee would come back to a forfeited stake.
    """
    started = threading.Event()

    def slow_ask(*a, **k):
        started.set()
        time.sleep(30)  # a developer who walked away
        return "terminal"

    monkeypatch.setattr("pyyol.console.ask_watch", slow_ask)

    c = _connector()
    t0 = time.monotonic()
    c._offer_watch({"match_id": "m_1", "game": "goofspiel"})
    elapsed = time.monotonic() - t0

    assert elapsed < 1.0, f"_offer_watch blocked the dispatch loop for {elapsed:.1f}s"
    assert started.wait(3), "the prompt never ran at all"


def test_offered_once_per_connection(monkeypatch):
    """A long run plays many matches; asking before each one is what you dread.

    Also correctness, not only taste: a timed-out prompt leaves a reader parked on
    stdin, so a second ask would find its answer swallowed by the first.
    """
    asks = []
    monkeypatch.setattr("pyyol.console.ask_watch", lambda label, url, **k: asks.append(label) or "terminal")

    c = _connector()
    for i in range(5):
        c._offer_watch({"match_id": f"m_{i}", "game": "goofspiel"})
    _settle()
    assert len(asks) <= 1, f"asked {len(asks)} times on one connection"


def test_env_override_skips_the_prompt_entirely(monkeypatch):
    """PYYOL_WATCH=terminal must make a headless run incapable of reading stdin.

    The timeout alone is not enough: a supervised process that pauses ten seconds per
    match is still wrong, just quieter about it.
    """
    def boom(*a, **k):
        raise AssertionError("stdin was read despite PYYOL_WATCH")

    monkeypatch.setattr("pyyol.console.ask_watch", boom)
    monkeypatch.setenv("PYYOL_WATCH", "terminal")
    opened = []
    monkeypatch.setattr("webbrowser.open", lambda u: opened.append(u))

    c = _connector()
    c._offer_watch({"match_id": "m_1", "game": "goofspiel"})
    _settle()
    assert opened == []


def test_env_browser_opens_without_asking(monkeypatch):
    def boom(*a, **k):
        raise AssertionError("stdin was read despite PYYOL_WATCH=browser")

    monkeypatch.setattr("pyyol.console.ask_watch", boom)
    monkeypatch.setenv("PYYOL_WATCH", "browser")
    opened = []
    monkeypatch.setattr("webbrowser.open", lambda u: opened.append(u))

    c = _connector()
    c._offer_watch({"match_id": "m_9", "game": "goofspiel"})
    _settle()
    assert len(opened) == 1 and "m_9" in opened[0]


def test_a_game_with_no_viewer_route_is_silent(monkeypatch):
    """No link rather than a wrong one — a tab on someone else's match is worse."""
    asked = []
    monkeypatch.setattr("pyyol.console.ask_watch", lambda label, url, **k: asked.append(url) or "terminal")

    c = _connector()
    c._offer_watch({"match_id": "m_1", "game": "not-a-game"})
    _settle()
    assert asked == []


def test_a_broken_prompt_never_reaches_the_run_loop(monkeypatch):
    """Watching is a courtesy; the match is not. Nothing here may raise into the loop."""
    monkeypatch.setattr(
        "pyyol.console.ask_watch",
        lambda *a, **k: (_ for _ in ()).throw(RuntimeError("terminal exploded")),
    )
    c = _connector()
    c._offer_watch({"match_id": "m_1", "game": "goofspiel"})  # must not raise
    _settle()


def _settle(seconds: float = 2.0) -> None:
    """Wait for the daemon offer thread to finish its work."""
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        if not any(t.name == "pyyol-watch" for t in threading.enumerate()):
            return
        time.sleep(0.02)
