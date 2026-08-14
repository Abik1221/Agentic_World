"""The interactive front door: what you get when you type ``pyyol`` and nothing else.

# Why this exists

``pyyol``'s subcommand is ``required=True``, so the very first thing a developer typed after
installing — the tool's own name — printed a usage error and exited 2. Every other modern agent
CLI answers that with a home screen.

# Why it is a front door and NOT a replacement

Every command stays exactly as it is on the command line. ``pyyol publish`` has to keep working
in CI, in a Dockerfile, in a Makefile and in the docs, and a REPL-only tool cannot be scripted.
So there is ONE dispatch layer: this shell parses a line with the SAME ``build_parser()`` the
command line uses and calls the SAME ``args.func``. A command added to the parser appears here
automatically, and the two surfaces cannot drift apart.

# The one hard rule

A shell is only ever entered on a TTY. ``pyyol | cat``, CI, cron and a Dockerfile ``RUN`` must
print help and exit — a REPL that waits for stdin there hangs the pipeline forever, and it would
hang it in exactly the environments nobody is watching.
"""

from __future__ import annotations

import os
import shlex
import sys
import threading
from typing import Any, TextIO
from collections.abc import Callable

_RESET = "\x1b[0m"
_BOLD = "\x1b[1m"
_DIM = "\x1b[2m"
_BRAND = "\x1b[38;5;69m"  # the console's cyan-indigo, degrades to plain on 16-colour terms
_OK = "\x1b[32m"
_WARN = "\x1b[33m"
_ERR = "\x1b[31m"

# Typed at the prompt, these end the session. Ctrl-D and Ctrl-C do the same.
_QUIT = {"exit", "quit", "q", ":q"}


def use_color(stream: TextIO) -> bool:
    """Colour only when someone is there to see it.

    NO_COLOR is honoured because a logo rendered into a CI log or a piped file is noise at
    best and mojibake at worst.
    """
    return stream.isatty() and os.environ.get("NO_COLOR") is None


class _Style:
    def __init__(self, color: bool) -> None:
        self.color = color

    def __call__(self, text: str, code: str) -> str:
        return f"{code}{text}{_RESET}" if self.color else text


# The wordmark: one entry per LETTER, five rows each.
#
# Per letter rather than one wide string because the colour sweep is applied per letter. A
# sweep applied per COLUMN lands its boundaries mid-glyph — half a stroke in one hue and half
# in the next — which reads as a rendering fault rather than as a design. Splitting on the
# letterforms is what makes it look deliberate.
#
# Five rows, because a P and a Y do not resolve in four: the first version of this banner drew
# its Ys with the arms on different rows, closer to an H.
_LETTERS: list[list[str]] = [
    ["██████ ", "██   ██", "██████ ", "██     ", "██     "],  # P
    ["██    ██", " ██  ██ ", "  ████  ", "   ██   ", "   ██   "],  # Y
    ["██    ██", " ██  ██ ", "  ████  ", "   ██   ", "   ██   "],  # Y
    [" ██████ ", "██    ██", "██    ██", "██    ██", " ██████ "],  # O
    ["██     ", "██     ", "██     ", "██     ", "███████"],  # L
]

# Indigo → cyan, one stop per letter. 256-colour rather than truecolour: it renders the same
# over ssh and inside tmux, where truecolour silently degrades to something muddy.
_RAMP = [63, 69, 75, 81, 87]

_GAP = "  "


def _wordmark(color: bool) -> str:
    rows = []
    for r in range(5):
        parts = []
        for i, letter in enumerate(_LETTERS):
            glyph = letter[r]
            parts.append(f"\x1b[38;5;{_RAMP[i]}m{glyph}{_RESET}" if color else glyph)
        rows.append("  " + _GAP.join(parts))
    return "\n".join(rows)


# Commands GROUPED and ordered by what a developer actually does, not alphabetically.
#
# Alphabetical put `arenas` and `autoplay` at the top and buried `play` and `dev` — the daily
# loop — in the middle of twenty-five entries. The order below is the order of a working day:
# get a game going, ship the agent, look at what happened, then the account plumbing you touch
# once. The groups are an ORDER, not a LIST — anything they do not claim still appears under
# "More", so a command added to the parser can never go missing because nobody updated this.
_GROUPS: list[tuple[str, list[str]]] = [
    ("Play", ["play", "dev", "games", "watch", "queue"]),
    ("Ship", ["init", "publish", "serve", "autoplay"]),
    ("Inspect", ["status", "doctor", "usage", "replay", "logs"]),
    ("Standing", ["leaderboard", "profile", "wallet", "arenas"]),
    ("Account", ["login", "whoami", "logout", "update"]),
    ("Advanced", ["run", "validate", "simulate"]),
]


# ── The live strip ──────────────────────────────────────────────────────────────
#
# What is actually happening, on the line above the prompt, refreshed while you sit there.
# The arena's whole proposition is that agents are playing right now, and a home screen that
# shows a URL instead of that number is describing the tool rather than the platform.
#
# Polled rather than streamed because the overview has no SSE — /v1/matches/{id}/watch streams
# ONE match, and opening three sockets to count games would cost more than a 200-byte read.
#
# It redraws IN PLACE and only while the input line is empty. Rewriting the terminal under
# someone mid-word is how a live display becomes a thing people disable, so a partially typed
# command always wins.
_LIVE_EVERY_S = 5.0


class _LiveStrip:
    def __init__(self, api: str, s: _Style, stream: TextIO) -> None:
        self.api, self.s, self.stream = api, s, stream
        # Shown until the first fetch lands, which is a fraction of a second on a healthy
        # arena and honest on an unhealthy one.
        self.text = "  " + s("LIVE", _DIM) + "  " + s("checking…", _DIM)
        self._stop = threading.Event()
        self._thread: threading.Thread | None = None

    # -- rendering -------------------------------------------------------------
    def _render(self, games: list[dict[str, Any]]) -> str:
        s = self.s
        if not games:
            return "  " + s("arena unreachable", _DIM)
        parts = []
        total_live = total_wait = 0
        for g in games:
            name = str(g.get("game", "?"))
            live = int(g.get("live", 0) or 0)
            playing = int(g.get("playing", 0) or 0)
            waiting = int(g.get("waiting", 0) or 0)
            total_live += live
            total_wait += waiting
            if live:
                parts.append(
                    s("●", _OK)
                    + " "
                    + s(name, _BOLD)
                    + " "
                    + s(f"{live} live · {playing} playing", _DIM)
                )
            elif waiting:
                parts.append(
                    s("◌", _WARN) + " " + s(name, _BOLD) + " " + s(f"{waiting} queued", _DIM)
                )
            else:
                parts.append(s("·", _DIM) + " " + s(name, _DIM) + " " + s("idle", _DIM))
        head = s("LIVE", _DIM) if (total_live or total_wait) else s("LIVE", _DIM)
        return "  " + head + "  " + s("   ", _DIM).join(parts)

    def fetch(self) -> None:
        """Never raises, and never takes long enough to be noticed.

        A SHORT timeout and no retries, unlike the command helpers: this is a decorative poll,
        so a slow arena must degrade to "unreachable" rather than hold anything up. The shared
        helper defaults to 15s and retries a 429 three times — up to 45 seconds, which on the
        open path would look exactly like a hung tool.
        """
        games: list[dict[str, Any]] = []
        try:
            import urllib.request

            from .cli import _urlopen_json

            st, resp = _urlopen_json(
                urllib.request.Request(f"{self.api}/v1/games", method="GET"), timeout=2.0
            )
            if st == 200:
                games = (resp or {}).get("games") or []
        except Exception:  # noqa: BLE001 - the door must open even if the API is down
            games = []
        self.text = self._render(games)

    # -- the idle refresh ------------------------------------------------------
    def _typing(self) -> bool:
        try:
            import readline

            return bool(readline.get_line_buffer())
        except Exception:  # noqa: BLE001 - no readline, or an uninitialised one, just means "not typing"
            return False

    def _loop(self) -> None:
        # The FIRST fetch happens here, not on the open path. Blocking the door on a network
        # call is how a CLI comes to feel slow — and it is worst exactly when the platform is
        # having a bad day, which is when someone most needs the prompt.
        self.fetch()
        self._redraw()
        while not self._stop.wait(_LIVE_EVERY_S):
            before = self.text
            self.fetch()
            if self.text != before:
                self._redraw()

    def _redraw(self) -> None:
        """Repaint the strip in place, and only while nothing is half-typed."""
        if self._typing():
            return
        # Save cursor, step up onto the strip, clear it, rewrite, come back. The prompt and
        # anything typed on it are untouched.
        try:
            self.stream.write("\x1b[s\x1b[1A\x1b[2K\r" + self.text + "\x1b[u")
            self.stream.flush()
        except Exception:  # noqa: BLE001 - a redraw that cannot write must not kill the strip thread
            return

    def start(self) -> None:
        if not self.s.color:
            self.fetch()  # no ANSI to redraw with, so fetch once and let it be printed
            return
        self._thread = threading.Thread(target=self._loop, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        self._stop.set()


def wordmark_for(stream: TextIO) -> str:
    """The wordmark, if this stream can actually print it. Empty string otherwise.

    `pyyol --help` is the first thing a developer sees in CI, in a Dockerfile, or when they
    pipe the tool anywhere, and it showed bare argparse output with no sign of what this is.
    The shell had all the identity and only people who found the shell ever saw it.

    THE ENCODING CHECK IS NOT OPTIONAL. The glyphs are U+2588 FULL BLOCK. On a legacy Windows
    console (cp1252) printing them raises UnicodeEncodeError — so decorating --help crashed
    --help, with a traceback, on the one platform least likely to be able to read the fix. A
    logo that can break `--help` is not a logo, it is an outage with a brand on it.

    Measured, not assumed: the candidate string is encoded against the stream's own encoding,
    so a terminal that can render it gets it and one that cannot gets clean text.
    """
    art = _wordmark(use_color(stream))
    enc = getattr(stream, "encoding", None) or "ascii"
    try:
        art.encode(enc, errors="strict")
    except (UnicodeEncodeError, LookupError):
        return ""
    return art


def _banner(s: _Style, version: str, api: str, who: dict[str, Any] | None) -> str:
    lines = [_wordmark(s.color)]
    lines.append("")
    lines.append(
        "  " + s(f"v{version}", _DIM) + s("   build, run and rank autonomous agents", _DIM)
    )
    lines.append("")

    # Identity first: every other line means something different depending on it.
    if who and who.get("handle"):
        ident = s("●", _OK) + f"  {who['handle']}"
        if who.get("agent"):
            ident += s(f"   {who['agent']}", _DIM)
    else:
        ident = s("○", _WARN) + "  not signed in" + s("   /login to start", _DIM)
    lines.append("  " + ident)
    lines.append("  " + s(api, _DIM))
    lines.append("")

    # THE AFFORDANCE. One key, said plainly. A developer should never have to guess that a
    # slash does anything, and "/help" alone does not teach that "/" on its own is a menu.
    lines.append(
        "  "
        + s("type", _DIM)
        + " "
        + s("/", _BOLD)
        + " "
        + s("for commands", _DIM)
        + s("      ", _DIM)
        + s("tab", _BOLD)
        + s(" completes", _DIM)
        + s("      ", _DIM)
        + s("/exit", _BOLD)
        + s(" to leave", _DIM)
    )
    return "\n".join(lines)


def _whoami(api: str) -> dict[str, Any] | None:
    """Best-effort identity for the header. Never fatal: the shell must open even when the
    platform is unreachable, because /doctor and /login are exactly what you need then."""
    try:
        from . import credentials

        creds = credentials.load()
        if not creds:
            return None
        return {
            "handle": getattr(creds, "handle", None)
            or getattr(creds, "email", None)
            or "signed in",
            "agent": getattr(creds, "agent_id", None) or "",
        }
    except Exception:  # noqa: BLE001 - an unreadable credential store means "signed out", not a crash
        return None


def _commands(parser: Any) -> dict[str, str]:
    """Command name → help text, read off the real parser.

    Read rather than duplicated, so /help cannot list a command that no longer exists or miss
    one that was just added.
    """
    out: dict[str, str] = {}
    for action in parser._actions:  # noqa: SLF001 — argparse exposes no public accessor
        if not hasattr(action, "choices") or not isinstance(action.choices, dict):
            continue
        for name, sub in action.choices.items():
            out[name] = (sub.description or "").strip() or _help_of(action, name)
    return out


def _help_of(action: Any, name: str) -> str:
    for choice in getattr(action, "_choices_actions", []):
        if choice.dest == name:
            return (choice.help or "").strip()
    return ""


def _print_help(s: _Style, cmds: dict[str, str], stream: TextIO) -> None:
    """The palette: grouped, ordered by use, and complete.

    Complete matters — the groups are a hand-written ORDER, not a hand-written LIST. Anything
    in the parser that no group claims still appears under "More", so a command added tomorrow
    shows up here whether or not anyone remembered this file.
    """
    stream.write("\n")
    shown: set[str] = set()
    width = max((len(c) for c in cmds), default=10) + 1

    for title, names in _GROUPS:
        present = [n for n in names if n in cmds]
        if not present:
            continue
        stream.write("  " + s(title.upper(), _DIM) + "\n")
        for name in present:
            shown.add(name)
            stream.write(f"    {s('/' + name.ljust(width), _BRAND)} {s(cmds[name], _DIM)}\n")
        stream.write("\n")

    rest = sorted(set(cmds) - shown)
    if rest:
        stream.write("  " + s("MORE", _DIM) + "\n")
        for name in rest:
            stream.write(f"    {s('/' + name.ljust(width), _BRAND)} {s(cmds[name], _DIM)}\n")
        stream.write("\n")

    stream.write(
        "  "
        + s("flags pass straight through", _DIM)
        + s("   e.g. ", _DIM)
        + s("/play mafia --ranked", _BOLD)
        + "\n"
    )
    stream.write(
        "  "
        + s("/clear", _BRAND)
        + s(" screen", _DIM)
        + s("    ", _DIM)
        + s("/exit", _BRAND)
        + s(" leave", _DIM)
        + "\n\n"
    )


# ── The picker ──────────────────────────────────────────────────────────────────
#
# What "/" gives you when the terminal can do it: a list you arrow through, filter by typing,
# and choose with Enter. Every other agent CLI has this and it is the difference between
# twenty-five commands being a menu and being a wall.
#
# POSIX raw mode only, and it degrades rather than fails: without termios (Windows, a dumb
# terminal, a pipe) "/" prints the grouped palette instead, which is the same information
# without the cursor. A picker that crashed on an unusual terminal would be worse than the
# printed list it replaced.


def _pick(
    s: _Style, cmds: dict[str, str], groups: list[tuple[str, list[str]]], stream: TextIO
) -> str | None:
    """Return the chosen command name, or None to cancel (Esc / Ctrl-C / no terminal)."""
    try:
        import termios
        import tty
    except ImportError:
        return None
    if not (stream.isatty() and sys.stdin.isatty()):
        return None

    # Flattened in the SAME order the palette groups use, so the picker and the printed list
    # never disagree about what comes first.
    ordered: list[tuple[str, str]] = []
    seen: set[str] = set()
    for _title, names in groups:
        for n in names:
            if n in cmds and n not in seen:
                ordered.append((n, cmds[n]))
                seen.add(n)
    for n in sorted(set(cmds) - seen):
        ordered.append((n, cmds[n]))

    query = ""
    idx = 0
    rows = min(10, len(ordered))
    drawn = 0
    fd = sys.stdin.fileno()
    old = termios.tcgetattr(fd)

    def matches() -> list[tuple[str, str]]:
        if not query:
            return ordered
        q = query.lower()
        return [(n, h) for n, h in ordered if q in n.lower()]

    def draw() -> None:
        nonlocal drawn
        if drawn:
            stream.write(f"\x1b[{drawn}A")
        stream.write("\x1b[J")
        hits = matches()
        head = "  " + s("/" + query, _BOLD) + s("   ↑↓ move · enter run · esc cancel", _DIM)
        stream.write(head + "\n")
        shown = hits[:rows]
        for i, (name, help_text) in enumerate(shown):
            mark = s(" ❯ ", _BRAND) if i == idx else "   "
            label = s(name.ljust(12), _BRAND if i == idx else _DIM)
            stream.write(f"{mark}{label} {s(help_text[:60], _DIM)}\n")
        if not shown:
            stream.write("   " + s("no command matches", _DIM) + "\n")
        drawn = 1 + max(1, len(shown))
        stream.flush()

    try:
        tty.setraw(fd)
        while True:
            draw()
            ch = sys.stdin.read(1)
            hits = matches()
            if ch == "\x1b":  # escape, or an arrow key's prefix
                nxt = sys.stdin.read(1) if sys.stdin.readable() else ""
                if nxt != "[":
                    return None
                key = sys.stdin.read(1)
                if key == "A":
                    idx = max(0, idx - 1)
                elif key == "B":
                    idx = min(max(0, min(rows, len(hits)) - 1), idx + 1)
                continue
            if ch in ("\r", "\n"):
                return hits[idx][0] if hits else None
            if ch == "\x03":  # Ctrl-C cancels the menu, not the session
                return None
            if ch in ("\x7f", "\b"):
                query = query[:-1]
                idx = 0
                continue
            if ch.isprintable():
                query += ch
                idx = 0
    finally:
        termios.tcsetattr(fd, termios.TCSADRAIN, old)
        stream.write("\x1b[J")
        stream.flush()


def _install_readline(cmds: dict[str, str]) -> None:
    """History and tab completion. Optional: a platform without readline still gets a shell,
    it just does not complete — which is worth having rather than refusing to start."""
    try:
        import readline
    except ImportError:
        return

    names = sorted(cmds)

    def complete(text: str, state: int) -> str | None:
        stripped = text[1:] if text.startswith("/") else text
        prefix = "/" if text.startswith("/") else ""
        hits = [prefix + n for n in names if n.startswith(stripped)]
        return hits[state] if state < len(hits) else None

    readline.set_completer(complete)
    readline.set_completer_delims(" \t\n")
    readline.parse_and_bind("tab: complete")


def run_shell(
    build_parser: Callable[[], Any],
    version: str,
    api_base: str,
    stream: TextIO | None = None,
) -> int:
    """The prompt loop. Returns a process exit code."""
    out = stream or sys.stdout
    s = _Style(use_color(out))
    parser = build_parser()
    cmds = _commands(parser)

    out.write(_banner(s, version, api_base, _whoami(api_base)) + "\n\n")
    out.flush()
    _install_readline(cmds)

    # What is happening right now, fetched once before the first prompt so the opening screen
    # already carries it, then refreshed in place while the prompt is idle.
    strip = _LiveStrip(api_base, s, out)
    strip.start()

    prompt = s("pyyol", _BRAND) + s(" › ", _DIM) if s.color else "pyyol > "

    try:
        return _loop(parser, cmds, s, out, prompt, strip)
    finally:
        strip.stop()


def _loop(
    parser: Any,
    cmds: dict[str, str],
    s: _Style,
    out: TextIO,
    prompt: str,
    strip: _LiveStrip,
) -> int:
    while True:
        # Reprinted each cycle so it always sits directly above the prompt — a command's
        # output scrolls the previous one away, and a strip stranded mid-scrollback is worse
        # than none because it goes stale where nobody looks.
        out.write(strip.text + "\n")
        out.flush()
        try:
            line = input(prompt).strip()
        except (EOFError, KeyboardInterrupt):
            # Ctrl-D / Ctrl-C at an empty prompt is "I am done", not an error.
            out.write("\n")
            return 0

        if not line:
            continue
        if line.lower() in _QUIT or line.lower().lstrip("/") in _QUIT:
            return 0

        bare = line[1:].strip() if line.startswith("/") else line
        if not bare:
            # A lone "/" is the menu — the affordance the banner advertises, and the first
            # thing anyone coming from another agent CLI reaches for. An interactive pick
            # where the terminal allows it, the printed palette where it does not.
            chosen = _pick(s, cmds, _GROUPS, out)
            if chosen is None:
                _print_help(s, cmds, out)
                continue
            out.write(s("  /" + chosen, _BRAND) + "\n")
            _dispatch(parser, [chosen], s, out)
            continue
        head = bare.split()[0].lower()

        if head in {"help", "?", "h"}:
            _print_help(s, cmds, out)
            continue
        if head == "clear":
            out.write("\x1b[2J\x1b[H" if s.color else "\n" * 50)
            out.flush()
            continue

        try:
            argv = shlex.split(bare)
        except ValueError as e:  # an unbalanced quote must not kill the session
            out.write(s(f"  could not read that line: {e}\n", _ERR))
            continue

        if argv and argv[0] not in cmds:
            out.write(
                s(f"  unknown command: {argv[0]}", _ERR) + s("   /help lists them all\n", _DIM)
            )
            continue

        _dispatch(parser, argv, s, out)


def _dispatch(parser: Any, argv: list[str], s: _Style, out: TextIO) -> None:
    """Run one command through the REAL parser, and survive whatever it does.

    Three things routinely try to end the process here and must not end the session:
    argparse calls ``exit()`` on a bad flag or ``--help``; a long-running command
    (dev/play/watch/serve) ends on Ctrl-C; and any command can raise. A shell that died on
    a typo'd flag would be worse than no shell.
    """
    try:
        args = parser.parse_args(argv)
    except SystemExit:
        return

    try:
        rc = args.func(args)
        if isinstance(rc, int) and rc != 0:
            out.write(s(f"  exited {rc}\n", _WARN))
    except KeyboardInterrupt:
        # Ctrl-C stops the COMMAND and returns to the prompt, which is what it means inside
        # a shell — leaving is /exit.
        out.write("\n" + s("  stopped\n", _DIM))
    except SystemExit as e:
        code = e.code if isinstance(e.code, int) else 0
        if code:
            out.write(s(f"  exited {code}\n", _WARN))
    except Exception as e:  # noqa: BLE001 — the session must outlive any single command
        # The SAME report the command line gives, minus the exit: a crash here printed a bare
        # "IndexError: list index out of range", which tells a developer nothing about whose
        # bug it is or where the detail went. The session continues either way.
        from ._crash import report_crash
        from . import __version__

        report_crash(e, argv[0] if argv else "", __version__, stream=out)
