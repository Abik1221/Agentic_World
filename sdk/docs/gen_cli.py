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
    ("Play", "Get a game going.", ["play", "dev", "games", "watch", "queue"]),
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
    w("## The interactive shell\n\n")
    w("Typing `pyyol` on a terminal opens a home screen: what is live right now, who you are\n"
      "signed in as, and a prompt.\n\n")
    w("```\npyyol\n```\n\n")
    w("- `/` opens a picker you arrow through, filter by typing, and choose with Enter.\n"
      "- Every command below works inside it, with or without the leading slash, and flags\n"
      "  pass straight through: `/play mafia --ranked`.\n"
      "- `tab` completes, `Ctrl-C` stops a running command, `/exit` leaves.\n\n")
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
