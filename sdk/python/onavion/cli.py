"""The ``onavion`` CLI — scaffold, validate, simulate, and publish agents.

    onavion init my-agent --lang python      # scaffold a runnable starter
    onavion validate --url http://localhost:9099/turn --secret S
    onavion simulate --url http://localhost:9099/turn --secret S --game goofspiel
    onavion publish  --api https://.../api --agent ag_… --token <dash-jwt> \\
                     --manifest manifest.json --secret S

`init`, `validate`, and `simulate` are fully local (no platform needed) and are
the "under 30 minutes to a live game" path. `publish` drives the real manifest
API (submit -> set endpoint secret -> verify).
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
from typing import Any, Dict, List, Optional

from . import __version__
from .signing import (
    REQUEST_ID_HEADER,
    SIGNATURE_HEADER,
    SIGNATURE_VERSION,
    TIMESTAMP_HEADER,
    compute_signature,
)

OK = "✓"
BAD = "✗"


# --- HTTP helper (signed, stdlib) ---------------------------------------------

def _rfc3339() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def _request(url: str, method: str, secret: str, payload: Optional[Dict[str, Any]],
             sign_path: Optional[str] = None) -> tuple[int, Dict[str, Any]]:
    """Send a (optionally signed) request. ``sign_path`` is the path the signature
    binds; defaults to the URL's path."""
    from urllib.parse import urlsplit

    body = json.dumps(payload).encode() if payload is not None else b""
    path = sign_path if sign_path is not None else (urlsplit(url).path or "/")
    headers: Dict[str, str] = {}
    if payload is not None:
        headers["Content-Type"] = "application/json"
    if secret and payload is not None:
        nonce = f"cli_{int(time.time() * 1000)}"
        ts = _rfc3339()
        headers[TIMESTAMP_HEADER] = ts
        headers[REQUEST_ID_HEADER] = nonce
        headers[SIGNATURE_HEADER] = f"{SIGNATURE_VERSION}={compute_signature(secret, ts, nonce, method, path, body)}"
        headers["Authorization"] = "Bearer " + secret
    req = urllib.request.Request(url, data=body or None, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, json.loads(raw) if raw else {}
        except json.JSONDecodeError:
            return e.code, {"raw": raw.decode(errors="replace")}


def _sibling(url: str, name: str) -> str:
    """Derive {dir(url)}/name — matches the platform's sibling routing."""
    base, _, _ = url.rstrip("/").rpartition("/")
    return f"{base}/{name}"


# --- validate ------------------------------------------------------------------

def cmd_validate(args: argparse.Namespace) -> int:
    url, secret = args.url, args.secret or ""
    checks: List[tuple[str, bool, str]] = []

    # 1. health (unsigned GET on the sibling)
    try:
        st, body = _request(_sibling(url, "health"), "GET", "", None)
        healthy = st == 200 and str(body.get("status", "")).lower() == "healthy"
        checks.append(("health", healthy, f"{st} {body.get('status', '')}"))
    except Exception as e:  # noqa: BLE001
        checks.append(("health", False, str(e)))

    # 2. handshake (signed POST)
    try:
        st, body = _request(_sibling(url, "handshake"), "POST", secret,
                            {"platform": "agent-arena", "protocol": "1.0"})
        acc = st == 200 and bool(body.get("accepted"))
        games = ",".join(body.get("supportedGames", []) or [])
        checks.append(("handshake", acc, f"{st} accepted={body.get('accepted')} games=[{games}]"))
    except Exception as e:  # noqa: BLE001
        checks.append(("handshake", False, str(e)))

    # 3. a real signed turn — the platform's core call. Uses a synthetic view.
    view, legal, pick = _synthetic_turn(args.game)
    try:
        st, move = _request(url, "POST", secret, view)
        legal_move = st == 200 and pick(move)
        checks.append(("turn", legal_move, f"{st} -> {json.dumps(move)}"))
    except Exception as e:  # noqa: BLE001
        checks.append(("turn", False, str(e)))

    # 4. lifecycle notifications must ack 200.
    for name, payload in _lifecycle_probes(args.game):
        try:
            st, _ = _request(_sibling(url, name), "POST", secret, payload)
            checks.append((name, st == 200, str(st)))
        except Exception as e:  # noqa: BLE001
            checks.append((name, False, str(e)))

    print(f"onavion validate — {url}\n")
    all_ok = True
    for name, ok, detail in checks:
        mark = OK if ok else BAD
        all_ok = all_ok and ok
        print(f"  {mark} {name:<12} {detail}")
    print("\n" + ("PASS — endpoint speaks the push protocol." if all_ok
                   else "FAIL — fix the checks marked ✗ above."))
    return 0 if all_ok else 1


def _synthetic_turn(game: str):
    """Return (view, legal, is_legal_move_fn) for a probe turn."""
    if game == "monopoly":
        view = {"game": "monopoly", "match_id": "validate", "seat": 0, "phase": "roll",
                "legal_actions": ["roll", "end_turn"], "state": {"players": [], "phase": "roll"}}
        return view, ["roll", "end_turn"], lambda m: m.get("action") in ("roll", "end_turn")
    if game == "mafia":
        view = {"game": "mafia", "match_id": "validate", "your_seat": 1, "your_role": "villager",
                "day": 1, "phase": "day", "alive": {"1": True, "2": True, "3": True},
                "legal": ["vote"], "public": [], "private": []}
        return view, ["vote"], lambda m: m.get("action") == "vote"
    # goofspiel (default)
    view = {"game": "goofspiel", "match_id": "validate", "seat": 0, "round": 0,
            "current_prize": 5, "prize_pool": 5, "your_hand": [1, 2, 3, 4, 5],
            "scores": [0, 0], "legal_actions": [1, 2, 3, 4, 5]}
    return view, [1, 2, 3, 4, 5], lambda m: m.get("card") in [1, 2, 3, 4, 5]


def _lifecycle_probes(game: str):
    return [
        ("initialize", {"protocol": "1.0", "match_id": "validate", "game": game, "seat": 0, "players": 2}),
        ("event", {"protocol": "1.0", "match_id": "validate", "game": game, "seq": 1, "type": "probe"}),
        ("game-end", {"protocol": "1.0", "match_id": "validate", "game": game, "result": {}}),
    ]


# --- simulate (drive a running endpoint over HTTP) -----------------------------

def cmd_simulate(args: argparse.Namespace) -> int:
    if args.game != "goofspiel":
        print(f"simulate currently supports goofspiel (got {args.game!r}); "
              f"use `validate` for a single-turn check of any game.", file=sys.stderr)
        return 2
    url, secret = args.url, args.secret or ""
    hand = args.hand
    prizes = list(range(1, hand + 1))
    dev_hand = list(range(1, hand + 1))
    opp_hand = list(range(1, hand + 1))
    scores = [0, 0]
    carried = 0

    _request(_sibling(url, "initialize"), "POST", secret,
             {"protocol": "1.0", "match_id": "sim", "game": "goofspiel", "seat": 0, "players": 2})
    for rnd, prize in enumerate(prizes):
        pool = prize + carried
        view = {"game": "goofspiel", "match_id": "sim", "seat": 0, "round": rnd,
                "current_prize": prize, "prize_pool": pool, "your_hand": list(dev_hand),
                "scores": list(scores), "legal_actions": list(dev_hand)}
        st, move = _request(url, "POST", secret, view)
        card = move.get("card")
        if st != 200 or card not in dev_hand:
            print(f"round {rnd}: illegal/failed move (status {st}, move {move})", file=sys.stderr)
            return 1
        opp = min(opp_hand, key=lambda c: (abs(c - pool), c))
        dev_hand.remove(card)
        opp_hand.remove(opp)
        if card > opp:
            scores[0] += pool; carried = 0
        elif opp > card:
            scores[1] += pool; carried = 0
        else:
            carried = pool
    winner = "agent" if scores[0] > scores[1] else "baseline" if scores[1] > scores[0] else "tie"
    _request(_sibling(url, "game-end"), "POST", secret,
             {"protocol": "1.0", "match_id": "sim", "game": "goofspiel",
              "result": {"scores": scores}})
    print(f"simulate goofspiel ({hand} rounds): winner={winner} scores agent={scores[0]} baseline={scores[1]}")
    return 0


# --- publish (submit -> set secret -> verify) ----------------------------------

def cmd_publish(args: argparse.Namespace) -> int:
    api = args.api.rstrip("/")
    agent, token = args.agent, args.token
    with open(args.manifest, "rb") as f:
        manifest = f.read()

    def api_req(method: str, path: str, body: Optional[bytes], ctype: str = "application/json"):
        req = urllib.request.Request(api + path, data=body, method=method,
                                     headers={"Authorization": "Bearer " + token, "Content-Type": ctype})
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                raw = resp.read()
                return resp.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            raw = e.read()
            return e.code, (json.loads(raw) if raw else {"raw": raw.decode(errors="replace")})

    st, m = api_req("POST", f"/v1/agents/{agent}/manifest", manifest)
    if st != 201:
        print(f"{BAD} submit failed ({st}): {m}", file=sys.stderr)
        return 1
    mid = m.get("manifest_id")
    print(f"{OK} manifest submitted: {mid}")

    if args.secret:
        st, r = api_req("PUT", f"/v1/agents/{agent}/manifest/{mid}/endpoint-secret",
                        json.dumps({"token": args.secret}).encode())
        if st != 200:
            print(f"{BAD} set endpoint secret failed ({st}): {r}", file=sys.stderr)
            return 1
        print(f"{OK} endpoint secret stored")

    st, report = api_req("POST", f"/v1/agents/{agent}/manifest/{mid}/verify", b"")
    verified = st == 200 and (report.get("verified") or report.get("status") == "verified")
    mark = OK if verified else BAD
    print(f"{mark} verify ({st}): {json.dumps(report)}")
    return 0 if verified else 1


# --- init (scaffold) -----------------------------------------------------------

_PY_STARTER = '''\
import os
from onavion import Agent
from onavion.models import GoofspielView, GoofspielMove

agent = Agent(secret=os.environ.get("ONAVION_SECRET", ""), supported_games=["goofspiel"], name="{name}")

@agent.on_turn("goofspiel")
def decide(view: GoofspielView) -> GoofspielMove:
    # TODO: your strategy here. Baseline: spend the smallest card.
    return GoofspielMove(card=min(view.legal_actions), round=view.round)

if __name__ == "__main__":
    agent.serve(port=int(os.environ.get("PORT", "9099")))
'''

_JS_STARTER = '''\
import {{ Agent }} from "onavion";

const agent = new Agent({{ secret: process.env.ONAVION_SECRET, supportedGames: ["goofspiel"], name: "{name}" }});

agent.onTurn("goofspiel", (v) => ({{ round: v.round, card: Math.min(...v.legal_actions) }}));

agent.serve(Number(process.env.PORT ?? 9099));
'''

# Matches the platform manifest schema (schema.go): manifestVersion "1.0", a
# nested `agent` block, endpoint.authentication == "bearer-token", camelCase keys.
_MANIFEST_TMPL = {
    "manifestVersion": "1.0",
    "agent": {
        "name": "",
        "description": "A push-protocol agent.",
        "version": "0.1.0",
        "visibility": "private",
    },
    "developer": {"name": "you", "organization": ""},
    "games": ["goofspiel"],
    "endpoint": {"url": "https://your-host.example.com/turn", "authentication": "bearer-token"},
    "runtime": {"timeout": 5000, "maxMemory": "256Mi"},
    "sdk": {"language": "python", "version": __version__},
    "contact": {"email": "you@example.com"},
}


def cmd_init(args: argparse.Namespace) -> int:
    import os

    d = args.dir
    os.makedirs(d, exist_ok=True)
    name = args.name or os.path.basename(os.path.abspath(d))
    if args.lang == "js":
        path = os.path.join(d, "agent.mjs")
        code = _JS_STARTER.format(name=name)
        lang = "js"
    else:
        path = os.path.join(d, "agent.py")
        code = _PY_STARTER.format(name=name)
        lang = "python"
    with open(path, "w") as f:
        f.write(code)

    import copy

    manifest = copy.deepcopy(_MANIFEST_TMPL)
    manifest["agent"]["name"] = name
    manifest["sdk"] = {"language": lang, "version": __version__}
    mpath = os.path.join(d, "manifest.json")
    with open(mpath, "w") as f:
        json.dump(manifest, f, indent=2)

    print(f"{OK} scaffolded {lang} agent in {d}/")
    print(f"    {path}")
    print(f"    {mpath}")
    print("\nNext:")
    if lang == "python":
        print(f"    pip install onavion && python {path}")
    else:
        print(f"    npm install onavion && node {path}")
    print(f"    onavion validate --url http://localhost:9099/turn --secret <your-secret>")
    return 0


# --- entry point ---------------------------------------------------------------

def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="onavion", description="Agent Arena developer CLI (Beta)")
    p.add_argument("--version", action="version", version=f"onavion {__version__}")
    sub = p.add_subparsers(dest="command", required=True)

    pi = sub.add_parser("init", help="scaffold a starter agent + manifest")
    pi.add_argument("dir")
    pi.add_argument("--lang", choices=["python", "js"], default="python")
    pi.add_argument("--name", default="")
    pi.set_defaults(func=cmd_init)

    pv = sub.add_parser("validate", help="probe a running endpoint like the platform does")
    pv.add_argument("--url", required=True, help="the /turn endpoint URL")
    pv.add_argument("--secret", default="")
    pv.add_argument("--game", choices=["goofspiel", "monopoly", "mafia"], default="goofspiel")
    pv.set_defaults(func=cmd_validate)

    ps = sub.add_parser("simulate", help="drive a full local match against a running endpoint")
    ps.add_argument("--url", required=True, help="the /turn endpoint URL")
    ps.add_argument("--secret", default="")
    ps.add_argument("--game", choices=["goofspiel"], default="goofspiel")
    ps.add_argument("--hand", type=int, default=13)
    ps.set_defaults(func=cmd_simulate)

    pp = sub.add_parser("publish", help="submit + verify a manifest via the platform API")
    pp.add_argument("--api", required=True, help="platform API base, e.g. https://host/api")
    pp.add_argument("--agent", required=True, help="agent public id (ag_…)")
    pp.add_argument("--token", required=True, help="dashboard JWT (user scope)")
    pp.add_argument("--manifest", required=True, help="path to manifest.json")
    pp.add_argument("--secret", default="", help="endpoint secret to store before verify")
    pp.set_defaults(func=cmd_publish)

    return p


def main(argv: Optional[List[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
