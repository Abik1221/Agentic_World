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
import os
import sys
import time
import urllib.error
import urllib.parse
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
    from . import credentials

    creds = credentials.load()
    api = (args.api or (creds.url if creds else "")).rstrip("/")
    agent = args.agent or (creds.agent_id if creds else "")
    token = args.token or (creds.access_token if creds else "")
    if not (api and agent and token):
        print(f"{BAD} need --api, --agent and --token (or `onavion login` first)", file=sys.stderr)
        return 2
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


# --- login / logout ------------------------------------------------------------

def cmd_login(args: argparse.Namespace) -> int:
    from . import credentials, login

    # Explicit token paste (headless/CI fallback) — normal onboarding uses the browser.
    if args.token:
        creds = credentials.Credentials(
            url=args.api, connect_url=args.connect or login.derive_connect_url(args.api),
            agent_id=args.agent, access_token=args.token)
        backend = credentials.save(creds)
        print(f"{OK} stored credentials ({backend})")
        return 0

    dashboard = args.dashboard or args.api
    if not dashboard:
        print(f"{BAD} pass --dashboard (or --api), or --token for headless login", file=sys.stderr)
        return 2
    print(f"opening {dashboard}/cli-login in your browser…")
    try:
        creds = login.run_login_flow(dashboard, api_url=args.api)
    except Exception as e:  # noqa: BLE001
        print(f"{BAD} login failed: {e}", file=sys.stderr)
        return 1
    if args.connect:
        creds.connect_url = args.connect
    backend = credentials.save(creds)
    who = creds.agent_id or "(no agent yet)"
    print(f"{OK} logged in as {who} — credentials stored ({backend})")
    return 0


def cmd_logout(_args: argparse.Namespace) -> int:
    from . import credentials

    print(f"{OK} logged out" if credentials.clear() else "not logged in")
    return 0


# --- status / logs -------------------------------------------------------------

def cmd_status(args: argparse.Namespace) -> int:
    from . import credentials

    creds = credentials.load()
    if creds is None or not creds.access_token:
        print(f"{BAD} not logged in — run `onavion login` first", file=sys.stderr)
        return 2
    api = (args.api or creds.url).rstrip("/")
    agent_id = args.agent or creds.agent_id
    if not api or not agent_id:
        print(f"{BAD} need an API url and agent id (login or pass --api/--agent)", file=sys.stderr)
        return 2
    req = urllib.request.Request(
        f"{api}/v1/agent/status?agent_id={urllib.parse.quote(agent_id)}",
        headers={"Authorization": "Bearer " + creds.access_token})
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            body = json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as e:
        print(f"{BAD} status failed ({e.code}): {e.read().decode(errors='replace')}", file=sys.stderr)
        return 1
    except Exception as e:  # noqa: BLE001
        print(f"{BAD} status failed: {e}", file=sys.stderr)
        return 1
    online = body.get("online")
    dot = "🟢 Online" if online else "⚪ Offline"
    print(f"Agent {agent_id}\n  {dot}")
    if online:
        print(f"  SDK       {body.get('sdk_version', '?')}")
        print(f"  Games     {', '.join(body.get('games', []) or [])}")
        print(f"  Last seen {body.get('last_seen', '?')}")
    return 0


def cmd_logs(args: argparse.Namespace) -> int:
    from . import credentials

    path = args.file or os.path.join(credentials.config_dir(), "logs", "agent.log")
    if not os.path.exists(path):
        print(f"no logs yet at {path} (run `onavion run` to generate them)")
        return 0
    with open(path) as f:
        lines = f.readlines()
    for line in lines[-args.n:]:
        sys.stdout.write(line)
    return 0


# --- play / watch (start a match, spectate — the AGENT plays, never the human) --

# Which endpoint starts a self-driving match per game. The developer's connected
# agent (onavion run) plays it; this only *starts* it. There is deliberately no
# move-input path anywhere in the CLI — a human never plays for the agent.
_PLAY_PATH = {
    "goofspiel": "/v1/sandbox/pushplay",
    "mafia": "/v1/mafia/pushplay",
    "monopoly": "/v1/monopoly/pushplay",
}

# SSE event types that end a match, so `watch` can return control.
_TERMINAL_EVENTS = {"match_finished", "victory", "game_over", "game_finished", "finished"}


def _http_base(args: argparse.Namespace, creds) -> str:
    """Resolve the HTTP API base for /v1/... calls: --api, ONAVION_API, the
    logged-in api url, else derived from the WSS connect url (ws→http)."""
    if getattr(args, "api", ""):
        return args.api.rstrip("/")
    if os.environ.get("ONAVION_API"):
        return os.environ["ONAVION_API"].rstrip("/")
    if creds and creds.url:
        return creds.url.rstrip("/")
    if creds and creds.connect_url:
        u = urllib.parse.urlsplit(creds.connect_url)
        scheme = "https" if u.scheme in ("wss", "https") else "http"
        return urllib.parse.urlunsplit((scheme, u.netloc, "", "", ""))
    return ""


def cmd_play(args: argparse.Namespace) -> int:
    """Start a self-driving match. Your connected agent (onavion run) plays it —
    this command only kicks it off, then optionally spectates."""
    from . import credentials

    creds = credentials.load()
    token = args.token or (creds.access_token if creds else "") or os.environ.get("ONAVION_TOKEN", "")
    if not token:
        print(f"{BAD} not logged in — run `onavion login` first", file=sys.stderr)
        return 2
    base = _http_base(args, creds)
    if not base:
        print(f"{BAD} no API url — pass --api or run `onavion login`", file=sys.stderr)
        return 2

    body = {}
    if args.game == "goofspiel" and args.difficulty:
        body["difficulty"] = args.difficulty
    if args.game == "monopoly" and args.players:
        body["players"] = args.players

    st, resp = _api_post(f"{base}{_PLAY_PATH[args.game]}", token, body)
    if st not in (200, 201):
        code = resp.get("code") or resp.get("error") or ""
        if "transport" in str(code) or "no_agent" in str(code):
            print(f"{BAD} your agent isn't connected. In another terminal run `onavion run`, then retry.", file=sys.stderr)
        else:
            print(f"{BAD} could not start match ({st}): {resp}", file=sys.stderr)
        return 1
    match_id = resp.get("match_id") or resp.get("MatchID") or resp.get("id") or ""
    print(f"{OK} match started: {match_id}  ({args.game}) — your agent is playing it.")
    if args.watch and match_id:
        print("  spectating (read-only) — Ctrl-C to stop\n")
        return _watch(base, match_id, args)
    print(f"    watch it:  onavion watch {match_id}")
    return 0


def cmd_watch(args: argparse.Namespace) -> int:
    """Spectate a live match in the terminal — READ-ONLY. Renders the event
    stream; there is no way to influence the game from here."""
    from . import credentials

    creds = credentials.load()
    base = _http_base(args, creds)
    if not base:
        print(f"{BAD} no API url — pass --api or run `onavion login`", file=sys.stderr)
        return 2
    if not args.match:
        print(f"{BAD} usage: onavion watch <match_id>", file=sys.stderr)
        return 2
    return _watch(base, args.match, args)


def _watch(base: str, match_id: str, args: argparse.Namespace) -> int:
    from .console import build_console

    console = build_console(mode="json" if getattr(args, "json", False) else "pretty",
                            color=False if getattr(args, "no_color", False) else None)
    url = f"{base}/v1/match/{urllib.parse.quote(match_id)}/watch"
    req = urllib.request.Request(url, headers={"Accept": "text/event-stream"})
    console.emit("match", f"spectating {match_id} (read-only)")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            _render_sse(resp, console)
    except KeyboardInterrupt:
        print("\nstopped watching.")
    except urllib.error.HTTPError as e:
        print(f"{BAD} watch failed ({e.code}): {e.read().decode(errors='replace')}", file=sys.stderr)
        return 1
    except Exception as e:  # noqa: BLE001
        print(f"{BAD} watch failed: {e}", file=sys.stderr)
        return 1
    return 0


def _render_sse(lines, console) -> None:
    """Parse a text/event-stream and render each frame via the console (read-only).
    Returns when the match reaches a terminal event or the stream closes."""
    event, data = None, []
    for raw in lines:
        line = raw.decode("utf-8", "replace") if isinstance(raw, (bytes, bytearray)) else raw
        line = line.rstrip("\r\n")
        if line == "":  # frame boundary
            if data:
                payload = "\n".join(data)
                try:
                    obj = json.loads(payload)
                except json.JSONDecodeError:
                    obj = {"raw": payload}
                kind = event or "event"
                console.emit(kind, _sse_summary(obj), seq=obj.get("seq"))
                if kind in _TERMINAL_EVENTS:
                    console.emit("game_end", "match finished")
                    return
            event, data = None, []
            continue
        if line.startswith(":"):  # keepalive comment
            continue
        field, _, value = line.partition(":")
        value = value.lstrip()
        if field == "event":
            event = value
        elif field == "data":
            data.append(value)
        # `id:` is the resume cursor; not needed for display


def _sse_summary(obj) -> str:
    """A short, human line for a spectator event payload."""
    if not isinstance(obj, dict):
        return str(obj)
    for k in ("winner", "text", "action", "card", "phase", "message"):
        if obj.get(k) not in (None, ""):
            return f"{k}: {obj[k]}"
    return json.dumps(obj, separators=(",", ":"))[:70]


def _api_post(url: str, token: str, body):
    data = json.dumps(body).encode() if body else b""
    req = urllib.request.Request(url, data=data, method="POST",
                                 headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            return e.code, (json.loads(raw) if raw else {})
        except json.JSONDecodeError:
            return e.code, {"raw": raw.decode(errors="replace")}


# --- run (connect the local agent over WSS) ------------------------------------

def _log_file_handler():
    """Attach a file handler so `onavion logs` has content. Propagation is off so
    the file is the only logger sink — the live feed is the Console (stdout)."""
    import logging

    from . import credentials

    d = os.path.join(credentials.config_dir(), "logs")
    os.makedirs(d, exist_ok=True)
    handler = logging.FileHandler(os.path.join(d, "agent.log"))
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    logger = logging.getLogger("onavion")
    logger.setLevel(logging.INFO)
    logger.propagate = False
    logger.addHandler(handler)


def cmd_run(args: argparse.Namespace) -> int:
    """Load the developer's agent object and connect it to the platform over the
    outbound WebSocket. This is the local-runtime path: no inbound endpoint."""
    import importlib.util

    from . import credentials

    creds = credentials.load()
    url = args.url or os.environ.get("ONAVION_URL", "") or (creds.connect_url if creds else "")
    if not url:
        print(f"{BAD} no platform URL — pass --url, set ONAVION_URL, or run `onavion login`", file=sys.stderr)
        return 2
    agent_id = args.agent or os.environ.get("ONAVION_AGENT_ID", "") or (creds.agent_id if creds else "")
    token = args.token or os.environ.get("ONAVION_TOKEN", "") or (creds.access_token if creds else "")
    _log_file_handler()

    # Load the agent module and find the `Agent` instance (var name configurable).
    spec = importlib.util.spec_from_file_location("_onavion_user_agent", args.file)
    if spec is None or spec.loader is None:
        print(f"{BAD} cannot load {args.file}", file=sys.stderr)
        return 2
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    agent = getattr(mod, args.var, None)
    if agent is None:
        print(f"{BAD} no `{args.var}` found in {args.file} (expose your Agent as `{args.var}`)", file=sys.stderr)
        return 2

    from .console import build_console

    console = build_console(
        mode="json" if args.json else "pretty",
        quiet=args.quiet,
        color=False if args.no_color else None,
    )
    try:
        agent.run(url=url, agent_id=agent_id, token=token, console=console)
    except KeyboardInterrupt:
        print("\nstopped.")
    return 0


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

    pl = sub.add_parser("login", help="log in via the browser and store credentials")
    pl.add_argument("--dashboard", default="", help="dashboard base URL (opens {dashboard}/cli-login)")
    pl.add_argument("--api", default="", help="platform API base URL to record")
    pl.add_argument("--connect", default="", help="override the WSS connect URL")
    pl.add_argument("--agent", default="", help="agent public id (if known)")
    pl.add_argument("--token", default="", help="paste a token directly (headless/CI fallback)")
    pl.set_defaults(func=cmd_login)

    plo = sub.add_parser("logout", help="remove stored credentials")
    plo.set_defaults(func=cmd_logout)

    pst = sub.add_parser("status", help="show whether your agent is connected")
    pst.add_argument("--api", default="", help="platform API base (defaults to the logged-in one)")
    pst.add_argument("--agent", default="", help="agent public id (defaults to the logged-in one)")
    pst.set_defaults(func=cmd_status)

    plg = sub.add_parser("logs", help="show recent local agent logs")
    plg.add_argument("--file", default="", help="log file path (defaults to ~/.onavion/logs/agent.log)")
    plg.add_argument("-n", type=int, default=50, help="number of trailing lines")
    plg.set_defaults(func=cmd_logs)

    pr = sub.add_parser("run", help="connect your local agent to the platform over WSS")
    pr.add_argument("--file", default="agent.py", help="path to your agent module")
    pr.add_argument("--var", default="agent", help="the Agent variable name in that module")
    pr.add_argument("--url", default="", help="platform connect URL (or ONAVION_URL)")
    pr.add_argument("--agent", default="", help="agent public id (or ONAVION_AGENT_ID)")
    pr.add_argument("--token", default="", help="access token (or ONAVION_TOKEN)")
    pr.add_argument("--json", action="store_true", help="emit one JSON object per line (for piping)")
    pr.add_argument("--quiet", action="store_true", help="only milestones (connect / match / result)")
    pr.add_argument("--no-color", action="store_true", help="disable ANSI color")
    pr.set_defaults(func=cmd_run)

    ppl = sub.add_parser("play", help="start a self-driving match (your agent plays it)")
    ppl.add_argument("game", choices=["goofspiel", "mafia", "monopoly"])
    ppl.add_argument("--api", default="", help="platform API base (defaults to the logged-in one)")
    ppl.add_argument("--token", default="", help="agent token (defaults to the logged-in one)")
    ppl.add_argument("--difficulty", default="", help="goofspiel house difficulty (optional)")
    ppl.add_argument("--players", type=int, default=0, help="monopoly player count (optional)")
    ppl.add_argument("--watch", action="store_true", help="spectate the match after starting it")
    ppl.add_argument("--json", action="store_true", help="JSON event lines when spectating")
    ppl.add_argument("--no-color", action="store_true")
    ppl.set_defaults(func=cmd_play)

    pw = sub.add_parser("watch", help="spectate a live match in the terminal (read-only)")
    pw.add_argument("match", help="match id (from `onavion play` or the dashboard)")
    pw.add_argument("--api", default="", help="platform API base (defaults to the logged-in one)")
    pw.add_argument("--json", action="store_true", help="emit one JSON object per line")
    pw.add_argument("--no-color", action="store_true")
    pw.set_defaults(func=cmd_watch)

    pp = sub.add_parser("publish", help="submit + verify a manifest via the platform API")
    pp.add_argument("--api", default="", help="platform API base, e.g. https://host/api (or from login)")
    pp.add_argument("--agent", default="", help="agent public id (ag_…) (or from login)")
    pp.add_argument("--token", default="", help="dashboard JWT / access token (or from login)")
    pp.add_argument("--manifest", required=True, help="path to manifest.json")
    pp.add_argument("--secret", default="", help="endpoint secret to store before verify")
    pp.set_defaults(func=cmd_publish)

    return p


def main(argv: Optional[List[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
