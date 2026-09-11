#!/usr/bin/env python3
"""Generate cli.md from the REAL argparse parser.

Hand-written command lists rot. This one is read off `pyyol.cli.build_parser()`, which is the
same object the CLI and the interactive shell dispatch through, so a command added tomorrow
appears here the next time this runs — and one that is removed cannot linger.

    python sdk/docs/gen_cli.py > sdk/docs/cli.md
"""
from __future__ import annotations

import argparse
import io
import sys

from pyyol import __version__, cli

# The order of a working day, mirroring the shell's palette (pyyol/shell.py _GROUPS).
# Alphabetical buried `play` and `dev` behind `arenas` and `autoplay`.
GROUPS = [
    ("Play", "Get a game going.", ["play", "dev", "games", "watch", "queue", "room"]),
    ("Ship", "Put your agent where it can earn.", ["init", "publish", "serve", "autoplay"]),
    ("Inspect", "What happened, and what it cost.", ["status", "doctor", "usage", "replay", "logs"]),
    ("Standing", "Where you rank.", ["leaderboard", "profile", "wallet", "arenas"]),
    ("Account", "Sign in and keep current.", ["login", "whoami", "logout", "update"]),
    ("Advanced", "Lower-level entry points.", ["run", "validate", "simulate"]),
]


def subparsers(parser: argparse.ArgumentParser) -> dict[str, argparse.ArgumentParser]:
    for action in parser._actions:  # noqa: SLF001 — argparse exposes no public accessor
        if isinstance(getattr(action, "choices", None), dict):
            return dict(action.choices)
    return {}


def helps(parser: argparse.ArgumentParser) -> dict[str, str]:
    out: dict[str, str] = {}
    for action in parser._actions:  # noqa: SLF001
        for choice in getattr(action, "_choices_actions", []):
            out[choice.dest] = (choice.help or "").strip()
    return out


def usage_of(sub: argparse.ArgumentParser) -> str:
    buf = io.StringIO()
    sub.print_help(buf)
    return buf.getvalue().rstrip()


def main() -> int:
    parser = cli.build_parser()
    subs, hints = subparsers(parser), helps(parser)

    w = sys.stdout.write
    w("# CLI reference\n\n")
    w(f"Generated from `pyyol` v{__version__}. Every command below is real — this page is\n"
      "produced from the parser the CLI dispatches through, so it cannot list a command that\n"
      "does not exist or miss one that does.\n\n")
    w("## Start here: `pyyol`\n\n")
    w("Type `pyyol` on a terminal and you get a home screen — what is live right now, who you\n"
      "are signed in as, and a prompt. **This is the front door.** Everything below can be run\n"
      "from it, so there is one thing to remember rather than twenty-five.\n\n")
    w("```\npyyol\n```\n\n")
    # The pictures are GENERATED from the real CLI by gen_shots.py, which runs it under a PTY
    # and converts what it prints. A hand-drawn mockup drifts from the tool the moment either
    # changes and a reader cannot tell; these change when the banner does.
    w('<p align="center">\n'
      '  <img src="assets/cli-home.svg" alt="The pyyol home screen: wordmark, version,'
      ' sign-in state and the affordance line" width="760">\n'
      "</p>\n\n")
    w("At the prompt, press `/` — that is a key, not a line to submit. The command menu\n"
      "opens immediately (no Enter). Arrow to move, type to filter, Enter to run:\n\n")
    w('<p align="center">\n'
      '  <img src="assets/cli-menu.svg" alt="The pyyol / command menu, grouped into PLAY,'
      ' SHIP and INSPECT" width="760">\n'
      "</p>\n\n")
    w("Both pictures are produced from the REAL CLI by `sdk/docs/gen_shots.py`, so they change\n"
      "when the tool does.\n\n")
    w("### Inside the shell\n\n")
    w("- `/` opens the picker **on the keystroke** — do not press Enter first. Arrow to move,\n"
      "  type to filter — the filter matches the DESCRIPTION as well as the name, so \"stake\"\n"
      "  finds `play` and \"coins\" finds `wallet`. Enter runs it.\n"
      "- `help` (or `/help` from bash) lists every command. `help play` shows that command's flags.\n"
      "- Long lists scroll and a counter shows your position, so every command is reachable.\n"
      "- Every command below works inside it, with or without the leading slash, and flags\n"
      "  pass straight through: `play mafia --ranked`.\n"
      "- `tab` completes, `Ctrl-C` stops the running command (not the session), `/exit` leaves.\n"
      "- From bash, `pyyol /help` and `pyyol /play …` work too — a leading slash is stripped.\n\n")
    w("**Not on a terminal, no prompt.** Piped, in CI, in cron or in a Dockerfile `RUN`,\n"
      "`pyyol` prints this help and exits — a prompt waiting on stdin there would hang the\n"
      "pipeline forever.\n\n")

    seen: set[str] = set()
    for title, blurb, names in GROUPS:
        present = [n for n in names if n in subs]
        if not present:
            continue
        w(f"## {title}\n\n{blurb}\n\n")
        for name in present:
            seen.add(name)
            w(f"### `pyyol {name}`\n\n")
            if hints.get(name):
                w(f"{hints[name]}\n\n")
            w("```\n" + usage_of(subs[name]) + "\n```\n\n")

    rest = sorted(set(subs) - seen)
    if rest:
        w("## More\n\n")
        for name in rest:
            w(f"### `pyyol {name}`\n\n")
            if hints.get(name):
                w(f"{hints[name]}\n\n")
            w("```\n" + usage_of(subs[name]) + "\n```\n\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
