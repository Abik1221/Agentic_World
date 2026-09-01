#!/usr/bin/env python3
"""Render REAL captured terminal output into framed SVG "screenshots" for the docs.

    python sdk/docs/gen_shots.py

# Why generated, and why from real bytes

A hand-drawn mockup of a terminal drifts from the tool the moment either changes, and the
reader cannot tell. These are produced by running the actual CLI under a PTY (the shell is
TTY-gated, so a pipe would capture the fallback rather than the thing being documented) and
converting the ANSI it emits. If the banner changes, the picture changes with it.

# Why SVG rather than PNG

No screen and no image toolchain is needed to produce one, it scales without blurring on a
retina display, the text stays selectable and searchable, and it diffs as text in review —
a PNG in a pull request is an opaque blob nobody can check.
"""

from __future__ import annotations

import pathlib
import re
import sys

# xterm-256 → hex, for the slice this CLI actually uses (the brand ramp plus basic ANSI).
BASIC = {
    30: "#3b4048", 31: "#e06c75", 32: "#98c379", 33: "#e5c07b",
    34: "#61afef", 35: "#c678dd", 36: "#56b6c2", 37: "#dcdfe4",
}
FG = "#d7dae0"
DIM = "#6b7280"
BG = "#12141a"


def _cube(n: int) -> str:
    """xterm-256 colour n → hex, for the 16-231 cube and the 232-255 greys."""
    if n < 16:
        return BASIC.get(30 + (n % 8), FG)
    if n < 232:
        n -= 16
        r, g, b = n // 36, (n % 36) // 6, n % 6
        f = lambda v: 0 if v == 0 else 55 + 40 * v  # noqa: E731 - tiny local table lookup
        return f"#{f(r):02x}{f(g):02x}{f(b):02x}"
    v = 8 + (n - 232) * 10
    return f"#{v:02x}{v:02x}{v:02x}"


SGR = re.compile(r"\x1b\[([0-9;]*)m")
OTHER_CSI = re.compile(r"\x1b\[[0-9;]*[A-Za-z]")


def parse(raw: str) -> list[list[tuple[str, str, bool]]]:
    """ANSI → rows of (text, colour, bold) spans. Unsupported CSI is dropped, not printed."""
    rows: list[list[tuple[str, str, bool]]] = []
    colour, bold = FG, False
    for line in raw.replace("\r\n", "\n").replace("\r", "").split("\n"):
        spans: list[tuple[str, str, bool]] = []
        pos = 0
        for m in SGR.finditer(line):
            text = line[pos : m.start()]
            if text:
                spans.append((text, colour, bold))
            pos = m.end()
            for code in (m.group(1) or "0").split(";"):
                if code in ("", "0"):
                    colour, bold = FG, False
                elif code == "1":
                    bold = True
                elif code == "2":
                    colour = DIM
                elif code == "38":
                    pass  # handled by the 5;N pair below
                elif code.isdigit() and 30 <= int(code) <= 37:
                    colour = BASIC[int(code)]
            m38 = re.match(r"38;5;(\d+)", m.group(1) or "")
            if m38:
                colour = _cube(int(m38.group(1)))
        tail = line[pos:]
        if tail:
            spans.append((tail, colour, bold))
        rows.append([(OTHER_CSI.sub("", t), c, b) for t, c, b in spans])
    return rows


def esc(s: str) -> str:
    return s.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")


def svg(rows, title: str) -> str:
    cw, lh, pad, top = 8.4, 19.0, 22, 44
    width = 84 * cw + pad * 2
    height = top + len(rows) * lh + pad
    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width:.0f}" height="{height:.0f}" '
        f'viewBox="0 0 {width:.0f} {height:.0f}" role="img" aria-label="{esc(title)}">',
        f'<rect width="{width:.0f}" height="{height:.0f}" rx="10" fill="{BG}"/>',
        f'<rect width="{width:.0f}" height="{top - 12:.0f}" rx="10" fill="#191c23"/>',
        '<circle cx="20" cy="16" r="5.5" fill="#ff5f57"/>',
        '<circle cx="38" cy="16" r="5.5" fill="#febc2e"/>',
        '<circle cx="56" cy="16" r="5.5" fill="#28c840"/>',
        f'<text x="{width/2:.0f}" y="20" fill="#7c8494" font-family="ui-monospace,SFMono-Regular,Menlo,monospace" '
        f'font-size="11" text-anchor="middle">{esc(title)}</text>',
        '<g font-family="ui-monospace,SFMono-Regular,Menlo,Consolas,monospace" font-size="13">',
    ]
    for i, spans in enumerate(rows):
        y = top + i * lh
        x = pad
        for text, colour, bold in spans:
            if not text:
                continue
            weight = ' font-weight="600"' if bold else ""
            out.append(
                f'<text x="{x:.1f}" y="{y:.1f}" fill="{colour}"{weight} '
                f'xml:space="preserve">{esc(text)}</text>'
            )
            x += len(text) * cw
    out.append("</g></svg>")
    return "\n".join(out)


def main() -> int:
    here = pathlib.Path(__file__).resolve().parent
    shots = {
        "cli-home.svg": ("/tmp/shot_home.raw", "pyyol — the home screen"),
        "cli-menu.svg": ("/tmp/shot_menu.raw", "pyyol — the / command menu"),
    }
    for name, (src, title) in shots.items():
        p = pathlib.Path(src)
        if not p.exists():
            print(f"missing capture {src}; skipping {name}", file=sys.stderr)
            continue
        raw = p.read_text(encoding="utf-8", errors="replace")
        rows = [r for r in parse(raw) if any(t.strip() for t, _, _ in r)] or parse(raw)
        (here / "assets" / name).write_text(svg(rows, title), encoding="utf-8")
        print("wrote", here / "assets" / name)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
