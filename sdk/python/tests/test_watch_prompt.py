"""The watch prompt: where a developer chooses to follow a match.

The prompt sits between a developer and a STAKED match that is about to start, so the
failure that matters is not a wrong colour — it is a prompt that blocks. Every test here
pins a way it could hang or mislead.
"""

from __future__ import annotations

import io
import time

import pytest

from pyyol.console import WATCH_BROWSER, WATCH_TERMINAL, ask_watch


class FakeTTY(io.StringIO):
    """A StringIO that claims to be a terminal, so the prompt engages."""

    def __init__(self, data: str = "", tty: bool = True):
        super().__init__(data)
        self._tty = tty

    def isatty(self) -> bool:  # noqa: D102
        return self._tty


def test_no_tty_returns_terminal_without_prompting():
    """CI must never see a prompt. This is the test that keeps pipelines alive."""
    out = FakeTTY(tty=False)
    assert ask_watch("goofspiel · m_x", "http://x", timeout=5, stream=out, stdin=FakeTTY(tty=False)) == WATCH_TERMINAL
    assert out.getvalue() == "", "a non-interactive run must print no prompt at all"


def test_stdout_tty_but_stdin_not_still_skips():
    """`pyyol play | tee log` has a pipe on stdin — nobody can answer it."""
    out = FakeTTY(tty=True)
    got = ask_watch("g · m", "http://x", timeout=5, stream=out, stdin=FakeTTY(tty=False))
    assert got == WATCH_TERMINAL
    assert out.getvalue() == ""


def test_b_chooses_browser():
    out = FakeTTY(tty=True)
    got = ask_watch("g · m", "http://x", timeout=5, stream=out, stdin=FakeTTY("b\n"))
    assert got == WATCH_BROWSER


@pytest.mark.parametrize("answer", ["t\n", "\n", "anything\n"])
def test_everything_other_than_b_follows_here(answer):
    """The default must be the SAFE one: staying put cannot fail, opening a tab can."""
    out = FakeTTY(tty=True)
    assert ask_watch("g · m", "http://x", timeout=5, stream=out, stdin=FakeTTY(answer)) == WATCH_TERMINAL


def test_silence_defaults_within_the_timeout():
    """A developer who walked away must not hold the match open.

    Also a timing assertion: it must return AT the timeout, not after some longer
    internal wait, because the match starts on the server's schedule regardless.
    """

    class NeverAnswers(FakeTTY):
        def readline(self) -> str:
            time.sleep(30)
            return "b\n"

    out = FakeTTY(tty=True)
    t0 = time.monotonic()
    got = ask_watch("g · m", "http://x", timeout=0.3, stream=out, stdin=NeverAnswers())
    elapsed = time.monotonic() - t0

    assert got == WATCH_TERMINAL
    assert elapsed < 3, f"waited {elapsed:.1f}s on a 0.3s timeout — the prompt outlived the countdown"
    assert "no answer" in out.getvalue(), "a silent default reads as a dropped keystroke"


def test_closed_stdin_does_not_raise():
    """A stdin that errors on read is a non-answer, not a crash mid-match."""

    class Broken(FakeTTY):
        def readline(self) -> str:
            raise OSError("stdin is gone")

    got = ask_watch("g · m", "http://x", timeout=0.3, stream=FakeTTY(tty=True), stdin=Broken())
    assert got == WATCH_TERMINAL


def test_box_is_aligned_regardless_of_match_id_length():
    """Every border row must be the same width on screen.

    Padding is computed on the UNCOLORED text: an escape sequence takes columns in a
    Python string and none in a terminal, so measuring the coloured row draws a box
    that is visibly crooked exactly when colour is on.
    """
    out = FakeTTY(tty=True)
    ask_watch("goofspiel · m_tqp7ze5jzmn7xoxu_and_then_some", "http://x",
              timeout=0.2, stream=out, stdin=FakeTTY(tty=True), color=True)
    box = [ln for ln in out.getvalue().splitlines() if ln.startswith(("\033[90m╭", "\033[90m│", "\033[90m╰"))]
    assert len(box) >= 3
    widths = {_visible(ln) for ln in box}
    assert len(widths) == 1, f"border rows disagree on width: {widths}"


def test_uncolored_output_has_no_escape_codes():
    """NO_COLOR / a dumb terminal must get plain text, not literal escape bytes."""
    out = FakeTTY(tty=True)
    ask_watch("g · m", "http://x", timeout=0.2, stream=out, stdin=FakeTTY(tty=True), color=False)
    assert "\033[" not in out.getvalue()


def _visible(s: str) -> int:
    import re

    return len(re.sub(r"\033\[[0-9;]*m", "", s))
