"""Money-safety mode model — the one invariant that prevents accidental losses.

Two modes:

* ``sandbox`` (default, SAFE): practice / open-lobby play. No stakes, no
  certification needed. ``pyyol dev`` is hard-locked to this.
* ``ranked`` (real stakes): escrow + Elo + P-Index. Reachable ONLY via an explicit
  ``pyyol play <arena> --ranked``, gated on a certified agent, shown with a loud red
  banner, and confirmed once. You can never enter ranked by accident.

Precedence for ``play`` (highest wins): ``--ranked`` flag → ``PYYOL_MODE=ranked``
env → ``pyyol.toml [mode]`` → default ``sandbox``. This mirrors Stripe's test-vs-live
key model: safe by default, live only on a deliberate choice.
"""

from __future__ import annotations

import os
import sys

SANDBOX = "sandbox"
RANKED = "ranked"

_GREEN = "\x1b[32m"
_RED = "\x1b[1;31m"
_DIM = "\x1b[2m"
_RESET = "\x1b[0m"


def _use_color(stream=None) -> bool:
    if os.environ.get("NO_COLOR"):
        return False
    stream = stream or sys.stdout
    return bool(getattr(stream, "isatty", lambda: False)())


def resolve(*, ranked_flag: bool = False, cfg_mode: str = "", dev_locked: bool = False) -> str:
    """Resolve the effective mode. ``dev_locked`` (``pyyol dev``) forces sandbox."""
    if dev_locked:
        return SANDBOX
    if ranked_flag:
        return RANKED
    env = os.environ.get("PYYOL_MODE", "").strip().lower()
    if env in (SANDBOX, RANKED):
        return env
    if cfg_mode in (SANDBOX, RANKED):
        return cfg_mode
    return SANDBOX


def banner(mode: str, *, color: bool | None = None) -> str:
    """A one-line mode banner printed before every dev/play run."""
    use = _use_color() if color is None else color
    if mode == RANKED:
        text = "⚠  RANKED — real stakes (escrow · Elo · P-Index)"
        return f"{_RED}{text}{_RESET}" if use else text
    text = "●  SANDBOX — practice, no stakes"
    return f"{_GREEN}{text}{_RESET}" if use else text


def confirm_ranked(*, assume_yes: bool = False, stream=None) -> bool:
    """One-time confirmation before real-stakes play. ``--yes``/non-TTY CI skips the
    prompt only when ``assume_yes`` is set; otherwise a non-interactive session
    refuses (fail-safe: never auto-enter ranked without an explicit opt-in)."""
    if assume_yes:
        return True
    stream = stream or sys.stdin
    if not getattr(stream, "isatty", lambda: False)():
        # Non-interactive and no --yes: do NOT silently enter real-stakes play.
        return False
    reply = input("This plays with REAL stakes. Continue? [y/N] ").strip().lower()
    return reply in ("y", "yes")
