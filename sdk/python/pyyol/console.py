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
import sys
import time
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


def build_console(
    mode: str = "pretty", quiet: bool = False, color: bool | None = None
) -> Console:
    """Factory used by the CLI: mode is ``pretty`` | ``json``."""
    if mode == "json":
        return JsonConsole()
    return PrettyConsole(color=color, quiet=quiet)
