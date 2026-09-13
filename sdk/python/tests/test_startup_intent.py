"""Join vs Invite for ``pyyol play`` — never hang CI, default Join on timeout."""

from __future__ import annotations

import io
import time

import pytest

from pyyol.console import (
    INTENT_INVITE,
    INTENT_QUEUE,
    ask_intent,
    friends_url,
    resolve_startup_intent,
)


class FakeTTY(io.StringIO):
    def __init__(self, data: str = "", tty: bool = True):
        super().__init__(data)
        self._tty = tty

    def isatty(self) -> bool:
        return self._tty


def test_friends_url_joins_dashboard():
    assert friends_url("https://pyyol.com") == "https://pyyol.com/friends"
    assert friends_url("https://pyyol.com/") == "https://pyyol.com/friends"
    assert friends_url("") == ""


def test_non_tty_returns_queue_without_prompt():
    out = FakeTTY(tty=False)
    assert ask_intent(timeout=5, stream=out, stdin=FakeTTY(tty=False)) == INTENT_QUEUE
    assert out.getvalue() == ""


def test_i_chooses_invite():
    out = FakeTTY(tty=True)
    assert ask_intent(timeout=5, stream=out, stdin=FakeTTY("i\n")) == INTENT_INVITE


@pytest.mark.parametrize("answer", ["j\n", "\n", "anything\n"])
def test_everything_other_than_i_joins(answer):
    out = FakeTTY(tty=True)
    assert ask_intent(timeout=5, stream=out, stdin=FakeTTY(answer)) == INTENT_QUEUE


def test_silence_defaults_to_join():
    class NeverAnswers(FakeTTY):
        def readline(self) -> str:
            time.sleep(30)
            return "i\n"

    out = FakeTTY(tty=True)
    t0 = time.monotonic()
    got = ask_intent(timeout=0.3, stream=out, stdin=NeverAnswers())
    elapsed = time.monotonic() - t0
    assert got == INTENT_QUEUE
    assert 0.2 <= elapsed < 2.0
    assert "joining a game" in out.getvalue()


def test_ranked_and_queue_flag_force_queue():
    assert resolve_startup_intent(ranked=True, invite_flag=True, is_tty=True) == INTENT_QUEUE
    assert resolve_startup_intent(queue_flag=True, invite_flag=True, is_tty=True) == INTENT_QUEUE


def test_invite_flag_and_mode():
    assert resolve_startup_intent(invite_flag=True, is_tty=False) == INTENT_INVITE
    assert resolve_startup_intent(mode="invite", is_tty=False) == INTENT_INVITE
    assert resolve_startup_intent(mode="queue", is_tty=True) == INTENT_QUEUE


def test_env_pyyol_startup():
    assert resolve_startup_intent(env={"PYYOL_STARTUP": "invite"}, is_tty=False) == INTENT_INVITE
    assert resolve_startup_intent(env={"PYYOL_STARTUP": "queue"}, is_tty=True) == INTENT_QUEUE


def test_non_tty_skips_ask():
    calls = []

    def boom(**_kwargs):
        calls.append(1)
        return INTENT_INVITE

    assert resolve_startup_intent(is_tty=False, ask=boom) == INTENT_QUEUE
    assert calls == []


def test_tty_asks():
    assert resolve_startup_intent(is_tty=True, ask=lambda **_: INTENT_INVITE) == INTENT_INVITE
