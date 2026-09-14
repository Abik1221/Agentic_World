"""Live terminal rendering for ``pyyol run``.

The connector emits one lifecycle event per milestone (connected, match found,
turn, decision, event, game finished, reconnecting, …). A Console turns those into
what the developer sees: a colored, timestamped feed by default; compact
milestones with ``--quiet``; or one JSON object per line with ``--json`` for
piping into ``jq`` / log shippers.

Nothing here ever prints a token or secret — the connector only passes
non-sensitive fields.
"""

from __future__ import annotations

import json
import os
import queue
import re
import sys
import threading
import time
from collections.abc import Callable
from typing import Any, TextIO

# kind → (symbol, ANSI color). Colors: green ok, amber idle, cyan match, dim
# event, magenta decision, red error.
_STYLE = {
    "connected": ("●", "32"),
    "reconnected": ("●", "32"),
    "waiting": ("◌", "33"),
    "match": ("⇢", "36"),
    "turn": (" ", "0"),
    "decision": ("→", "35"),
    "event": ("·", "90"),
    "game_end": ("★", "32"),
    "reconnecting": ("⚠", "33"),
    "disconnected": ("✕", "31"),
    "error": ("✕", "31"),
    "warn": ("⚠", "33"),
}

# Milestones shown even in --quiet mode.
_QUIET_KINDS = {
    "connected",
    "reconnected",
    "match",
    "game_end",
    "disconnected",
    "error",
    "warn",
    "reconnecting",
}


class Console:
    """Silent base — the library default. ``run`` installs a rendering subclass."""

    def emit(self, kind: str, msg: str, **fields: Any) -> None:  # noqa: D401
        pass

    def banner(self, name: str, url: str) -> None:
        pass


class PrettyConsole(Console):
    """Human-readable colored feed."""

    def __init__(
        self, color: bool | None = None, quiet: bool = False, stream: TextIO | None = None
    ):
        self.stream = stream or sys.stdout
        self.quiet = quiet
        if color is None:
            color = self.stream.isatty() and os.environ.get("NO_COLOR") is None
        self.color = bool(color)

    def _c(self, text: str, code: str) -> str:
        if not self.color or code == "0":
            return text
        return f"\033[{code}m{text}\033[0m"

    def banner(self, name: str, url: str) -> None:
        line = f"pyyol · {name}"
        self.stream.write(self._c(line, "1") + f"   {self._c(url, '90')}\n")
        self.stream.write(self._c("─" * 74, "90") + "\n")
        self.stream.flush()

    def emit(self, kind: str, msg: str, **fields: Any) -> None:
        if self.quiet and kind not in _QUIET_KINDS:
            return
        symbol, code = _STYLE.get(kind, (" ", "0"))
        ts = self._c(time.strftime("%H:%M:%S"), "90")
        sym = self._c(symbol, code)
        label = self._c(f"{kind:<13}", code)
        detail = ""
        if fields:
            parts = [f"{k}={v}" for k, v in fields.items() if v not in (None, "", 0)]
            detail = self._c("  " + " · ".join(parts), "90") if parts else ""
        self.stream.write(f"{ts}  {sym}  {label} {msg}{detail}\n")
        self.stream.flush()


class JsonConsole(Console):
    """One JSON object per line — machine-readable for piping/log shippers."""

    def __init__(self, stream: TextIO | None = None):
        self.stream = stream or sys.stdout

    def banner(self, name: str, url: str) -> None:
        self._write("start", "", name=name, url=url)

    def emit(self, kind: str, msg: str, **fields: Any) -> None:
        self._write(kind, msg, **fields)

    def _write(self, kind: str, msg: str, **fields: Any) -> None:
        rec: dict[str, Any] = {
            "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "event": kind,
        }
        if msg:
            rec["msg"] = msg
        rec.update({k: v for k, v in fields.items() if v is not None})
        self.stream.write(json.dumps(rec) + "\n")
        self.stream.flush()


#: What ``ask_watch`` returns. Deliberately two words rather than a bool — a caller
#: reading ``if watch:`` would have to guess which way round it went.
WATCH_BROWSER, WATCH_TERMINAL = "browser", "terminal"

#: What ``ask_intent`` / ``resolve_startup_intent`` return for ``pyyol play``.
INTENT_QUEUE, INTENT_INVITE = "queue", "invite"

#: Default dashboard, read once. Mirrors cli.DEFAULT_DASHBOARD.
DEFAULT_DASHBOARD = os.environ.get("PYYOL_DASHBOARD", "").rstrip("/") or "https://pyyol.com"

# Where a running match is watched in the browser, per game. Verified against the client's
# routes: Goofspiel and Monopoly take ?match= at the top level, Mafia's viewer lives under
# /arena. A wrong path here is worse than no link — it drops the developer on a DIFFERENT
# live match and everything they see is someone else's game.
#
# Lives here rather than in cli.py because the RUNTIME needs it too, and the runtime is
# library code: reaching into the CLI for a URL would make every agent that starts a match
# import argparse and the whole command surface. One copy, so the two paths cannot drift
# into disagreeing about where a match is watched.
_WATCH_ROUTE = {
    "goofspiel": "/goofspiel",
    "mafia": "/arena/mafia",
}


def watch_url(arena: str, match_id: str, dashboard: str | None = None) -> str:
    """Browser URL for a specific live match, or "" when it cannot be named exactly.

    A link to 'some match' would be a lie dressed as a convenience.

    ``dashboard`` distinguishes NOT SPECIFIED from EXPLICITLY EMPTY, which are different
    facts. None means the caller has no opinion, so the default applies. "" means the
    caller looked and does not know where the dashboard is — and inventing a link to the
    public one there would send a self-hosted developer to a match that is not theirs.
    """
    route = _WATCH_ROUTE.get(arena)
    dashboard = (DEFAULT_DASHBOARD if dashboard is None else dashboard).rstrip("/")
    if not (dashboard and route and match_id):
        return ""
    # safe="" so a slash is escaped too. quote() defaults to safe="/", which would let a
    # match id containing one alter the PATH rather than the query — the link would then
    # point somewhere else entirely.
    import urllib.parse

    return f"{dashboard}{route}?match={urllib.parse.quote(match_id, safe='')}"


def ask_watch(
    label: str,
    url: str,
    timeout: float = 10.0,
    stream: TextIO | None = None,
    color: bool | None = None,
    stdin: TextIO | None = None,
) -> str:
    """Ask where the developer wants to watch this match. Returns a ``WATCH_*``.

    A match used to open a browser tab on its own the moment it started. That is the
    wrong default in both directions: on a remote box or in tmux the tab goes nowhere,
    and a developer who ran a command in a terminal did not necessarily ask to have
    their screen taken over. So we ask, once, and remember.

    # Three rules that keep this from becoming a liability

    **It never blocks a machine.** No TTY on stdin OR stdout means nobody is there to
    answer — CI, a pipe, a systemd unit — so it returns the terminal default without
    printing a prompt at all. A prompt that can hang a pipeline is worse than no prompt.

    **It never outlives the countdown.** The read is bounded by ``timeout`` and defaults
    on expiry. The match begins whether or not this question was answered; a prompt still
    sitting on screen after play has started is asking about a decision that is gone.

    **It never eats the agent's turn.** The read runs on a daemon thread and the caller
    waits on a queue, so a developer who walks away costs their agent nothing. A bare
    ``input()`` here would block the process — including the run loop — until Enter.

    The thread is why a caller should ask ONCE per run: a timed-out reader is still
    holding stdin, and a second prompt would find its answer swallowed by the first.
    """
    stream = stream or sys.stdout
    stdin = stdin or sys.stdin

    if not (_isatty(stream) and _isatty(stdin)):
        return WATCH_TERMINAL

    if color is None:
        color = os.environ.get("NO_COLOR") is None

    def c(text: str, code: str) -> str:
        return f"\033[{code}m{text}\033[0m" if color else text

    rows = [
        c(label, "36"),
        "",
        f"{c('[b]', '1')} watch the live table in your browser",
        f"{c('[t]', '1')} follow the logs here          {c('· default', '90')}",
    ]
    # Sized to the content: a long match id must not blow the border out of alignment.
    # Measured on the UNCOLORED text — escape codes take columns in a string and none
    # on screen, so padding by len() of a colored row draws a visibly crooked box.
    width = max(_visible_len(r) for r in rows) + 2
    stream.write("\n" + c("╭─ match found " + "─" * max(0, width - 13) + "╮", "90") + "\n")
    for r in rows:
        stream.write(c("│", "90") + " " + r + " " * (width - _visible_len(r)) + c("│", "90") + "\n")
    stream.write(c("╰" + "─" * (width + 1) + "╯", "90") + "\n")
    if url:
        stream.write("  " + c(url, "90") + "\n")
    stream.write("  " + c("›", "36") + " ")
    stream.flush()

    answer = _read_line(stdin, timeout)
    if answer is None:
        # Say the default was taken rather than leaving a bare prompt on screen — an
        # unexplained newline reads as a dropped keystroke.
        stream.write("\n  " + c(f"no answer in {int(timeout)}s — following here", "90") + "\n")
        stream.flush()
        return WATCH_TERMINAL
    return WATCH_BROWSER if answer.strip().lower().startswith("b") else WATCH_TERMINAL


def friends_url(dashboard: str | None = None) -> str:
    """Absolute Play-a-friend URL. Empty dashboard → no invented public link."""
    base = (DEFAULT_DASHBOARD if dashboard is None else dashboard).rstrip("/")
    return f"{base}/friends" if base else ""


def wallet_buy_url(dashboard: str | None = None) -> str:
    """Absolute Buy-coins wallet URL (``/wallet?tab=buy``). Empty dashboard → no link."""
    base = (DEFAULT_DASHBOARD if dashboard is None else dashboard).rstrip("/")
    return f"{base}/wallet?tab=buy" if base else ""


def api_error_code(resp: object) -> str:
    """Normalize arena error envelopes to a lowercase code string."""
    if not isinstance(resp, dict):
        return str(resp or "").lower()
    code = resp.get("code")
    if isinstance(code, str) and code.strip():
        return code.strip().lower()
    err = resp.get("error")
    if isinstance(err, dict):
        nested = err.get("code")
        return str(nested or "").strip().lower()
    if isinstance(err, str):
        return err.strip().lower()
    return ""


def is_insufficient_balance(
    resp: object = None,
    *,
    status: int | None = None,
    code: str | None = None,
) -> bool:
    """True for HTTP 402 / ``insufficient_balance`` from CheckJoin and stake sits.

    Used only on paid paths (ranked queue, private rooms). Sandbox never hits this.
    """
    if status == 402:
        return True
    c = (code or "").strip().lower() or (api_error_code(resp) if resp is not None else "")
    if not c:
        return False
    return c == "insufficient_balance" or ("insufficient" in c and "balance" in c)


def offer_buy_coins(
    dashboard: str | None = None,
    *,
    open_browser: bool = True,
    stream: TextIO | None = None,
    color: bool | None = None,
    is_tty: bool | None = None,
    opener: Callable[[str], bool] | None = None,
) -> str:
    """Print a professional funds prompt for a refused paid sit; optionally open Buy coins.

    Never waits for input — non-TTY / CI only print the URL. Returns the URL (may be empty).
    """
    stream = stream or sys.stderr
    url = wallet_buy_url(dashboard)
    tty = _isatty(stream) if is_tty is None else is_tty

    if color is None:
        color = os.environ.get("NO_COLOR") is None and tty

    def c(text: str, code: str) -> str:
        return f"\033[{code}m{text}\033[0m" if color else text

    rows = [
        c("not enough coins to cover this stake", "1"),
        "",
        c("Buy coins (or allocate to this agent), then retry.", "90"),
    ]
    width = max(_visible_len(r) for r in rows) + 2
    stream.write("\n" + c("╭─ pyyol " + "─" * max(0, width - 7) + "╮", "90") + "\n")
    for r in rows:
        stream.write(c("│", "90") + " " + r + " " * (width - _visible_len(r)) + c("│", "90") + "\n")
    stream.write(c("╰" + "─" * (width + 1) + "╯", "90") + "\n")
    if url:
        stream.write("  " + c(url, "36") + "\n")
    stream.flush()

    if url and open_browser and tty:
        try:
            if opener is not None:
                opened = bool(opener(url))
            else:
                import webbrowser

                opened = bool(webbrowser.open(url))
            if opened:
                stream.write("  " + c("opened Buy coins in your browser", "32") + "\n")
                stream.flush()
        except Exception:  # noqa: BLE001 — URL already printed; opening is a bonus
            pass
    return url


def ask_intent(
    timeout: float = 10.0,
    stream: TextIO | None = None,
    color: bool | None = None,
    stdin: TextIO | None = None,
) -> str:
    """Ask Join a game vs Invite a friend. Returns ``INTENT_QUEUE`` or ``INTENT_INVITE``.

    Same three rules as ``ask_watch``: never blocks a machine, never outlives the
    countdown, never eats the agent's turn. Timeout defaults to **Join** so TTY
    muscle memory and scripts that somehow hit a TTY still queue.
    """
    stream = stream or sys.stdout
    stdin = stdin or sys.stdin

    if not (_isatty(stream) and _isatty(stdin)):
        return INTENT_QUEUE

    if color is None:
        color = os.environ.get("NO_COLOR") is None

    def c(text: str, code: str) -> str:
        return f"\033[{code}m{text}\033[0m" if color else text

    rows = [
        c("how should this agent play?", "36"),
        "",
        f"{c('[j]', '1')} Join a game                   {c('· default', '90')}",
        f"{c('[i]', '1')} Invite a friend",
    ]
    width = max(_visible_len(r) for r in rows) + 2
    stream.write("\n" + c("╭─ pyyol play " + "─" * max(0, width - 12) + "╮", "90") + "\n")
    for r in rows:
        stream.write(c("│", "90") + " " + r + " " * (width - _visible_len(r)) + c("│", "90") + "\n")
    stream.write(c("╰" + "─" * (width + 1) + "╯", "90") + "\n")
    stream.write("  " + c("›", "36") + " ")
    stream.flush()

    answer = _read_line(stdin, timeout)
    if answer is None:
        stream.write("\n  " + c(f"no answer in {int(timeout)}s — joining a game", "90") + "\n")
        stream.flush()
        return INTENT_QUEUE
    return INTENT_INVITE if answer.strip().lower().startswith("i") else INTENT_QUEUE


def resolve_startup_intent(
    *,
    ranked: bool = False,
    mode: str | None = None,
    queue_flag: bool = False,
    invite_flag: bool = False,
    env: dict[str, str] | None = None,
    is_tty: bool = False,
    ask: Callable[..., str] | None = None,
    ask_timeout: float = 10.0,
) -> str:
    """Decide queue vs invite for ``pyyol play`` (not ``dev`` / ``run``).

    Precedence: ``--ranked`` / ``--queue`` / ``--invite`` / ``--mode`` →
    ``$PYYOL_STARTUP`` → TTY ask (default Join) → non-TTY queue.
    """
    if ranked or queue_flag:
        return INTENT_QUEUE
    if invite_flag:
        return INTENT_INVITE
    m = (mode or "").strip().lower()
    if m == "queue":
        return INTENT_QUEUE
    if m == "invite":
        return INTENT_INVITE
    env_map = env if env is not None else os.environ
    env_v = (env_map.get("PYYOL_STARTUP") or "").strip().lower()
    if env_v in (INTENT_QUEUE, INTENT_INVITE):
        return env_v
    # mode ask / unset: TTY prompts; CI / pipes keep today's auto-start.
    if not is_tty:
        return INTENT_QUEUE
    asker = ask or ask_intent
    return asker(timeout=ask_timeout)


def _isatty(f: TextIO) -> bool:
    """isatty() that tolerates a stream which does not have it (StringIO in tests)."""
    try:
        return bool(f.isatty())
    except Exception:  # noqa: BLE001 - a stream that cannot answer is not a terminal
        return False


def _read_line(stdin: TextIO, timeout: float) -> str | None:
    """One line from stdin, or None if ``timeout`` passes first.

    A thread rather than ``select`` because select() on Windows accepts sockets only,
    and this is the path a developer on Windows runs every day.
    """
    box: queue.Queue[str] = queue.Queue(maxsize=1)

    def read() -> None:
        try:
            box.put(stdin.readline())
        except Exception:  # noqa: BLE001 - a closed stdin is a non-answer, not a crash
            pass

    threading.Thread(target=read, daemon=True).start()
    try:
        return box.get(timeout=timeout)
    except queue.Empty:
        return None


def _visible_len(s: str) -> int:
    """Width on screen: the length with ANSI escape sequences removed."""
    return len(_ANSI_RE.sub("", s))


_ANSI_RE = re.compile(r"\033\[[0-9;]*m")


def build_console(mode: str = "pretty", quiet: bool = False, color: bool | None = None) -> Console:
    """Factory used by the CLI: mode is ``pretty`` | ``json``."""
    if mode == "json":
        return JsonConsole()
    return PrettyConsole(color=color, quiet=quiet)
