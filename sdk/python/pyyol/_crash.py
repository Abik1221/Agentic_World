"""The last line of defence between a bug in this CLI and the developer using it.

# What a developer used to see

`main()` ended in `return args.func(args)`, unguarded. So ANY unexpected exception — an
IndexError in a response parser, a KeyError on a field the platform renamed, an AttributeError
after a refactor — printed a raw Python traceback:

    Traceback (most recent call last):
      File "/usr/lib/python3.12/site-packages/pyyol/cli.py", line 2898, in main
        return args.func(args)
      ...
    IndexError: list index out of range

That is OUR source, OUR line numbers, and OUR variable names, shown to somebody who wanted to
know whether their agent was ranked. It reads as "this tool is broken and I cannot use it", it
is unactionable, and the one detail that WOULD help us fix it — what the user was doing and
which version they were on — is exactly what a traceback buries.

Ctrl-C was worse: pressing it during any command dumped a KeyboardInterrupt stack trace, as if
stopping a program were a crash.

# What replaces it

A crash becomes a short, honest report: this is our bug, not yours; here is how to tell us;
here is where the full detail is. The traceback still exists — it is written to a crash file
and is one env var away — because a developer who WANTS it (or is filing an issue) must not
have to reproduce the fault under a debugger to get it back.

Exit codes follow the shell convention so scripts and CI can branch on them:

    0    fine
    1    an ordinary, expected failure (already reported by the command itself)
    2    usage error (argparse's own)
    70   an internal fault — this module's job (EX_SOFTWARE, sysexits.h)
    130  interrupted with Ctrl-C (128 + SIGINT)
"""

from __future__ import annotations

import os
import sys
import tempfile
import traceback
from pathlib import Path
from collections.abc import Callable
from typing import Any

# A TERMINAL connector failure is a decision about the developer's credential, not a
# fault in pyyol, so the boundary treats it as an ordinary error rather than a crash.
from .runtime import ConnectorError

# EX_SOFTWARE from sysexits.h. Distinct from 1 on purpose: "the command ran and told you it
# failed" and "the command itself broke" are different events, and a CI pipeline should be able
# to tell them apart without scraping stderr.
EXIT_INTERNAL = 70
# 128 + SIGINT, the shell convention. A tool that exits 0 or 1 on Ctrl-C makes `&&` chains
# continue after a human has explicitly stopped them.
EXIT_INTERRUPTED = 130

_ISSUES_URL = "https://github.com/pyyol/pyyol/issues/new"


def _crash_dir() -> Path:
    """Where crash reports go.

    XDG_STATE_HOME is the correct home for this (state a program keeps that is not config and
    not a cache), falling back to ~/.local/state, then to the system temp dir — because a
    read-only or unusual HOME must not turn a crash report into a second crash.
    """
    base = os.environ.get("XDG_STATE_HOME") or os.path.join(
        os.path.expanduser("~"), ".local", "state"
    )
    try:
        d = Path(base) / "pyyol"
        d.mkdir(parents=True, exist_ok=True)
        return d
    except OSError:
        return Path(tempfile.gettempdir())


def _write_report(exc: BaseException, argv: list[str], version: str) -> Path | None:
    """Write the full traceback somewhere retrievable. Returns None if it cannot.

    Failing to write a crash report must never replace the crash message with a different
    error, so every failure here is swallowed: the developer still gets the summary, and still
    gets the traceback via PYYOL_DEBUG.
    """
    try:
        path = _crash_dir() / "last-crash.log"
        with path.open("w", encoding="utf-8") as fh:
            fh.write(f"pyyol {version}\n")
            fh.write(f"python {sys.version.split()[0]} on {sys.platform}\n")
            # The command only — never the arguments. A crash report is a file a developer may
            # paste into a public issue, and pyyol's own arguments include things like
            # `--api`, agent names and, on some commands, tokens.
            fh.write(f"command: pyyol {argv[0] if argv else ''}\n\n")
            fh.write("".join(traceback.format_exception(type(exc), exc, exc.__traceback__)))
        return path
    except OSError:
        return None


def _one_line(exc: BaseException) -> str:
    """The exception, in a form that fits on one line and says something."""
    text = str(exc).strip().splitlines()
    head = text[0] if text else ""
    name = type(exc).__name__
    if not head:
        return name
    if len(head) > 160:
        head = head[:157] + "…"
    return f"{name}: {head}"


def guard(
    fn: Callable[..., int], *args: Any, version: str = "", argv: list[str] | None = None
) -> int:
    """Run a CLI command, converting a crash or a Ctrl-C into a civilised exit.

    Deliberately catches BaseException-derived KeyboardInterrupt separately and lets
    SystemExit pass straight through — a command that has already decided its exit code has
    made a decision, and swallowing it here would override a deliberate `sys.exit(2)` with a
    crash report about nothing.
    """
    argv = argv if argv is not None else sys.argv[1:]
    try:
        return fn(*args)
    except SystemExit:
        raise
    except KeyboardInterrupt:
        # Deliberately quiet. The user pressed Ctrl-C; they know what happened, and printing a
        # stack trace to explain their own keystroke is noise. A newline keeps the shell prompt
        # from landing mid-line after the ^C.
        print(file=sys.stderr)
        print("Stopped.", file=sys.stderr)
        return EXIT_INTERRUPTED
    except ConnectorError as exc:
        # A TERMINAL connector failure is a decision the server made about this
        # credential — "this agent key was revoked, run `pyyol login`" — not a fault in
        # pyyol. Its message is already written for the developer and says exactly what
        # to do.
        #
        # Routing it through the crash report buried that message under "pyyol hit an
        # internal error / This is a bug in pyyol, not in your agent / Report it", which
        # tells someone to file a bug for a ten-second fix and to distrust a tool that is
        # working correctly. A developer hit this mid-setup and reasonably read it as
        # another platform failure.
        #
        # Exit 1, not EXIT_INTERNAL: the command ran and told you it failed, which is a
        # different event from the command breaking — the distinction EXIT_INTERNAL exists
        # to preserve.
        print(f"✗ {exc}", file=sys.stderr)
        return 1
    except BrokenPipeError:
        # `pyyol leaderboard | head` closes the pipe early. That is the pipeline working, not a
        # failure, and Python would otherwise print "BrokenPipeError ... Exception ignored" at
        # shutdown. stdout is redirected to devnull so the interpreter's flush-on-exit has
        # somewhere harmless to go.
        try:
            devnull = os.open(os.devnull, os.O_WRONLY)
            os.dup2(devnull, sys.stdout.fileno())
        except OSError:
            pass
        return EXIT_INTERRUPTED
    except Exception as exc:  # noqa: BLE001 — this is the boundary; everything stops here
        return report_crash(exc, argv[0] if argv else "", version)


def report_crash(exc: BaseException, command: str, version: str, stream: Any = None) -> int:
    """Print the crash report and return the exit code to use.

    Separate from `guard` so the INTERACTIVE SHELL can use it too. The shell must not exit on a
    crash — the session outlives any one command — but a developer there deserves the same
    report as on the command line, rather than the bare `IndexError: list index out of range`
    it used to print. One wording, one crash file, both entry points.

    `stream` exists for that second caller. The shell writes everything through its own stream
    so the session's output stays in one place (and stays capturable); sending only the crash
    to stderr would make it the one thing a developer could not see in context, or scroll back
    to, or pipe to a file with the rest of the session.
    """
    out = stream if stream is not None else sys.stderr
    if os.environ.get("PYYOL_DEBUG"):
        # An explicit request for the raw fault. Printed BEFORE the summary so the summary
        # stays the last thing on screen.
        traceback.print_exception(type(exc), exc, exc.__traceback__)
    report = _write_report(exc, [command] if command else [], version)
    print(file=out)
    print(f"✗ pyyol hit an internal error while running `{command}`.", file=out)
    print(f"  {_one_line(exc)}", file=out)
    print(file=out)
    # Said plainly, because the default assumption is the opposite. A developer who thinks
    # they broke it goes hunting through their own agent code for a fault that is ours.
    print("  This is a bug in pyyol, not in your agent.", file=out)
    if report is not None:
        print(f"  Full details: {report}", file=out)
    print(f"  Report it: {_ISSUES_URL}", file=out)
    if not os.environ.get("PYYOL_DEBUG"):
        print("  Re-run with PYYOL_DEBUG=1 to print the full traceback here.", file=out)
    if version:
        print(f"  pyyol {version} · python {sys.version.split()[0]} · {sys.platform}", file=out)
    return EXIT_INTERNAL
