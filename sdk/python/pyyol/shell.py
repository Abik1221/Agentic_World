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
from typing import Any, Callable, TextIO

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
    ["██████ ", "██   ██", "██████ ", "██     ", "██     "],           # P
    ["██    ██", " ██  ██ ", "  ████  ", "   ██   ", "   ██   "],      # Y
    ["██    ██", " ██  ██ ", "  ████  ", "   ██   ", "   ██   "],      # Y
    [" ██████ ", "██    ██", "██    ██", "██    ██", " ██████ "],      # O
    ["██     ", "██     ", "██     ", "██     ", "███████"],           # L
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
        "  " + s("type", _DIM) + " " + s("/", _BOLD) + " " + s("for commands", _DIM)
        + s("      ", _DIM) + s("tab", _BOLD) + s(" completes", _DIM)
        + s("      ", _DIM) + s("/exit", _BOLD) + s(" to leave", _DIM)
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
            "handle": getattr(creds, "handle", None) or getattr(creds, "email", None) or "signed in",
            "agent": getattr(creds, "agent_id", None) or "",
        }
    except Exception:
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
        "  " + s("flags pass straight through", _DIM)
        + s("   e.g. ", _DIM) + s("/play mafia --ranked", _BOLD) + "\n"
    )
    stream.write(
        "  " + s("/clear", _BRAND) + s(" screen", _DIM)
        + s("    ", _DIM) + s("/exit", _BRAND) + s(" leave", _DIM) + "\n\n"
    )


def _install_readline(cmds: dict[str, str]) -> None:
    """History and tab completion. Optional: a platform without readline still gets a shell,
    it just does not complete — which is worth having rather than refusing to start."""
    try:
        import readline
    except Exception:
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

    prompt = s("pyyol", _BRAND) + s(" › ", _DIM) if s.color else "pyyol > "

    while True:
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
            # A lone "/" is the menu. This is the affordance the banner advertises, and it is
            # what every developer coming from another agent CLI reaches for first.
            _print_help(s, cmds, out)
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
                s(f"  unknown command: {argv[0]}", _ERR)
                + s("   /help lists them all\n", _DIM)
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
        out.write(s(f"  {type(e).__name__}: {e}\n", _ERR))
