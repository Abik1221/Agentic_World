"""The ``pyyol`` CLI — scaffold, validate, simulate, and publish agents.

    pyyol init my-agent --lang python      # scaffold a runnable starter
    pyyol validate --url http://localhost:9099/turn --secret S
    pyyol simulate --url http://localhost:9099/turn --secret S --game goofspiel
    pyyol publish  --api https://.../api --agent ag_… --token <dash-jwt> \\
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
import urllib.parse  # cheap; used for quoting/URL parsing everywhere
from typing import Any, Dict, List, Optional

from . import __version__

# NOTE: `urllib.request`/`urllib.error` (which pull in `ssl`, `http.client`, `email`
# ≈40ms) and `.signing` are imported LAZILY inside the functions that make network
# calls, so no-network commands (init / --version / --help / doctor-offline) stay
# instant. Do not add them at module top.

OK = "✓"
BAD = "✗"

# Public platform defaults. A dev who `pip install pyyol` and runs `pyyol login`
# hits the live platform with no flags; self-hosted/local users override via
# PYYOL_API / PYYOL_DASHBOARD env vars (or --api / --dashboard). The API host
# serves the arena /v1/* endpoints; the dashboard host serves the /cli-login page
# — they are DIFFERENT hosts in the split-domain deployment.
DEFAULT_API_BASE = os.environ.get("PYYOL_API", "").rstrip("/") or "https://api.pyyol.com"
DEFAULT_DASHBOARD = os.environ.get("PYYOL_DASHBOARD", "").rstrip("/") or "https://pyyol.com"


# --- HTTP helper (signed, stdlib) ---------------------------------------------


def _rfc3339() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())


def _net_err(e: Exception) -> str:
    """A friendly one-line message for a network failure (offline / refused / timeout /
    DNS) — never a raw traceback. Used by the HTTP helpers so a dead connection reads
    as a clear error, not a Python stack trace."""
    reason = getattr(e, "reason", e)
    text = str(reason).lower()
    if "timed out" in text or "timeout" in text or isinstance(e, TimeoutError):
        return "network timed out (slow or unreachable) — check your connection"
    if "refused" in text:
        return "connection refused — is the platform URL correct and reachable?"
    if "name or service" in text or "nodename" in text or "getaddrinfo" in text:
        return "cannot resolve host — check the URL and your DNS/connection"
    return "network unreachable — check your internet connection"


_insecure_warned = False


def _warn_insecure_transport(url: str, has_auth: bool) -> None:
    """Warn (once) when credentials would be sent over a cleartext, non-loopback
    URL — a token/secret on plain http:// is exposed to any on-path observer.
    Loopback (localhost/127.0.0.1/::1) is exempt (local dev)."""
    if not has_auth or not url:
        return
    try:
        u = urllib.parse.urlsplit(url)
    except ValueError:
        return
    if u.scheme in ("https", "wss"):
        return
    host = (u.hostname or "").lower()
    if host in ("localhost", "127.0.0.1", "::1", "") or host.endswith(".localhost"):
        return
    global _insecure_warned
    if not _insecure_warned:
        _insecure_warned = True
        print(
            f"{BAD} WARNING: sending credentials over insecure {u.scheme}://{host} — use https://",
            file=sys.stderr,
        )


_argv_secret_warned = False


def _warn_argv_secret() -> None:
    """Warn (once) that a secret passed as a CLI flag is visible to other users on a
    shared host (via `ps`/`/proc`). The browser login flow avoids this; prefer env
    (PYYOL_TOKEN) or a PAT scoped to CI."""
    global _argv_secret_warned
    if not _argv_secret_warned:
        _argv_secret_warned = True
        print(
            f"{BAD} note: a secret on the command line is visible to other users on shared "
            f"hosts (ps/proc). Prefer `pyyol login` (browser) or the PYYOL_TOKEN env var.",
            file=sys.stderr,
        )


def _request(
    url: str,
    method: str,
    secret: str,
    payload: Optional[Dict[str, Any]],
    sign_path: Optional[str] = None,
) -> tuple[int, Dict[str, Any]]:
    """Send a (optionally signed) request. ``sign_path`` is the path the signature
    binds; defaults to the URL's path."""
    import urllib.error
    import urllib.request
    from urllib.parse import urlsplit

    from .signing import (
        REQUEST_ID_HEADER,
        SIGNATURE_HEADER,
        SIGNATURE_VERSION,
        TIMESTAMP_HEADER,
        compute_signature,
    )

    _warn_insecure_transport(url, bool(secret))
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
        headers[SIGNATURE_HEADER] = (
            f"{SIGNATURE_VERSION}={compute_signature(secret, ts, nonce, method, path, body)}"
        )
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
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return 0, {"error": _net_err(e)}


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
        st, body = _request(
            _sibling(url, "handshake"),
            "POST",
            secret,
            {"platform": "agent-arena", "protocol": "1.0"},
        )
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

    print(f"pyyol validate — {url}\n")
    all_ok = True
    for name, ok, detail in checks:
        mark = OK if ok else BAD
        all_ok = all_ok and ok
        print(f"  {mark} {name:<12} {detail}")
    print(
        "\n"
        + (
            "PASS — endpoint speaks the push protocol."
            if all_ok
            else "FAIL — fix the checks marked ✗ above."
        )
    )
    return 0 if all_ok else 1


def _synthetic_turn(game: str):
    """Return (view, legal, is_legal_move_fn) for a probe turn."""
    if game == "monopoly":
        view = {
            "game": "monopoly",
            "match_id": "validate",
            "seat": 0,
            "phase": "roll",
            "legal_actions": ["roll", "end_turn"],
            "state": {"players": [], "phase": "roll"},
        }
        return view, ["roll", "end_turn"], lambda m: m.get("action") in ("roll", "end_turn")
    if game == "mafia":
        view = {
            "game": "mafia",
            "match_id": "validate",
            "your_seat": 1,
            "your_role": "Villager",
            "day": 1,
            "phase": "voting",
            "alive": {"1": True, "2": True, "3": True},
            "legal": ["vote"],
            "public": [],
            "private": [],
        }
        return view, ["vote"], lambda m: m.get("action") == "vote"
    # goofspiel (default)
    view = {
        "game": "goofspiel",
        "match_id": "validate",
        "seat": 0,
        "round": 0,
        "current_prize": 5,
        "prize_pool": 5,
        "your_hand": [1, 2, 3, 4, 5],
        "scores": [0, 0],
        "legal_actions": [1, 2, 3, 4, 5],
    }
    return view, [1, 2, 3, 4, 5], lambda m: m.get("card") in [1, 2, 3, 4, 5]


def _lifecycle_probes(game: str):
    return [
        (
            "initialize",
            {"protocol": "1.0", "match_id": "validate", "game": game, "seat": 0, "players": 2},
        ),
        (
            "event",
            {"protocol": "1.0", "match_id": "validate", "game": game, "seq": 1, "type": "probe"},
        ),
        ("game-end", {"protocol": "1.0", "match_id": "validate", "game": game, "result": {}}),
    ]


# --- simulate (drive a running endpoint over HTTP) -----------------------------


def cmd_simulate(args: argparse.Namespace) -> int:
    if args.game != "goofspiel":
        print(
            f"simulate currently supports goofspiel (got {args.game!r}); "
            f"use `validate` for a single-turn check of any game.",
            file=sys.stderr,
        )
        return 2
    url, secret = args.url, args.secret or ""
    hand = args.hand
    prizes = list(range(1, hand + 1))
    dev_hand = list(range(1, hand + 1))
    opp_hand = list(range(1, hand + 1))
    scores = [0, 0]
    carried = 0

    _request(
        _sibling(url, "initialize"),
        "POST",
        secret,
        {"protocol": "1.0", "match_id": "sim", "game": "goofspiel", "seat": 0, "players": 2},
    )
    for rnd, prize in enumerate(prizes):
        pool = prize + carried
        view = {
            "game": "goofspiel",
            "match_id": "sim",
            "seat": 0,
            "round": rnd,
            "current_prize": prize,
            "prize_pool": pool,
            "your_hand": list(dev_hand),
            "scores": list(scores),
            "legal_actions": list(dev_hand),
        }
        st, move = _request(url, "POST", secret, view)
        card = move.get("card")
        if st != 200 or card not in dev_hand:
            print(f"round {rnd}: illegal/failed move (status {st}, move {move})", file=sys.stderr)
            return 1
        opp = min(opp_hand, key=lambda c: (abs(c - pool), c))
        dev_hand.remove(card)
        opp_hand.remove(opp)
        if card > opp:
            scores[0] += pool
            carried = 0
        elif opp > card:
            scores[1] += pool
            carried = 0
        else:
            carried = pool
    winner = "agent" if scores[0] > scores[1] else "baseline" if scores[1] > scores[0] else "tie"
    _request(
        _sibling(url, "game-end"),
        "POST",
        secret,
        {"protocol": "1.0", "match_id": "sim", "game": "goofspiel", "result": {"scores": scores}},
    )
    print(
        f"simulate goofspiel ({hand} rounds): winner={winner} scores agent={scores[0]} baseline={scores[1]}"
    )
    return 0


# --- publish (submit -> set secret -> verify) ----------------------------------


def cmd_publish(args: argparse.Namespace) -> int:
    from . import credentials

    creds = credentials.load()
    api = (args.api or (creds.url if creds else "")).rstrip("/")
    agent = args.agent or (creds.agent_id if creds else "")
    token = args.token or (creds.access_token if creds else "")
    if not (api and agent and token):
        print(f"{BAD} need --api, --agent and --token (or `pyyol login` first)", file=sys.stderr)
        return 2
    if args.token:
        _warn_argv_secret()
    with open(args.manifest, "rb") as f:
        manifest = f.read()

    import urllib.error
    import urllib.request

    _warn_insecure_transport(api, bool(token))
    agent_q = urllib.parse.quote(agent, safe="")  # never interpolate a raw id into the path

    def api_req(method: str, path: str, body: Optional[bytes], ctype: str = "application/json"):
        req = urllib.request.Request(
            api + path,
            data=body,
            method=method,
            headers={"Authorization": "Bearer " + token, "Content-Type": ctype},
        )
        try:
            with urllib.request.urlopen(req, timeout=15) as resp:
                raw = resp.read()
                return resp.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            raw = e.read()
            return e.code, (json.loads(raw) if raw else {"raw": raw.decode(errors="replace")})
        except (urllib.error.URLError, TimeoutError, OSError) as e:
            return 0, {"error": _net_err(e)}

    st, m = api_req("POST", f"/v1/agents/{agent_q}/manifest", manifest)
    if st != 201:
        print(f"{BAD} submit failed ({st}): {m}", file=sys.stderr)
        return 1
    mid = urllib.parse.quote(str(m.get("manifest_id", "")), safe="")
    print(f"{OK} manifest submitted: {m.get('manifest_id')}")

    if args.secret:
        _warn_argv_secret()
        st, r = api_req(
            "PUT",
            f"/v1/agents/{agent_q}/manifest/{mid}/endpoint-secret",
            json.dumps({"token": args.secret}).encode(),
        )
        if st != 200:
            print(f"{BAD} set endpoint secret failed ({st}): {r}", file=sys.stderr)
            return 1
        print(f"{OK} endpoint secret stored")

    st, report = api_req("POST", f"/v1/agents/{agent_q}/manifest/{mid}/verify", b"")
    verified = st == 200 and (report.get("verified") or report.get("status") == "verified")
    mark = OK if verified else BAD
    print(f"{mark} verify ({st}): {json.dumps(report)}")
    return 0 if verified else 1


# --- login / logout ------------------------------------------------------------


def cmd_login(args: argparse.Namespace) -> int:
    from . import credentials, login

    # The API host serves /v1/*; the dashboard host serves /cli-login — different
    # hosts in prod, so the dashboard must NOT fall back to --api (that would open
    # api.pyyol.com/cli-login → 404). Both default to the live platform.
    api = (args.api or DEFAULT_API_BASE).rstrip("/")
    dashboard = (args.dashboard or DEFAULT_DASHBOARD).rstrip("/")

    # Explicit token paste (headless/CI fallback) — normal onboarding uses the browser.
    if args.token:
        _warn_argv_secret()
        creds = credentials.Credentials(
            url=api,
            connect_url=args.connect or login.derive_connect_url(api),
            agent_id=args.agent,
            access_token=args.token,
        )
        backend = credentials.save(creds)
        print(f"{OK} stored credentials ({backend})")
        return 0

    provider = getattr(args, "provider", "")
    via = f" (via {provider})" if provider else ""
    print(f"opening {dashboard}/cli-login in your browser{via}…")
    try:
        creds = login.run_login_flow(dashboard, api_url=api, provider=provider)
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
    import urllib.error
    import urllib.request

    from . import credentials

    creds = credentials.load()
    if creds is None or not creds.access_token:
        print(f"{BAD} not logged in — run `pyyol login` first", file=sys.stderr)
        return 2
    api = (args.api or creds.url).rstrip("/")
    agent_id = args.agent or creds.agent_id
    if not api or not agent_id:
        print(f"{BAD} need an API url and agent id (login or pass --api/--agent)", file=sys.stderr)
        return 2
    _warn_insecure_transport(api, True)
    req = urllib.request.Request(
        f"{api}/v1/agent/status?agent_id={urllib.parse.quote(agent_id)}",
        headers={"Authorization": "Bearer " + creds.access_token},
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            body = json.loads(resp.read() or b"{}")
    except urllib.error.HTTPError as e:
        print(
            f"{BAD} status failed ({e.code}): {e.read().decode(errors='replace')}", file=sys.stderr
        )
        return 1
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        print(f"{BAD} {_net_err(e)}", file=sys.stderr)
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
        print(f"no logs yet at {path} (run `pyyol run` to generate them)")
        return 0
    with open(path) as f:
        lines = f.readlines()
    for line in lines[-args.n :]:
        sys.stdout.write(line)
    return 0


# --- play / watch (start a match, spectate — the AGENT plays, never the human) --

# Which endpoint starts a self-driving match per game. The developer's connected
# agent (pyyol run) plays it; this only *starts* it. There is deliberately no
# move-input path anywhere in the CLI — a human never plays for the agent.
_PLAY_PATH = {
    "goofspiel": "/v1/sandbox/pushplay",
    "mafia": "/v1/mafia/pushplay",
    "monopoly": "/v1/monopoly/pushplay",
}

# SSE event types that end a match, so `watch` can return control.
_TERMINAL_EVENTS = {"match_finished", "victory", "game_over", "game_finished", "finished"}


def _http_base(args: argparse.Namespace, creds) -> str:
    """Resolve the HTTP API base for /v1/... calls: --api, PYYOL_API, the
    logged-in api url, derived from the WSS connect url (ws→http), else the live
    platform default (so public reads work before login)."""
    if getattr(args, "api", ""):
        return args.api.rstrip("/")
    if os.environ.get("PYYOL_API"):
        return os.environ["PYYOL_API"].rstrip("/")
    if creds and creds.url:
        return creds.url.rstrip("/")
    if creds and creds.connect_url:
        u = urllib.parse.urlsplit(creds.connect_url)
        scheme = "https" if u.scheme in ("wss", "https") else "http"
        return urllib.parse.urlunsplit((scheme, u.netloc, "", "", ""))
    return DEFAULT_API_BASE


def cmd_queue(args: argparse.Namespace) -> int:
    """Enter ranked matchmaking at a stake tier. Your connected agent (pyyol run)
    is driven automatically once matched; this only enqueues + reports the match."""
    from . import credentials

    creds = credentials.load()
    base = _http_base(args, creds)
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`", file=sys.stderr)
        return 2
    game = args.game

    # `--list`: show the admin-configured stake tiers for the game (public) and exit.
    if args.list:
        st, resp = _api_get(f"{base}/v1/games/{game}/stakes")
        if st != 200:
            print(f"{BAD} could not fetch tiers ({st}): {resp}", file=sys.stderr)
            return 1
        tiers = resp.get("tiers") or []
        if not tiers:
            print(f"no stake tiers configured for {game} — use --bid <coins>")
            return 0
        print(f"{game} stake tiers:")
        for t in tiers:
            print(f"  {str(t.get('key','')):8} {int(t.get('coins',0)):>8} coins  {t.get('label','')}")
        return 0

    token = args.token or (creds.access_token if creds else "") or os.environ.get("PYYOL_TOKEN", "")
    if not token:
        print(f"{BAD} not logged in — run `pyyol login` first", file=sys.stderr)
        return 2

    body: Dict[str, object] = {"game": game}
    if args.tier:
        body["tier"] = args.tier
    elif args.bid > 0:
        body["bid"] = args.bid
    else:
        print(
            f"{BAD} choose a stake: --tier <low|mid|high> (see `pyyol queue --list`) "
            f"or --bid <coins> for a tier-less game",
            file=sys.stderr,
        )
        return 2

    st, resp = _api_post(f"{base}/v1/queue", token, body)
    if st not in (200, 202):
        code = str(resp.get("code") or resp.get("error") or "")
        msg = resp.get("message") or ""
        if "certified" in code:
            print(f"{BAD} agent not certified — run `pyyol publish` to verify your endpoint first.", file=sys.stderr)
        elif "tier" in code:
            print(f"{BAD} {msg or code} — see `pyyol queue --list`", file=sys.stderr)
        elif "balance" in code or "insufficient" in code:
            print(f"{BAD} not enough coins to stake this tier (or below your min balance).", file=sys.stderr)
        else:
            print(f"{BAD} could not queue ({st}): {resp}", file=sys.stderr)
        return 1

    print(f"{OK} queued for {game}. Keep your agent connected (`pyyol run`) — it plays automatically when matched.")
    deadline = time.time() + args.wait
    while time.time() < deadline:
        st, s = _api_get(f"{base}/v1/queue", token)
        if st == 200 and s.get("status") == "matched":
            mid = s.get("match_id") or ""
            print(f"{OK} matched → {mid}")
            if mid:
                print(f"    watch it:  pyyol watch {mid}")
            return 0
        time.sleep(1.5)
    print("still waiting for an opponent — leave `pyyol run` connected; check `pyyol status`.")
    return 0


def cmd_watch(args: argparse.Namespace) -> int:
    """Spectate a live match in the terminal — READ-ONLY. Renders the event
    stream; there is no way to influence the game from here."""
    from . import credentials

    creds = credentials.load()
    base = _http_base(args, creds)
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`", file=sys.stderr)
        return 2
    if not args.match:
        print(f"{BAD} usage: pyyol watch <match_id>", file=sys.stderr)
        return 2
    return _watch(base, args.match, args)


def _watch(base: str, match_id: str, args: argparse.Namespace) -> int:
    import urllib.error
    import urllib.request

    from .console import build_console

    console = build_console(
        mode="json" if getattr(args, "json", False) else "pretty",
        color=False if getattr(args, "no_color", False) else None,
    )
    url = f"{base}/v1/match/{urllib.parse.quote(match_id)}/watch"
    req = urllib.request.Request(url, headers={"Accept": "text/event-stream"})
    console.emit("match", f"spectating {match_id} (read-only)")
    try:
        with urllib.request.urlopen(req, timeout=30) as resp:
            _render_sse(resp, console)
    except KeyboardInterrupt:
        print("\nstopped watching.")
    except urllib.error.HTTPError as e:
        print(
            f"{BAD} watch failed ({e.code}): {e.read().decode(errors='replace')}", file=sys.stderr
        )
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


def _api_get(url: str, token: str = ""):
    import urllib.error
    import urllib.request

    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
        _warn_insecure_transport(url, True)
    req = urllib.request.Request(url, method="GET", headers=headers)
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
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return 0, {"error": _net_err(e)}


def _api_post(url: str, token: str, body):
    import urllib.error
    import urllib.request

    _warn_insecure_transport(url, bool(token))
    data = json.dumps(body).encode() if body else b""
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    )
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
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return 0, {"error": _net_err(e)}


# --- run (connect the local agent over WSS) ------------------------------------


def _log_file_handler():
    """Attach a file handler so `pyyol logs` has content. Propagation is off so
    the file is the only logger sink — the live feed is the Console (stdout)."""
    import logging

    from . import credentials

    d = os.path.join(credentials.config_dir(), "logs")
    os.makedirs(d, exist_ok=True)
    handler = logging.FileHandler(os.path.join(d, "agent.log"))
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    logger = logging.getLogger("pyyol")
    logger.setLevel(logging.INFO)
    logger.propagate = False
    logger.addHandler(handler)


def cmd_run(args: argparse.Namespace) -> int:
    """Load the developer's agent object and connect it to the platform over the
    outbound WebSocket. This is the local-runtime path: no inbound endpoint."""
    import importlib.util

    from . import credentials

    creds = credentials.load()
    url = args.url or os.environ.get("PYYOL_URL", "") or (creds.connect_url if creds else "")
    if not url:
        print(
            f"{BAD} no platform URL — pass --url, set PYYOL_URL, or run `pyyol login`",
            file=sys.stderr,
        )
        return 2
    agent_id = (
        args.agent or os.environ.get("PYYOL_AGENT_ID", "") or (creds.agent_id if creds else "")
    )
    token = args.token or os.environ.get("PYYOL_TOKEN", "") or (creds.access_token if creds else "")
    _log_file_handler()

    # Load the agent module and find the `Agent` instance (var name configurable).
    spec = importlib.util.spec_from_file_location("_pyyol_user_agent", args.file)
    if spec is None or spec.loader is None:
        print(f"{BAD} cannot load {args.file}", file=sys.stderr)
        return 2
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    agent = getattr(mod, args.var, None)
    if agent is None:
        print(
            f"{BAD} no `{args.var}` found in {args.file} (expose your Agent as `{args.var}`)",
            file=sys.stderr,
        )
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


# --- serve / autoplay (deploy once, plays anytime) -----------------------------


def _load_agent(file: str, var: str):
    """Import the developer's module and return the exposed Agent object (or None
    after printing why). Shared by `serve`."""
    import importlib.util

    spec = importlib.util.spec_from_file_location("_pyyol_user_agent", file)
    if spec is None or spec.loader is None:
        print(f"{BAD} cannot load {file}", file=sys.stderr)
        return None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    agent = getattr(mod, var, None)
    if agent is None:
        print(f"{BAD} no `{var}` found in {file} (expose your Agent as `{var}`)", file=sys.stderr)
        return None
    return agent


def _autoplay_set(api: str, token: str, *, enabled: bool, mode: str, bid: int, games: list) -> tuple:
    """PUT the agent's auto-play setting (availability). Returns (status, body)."""
    import urllib.error
    import urllib.request

    body = json.dumps({"enabled": enabled, "mode": mode, "bid": bid, "games": games}).encode()
    req = urllib.request.Request(
        api.rstrip("/") + "/v1/agent/autoplay",
        data=body,
        method="PUT",
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        return e.code, (json.loads(raw) if raw else {})
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return 0, {"error": _net_err(e)}


def _autoplay_opts(args, cfg) -> tuple:
    """Resolve (mode, games) from flags → pyyol.toml → defaults."""
    mode = "ranked" if getattr(args, "ranked", False) else (args.mode or (cfg.mode if cfg else "") or "sandbox")
    games = [g.strip() for g in (args.games or "").split(",") if g.strip()]
    if not games and cfg and cfg.arena:
        games = [cfg.arena]
    return mode, games


def cmd_serve(args: argparse.Namespace) -> int:
    """Deploy-once worker: switch auto-play ON, then hold the outbound WebSocket
    open so the platform drives your agent for every match it is paired into.
    Ctrl-C switches auto-play back OFF so you stop being matched once you exit."""
    from . import config, credentials

    creds = credentials.load()
    api = (args.api or (creds.url if creds else "")).rstrip("/")
    url = args.url or os.environ.get("PYYOL_URL", "") or (creds.connect_url if creds else "")
    token = args.token or os.environ.get("PYYOL_TOKEN", "") or (creds.access_token if creds else "")
    agent_id = args.agent or os.environ.get("PYYOL_AGENT_ID", "") or (creds.agent_id if creds else "")
    if not (api and token):
        print(f"{BAD} run `pyyol login` first (need the API base + token)", file=sys.stderr)
        return 2

    cfg = config.load()
    mode, games = _autoplay_opts(args, cfg)
    agent = _load_agent(args.file, args.var)
    if agent is None:
        return 2

    _warn_insecure_transport(api, bool(token))
    st, resp = _autoplay_set(api, token, enabled=True, mode=mode, bid=args.bid, games=games)
    if st and 200 <= st < 300:
        extra = f", bid={args.bid}" if mode == "ranked" else ""
        print(f"{OK} auto-play ON — mode={mode}{extra}, games={games or 'default'}")
    else:
        print(f"{BAD} could not enable auto-play (status {st}: {resp}); holding the connection anyway", file=sys.stderr)

    print("serving — the platform will drive your agent as matches are paired. Ctrl-C to stop.")
    _log_file_handler()
    from .console import build_console

    console = build_console(
        mode="json" if args.json else "pretty",
        quiet=args.quiet,
        color=False if args.no_color else None,
    )
    try:
        agent.run(url=url, agent_id=agent_id, token=token, console=console)
    except KeyboardInterrupt:
        print("\nstopping…")
    finally:
        _autoplay_set(api, token, enabled=False, mode=mode, bid=args.bid, games=games)
        print(f"{OK} auto-play OFF")
    return 0


def cmd_autoplay(args: argparse.Namespace) -> int:
    """Toggle auto-play WITHOUT holding a connection — for a hosted endpoint the
    platform calls in, so you just flip the switch (`pyyol autoplay on|off`)."""
    from . import config, credentials

    creds = credentials.load()
    api = (args.api or (creds.url if creds else "")).rstrip("/")
    token = args.token or (creds.access_token if creds else "")
    if not (api and token):
        print(f"{BAD} run `pyyol login` first", file=sys.stderr)
        return 2
    on = args.state == "on"
    cfg = config.load()
    mode, games = _autoplay_opts(args, cfg)
    _warn_insecure_transport(api, bool(token))
    st, resp = _autoplay_set(api, token, enabled=on, mode=mode, bid=args.bid, games=games)
    if st and 200 <= st < 300:
        detail = f" — mode={mode}, games={games or 'default'}" if on else ""
        print(f"{OK} auto-play {'ON' if on else 'OFF'}{detail}")
        return 0
    print(f"{BAD} failed (status {st}): {resp}", file=sys.stderr)
    return 1


# --- init (scaffold) -----------------------------------------------------------

_PY_STARTER_GOOFSPIEL = '''\
"""{name} — a Pyyol agent. Implement step(); initialize()/shutdown() are optional.

Run it:  pyyol dev            # practice locally (sandbox, no stakes)
         pyyol play goofspiel # compete (add --ranked for real stakes, after `pyyol publish`)
"""
from pyyol import Adapter
from pyyol.models import GoofspielView, GoofspielMove


class {cls}(Adapter):
    name = "{name}"
    supported_games = ["goofspiel"]

    def initialize(self, ctx):
        # Called once at match start (optional): ctx has match_id, seat, players.
        pass

    def step(self, view: GoofspielView) -> GoofspielMove:
        # Your strategy goes here. Baseline: spend the smallest legal card.
        return GoofspielMove(card=min(view.legal_actions), round=view.round)

    def shutdown(self, result):
        # Called once when the match ends (optional).
        pass


# `pyyol dev` / `pyyol play` discover this via pyyol.toml (entry = "agent.py:agent").
agent = {cls}()
'''

_PY_STARTER_GENERIC = '''\
"""{name} — a Pyyol agent for {arena}. Implement step(); the SDK owns everything else.

Run it:  pyyol dev            # practice locally (sandbox, no stakes)
         pyyol play {arena}   # compete (add --ranked for real stakes, after `pyyol publish`)
"""
from pyyol import Adapter


class {cls}(Adapter):
    name = "{name}"
    supported_games = ["{arena}"]

    def initialize(self, ctx):
        pass

    def step(self, view):
        # `view.legal_actions` lists what you may do this turn. Baseline: take the first.
        legal = getattr(view, "legal_actions", None) or []
        return {{"action": legal[0]}} if legal else {{}}

    def shutdown(self, result):
        pass


agent = {cls}()
'''

_JS_STARTER = """\
import {{ Agent }} from "pyyol";

const agent = new Agent({{ supportedGames: ["{arena}"], name: "{name}" }});

agent.onTurn("{arena}", (v) => ({{ round: v.round, card: Math.min(...v.legal_actions) }}));

export default agent;  // pyyol dev / pyyol play discover this via pyyol.toml
"""

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


def _class_name(name: str) -> str:
    """Turn a project name into a Python class name, e.g. 'my-atlas' -> 'MyAtlas'."""
    parts = [p for p in "".join(c if c.isalnum() else " " for c in name).split() if p]
    cls = "".join(p[:1].upper() + p[1:] for p in parts) or "Agent"
    if cls[0].isdigit():
        cls = "A" + cls
    return cls


def cmd_init(args: argparse.Namespace) -> int:
    from . import config as cfgmod

    d = args.dir
    os.makedirs(d, exist_ok=True)
    name = args.name or os.path.basename(os.path.abspath(d))
    arena = args.arena or "goofspiel"
    cls = _class_name(name)
    lang = "javascript" if args.lang == "js" else "python"

    if lang == "javascript":
        path = os.path.join(d, "agent.mjs")
        code = _JS_STARTER.format(name=name, arena=arena)
        entry = "agent.mjs:agent"
    else:
        path = os.path.join(d, "agent.py")
        tmpl = _PY_STARTER_GOOFSPIEL if arena == "goofspiel" else _PY_STARTER_GENERIC
        code = tmpl.format(name=name, cls=cls, arena=arena)
        entry = "agent.py:agent"
    with open(path, "w") as f:
        f.write(code)

    # Convention-over-configuration: a tiny pyyol.toml, no manifest.
    cfg = cfgmod.Config(
        name=name,
        language=lang,
        framework=args.framework or "",
        arena=arena,
        visibility="private",
        mode="sandbox",
        entry=entry,
    )
    cfg_path = cfgmod.save(cfg, d)

    print(f"{OK} created {lang} agent in {d}/")
    print(f"    {path}")
    print(f"    {cfg_path}")
    print("\nNext:")
    print("    pip install pyyol" if lang == "python" else "    npm install pyyol")
    print(f"    cd {d} && pyyol dev            # practice locally (sandbox — no stakes)")
    print(f"    pyyol play {arena}             # compete (sandbox); add --ranked for real")
    return 0


# --- v2: shared helpers --------------------------------------------------------


def _load_config_or_die():
    """Load pyyol.toml from the cwd tree, or print guidance and return None."""
    from . import config as cfgmod

    cfg = cfgmod.load()
    if cfg is None:
        print(f"{BAD} no pyyol.toml here — run `pyyol init <dir>` first.", file=sys.stderr)
    return cfg


def _load_agent_from_config(cfg):
    """Import the developer's agent object per pyyol.toml `entry` and normalize it to
    a pyyol Agent (accepts Agent, Adapter instance, or Adapter subclass)."""
    import importlib.util

    from . import config as cfgmod
    from .server import as_agent

    module_path, var = cfg.entry_parts()
    # Constrain `entry` to the project root (dir of the discovered pyyol.toml): reject
    # absolute paths and `..` traversal so a hostile pyyol.toml can't point the loader
    # at an arbitrary file outside the project.
    cfg_file = cfgmod.find()
    root = os.path.dirname(os.path.abspath(cfg_file)) if cfg_file else os.getcwd()
    resolved = os.path.abspath(os.path.join(root, module_path))
    if os.path.isabs(module_path) or os.path.commonpath([root, resolved]) != root:
        raise ValueError(f"entry {module_path!r} must be inside the project ({root})")
    module_path = resolved
    if not os.path.exists(module_path):
        raise FileNotFoundError(f"entry module {module_path!r} not found (see pyyol.toml `entry`)")
    spec = importlib.util.spec_from_file_location("_pyyol_user_agent", module_path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {module_path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    obj = getattr(mod, var, None)
    if obj is None:
        raise AttributeError(f"no `{var}` in {module_path} (see pyyol.toml `entry`)")
    return as_agent(obj)


def _orchestrate(args: argparse.Namespace, *, dev_locked: bool) -> int:
    """Shared engine behind `pyyol dev` (develop) and `pyyol play` (compete): resolve
    mode, connect the agent over WSS, and drive matches — hiding all transport."""
    import threading

    from . import config as cfgmod
    from . import credentials, mode
    from .console import build_console
    from .runtime import RuntimeConnector

    cfg = _load_config_or_die()
    if cfg is None:
        return 2
    creds = credentials.load()
    if creds is None or not creds.access_token:
        print(f"{BAD} not logged in — run `pyyol login` first.", file=sys.stderr)
        return 2

    connect_url = (
        args.url or os.environ.get("PYYOL_URL", "") or (creds.connect_url if creds else "")
    )
    base = _http_base(args, creds)
    # The agent id MUST match the token's owner. The token comes from creds, so
    # creds.agent_id (its matched pair) wins over a possibly-stale pyyol.toml pin —
    # else the socket's "key.agent == claimed agent_id" check rejects the register.
    agent_id = args.agent or (creds.agent_id if creds else "") or cfg.agent_id
    token = args.token or os.environ.get("PYYOL_TOKEN", "") or creds.access_token
    if not connect_url or not agent_id:
        print(f"{BAD} missing connect URL or agent id — run `pyyol login` (or pass --url/--agent).", file=sys.stderr)
        return 2

    arena = getattr(args, "arena", "") or cfg.arena
    m = mode.resolve(ranked_flag=getattr(args, "ranked", False), cfg_mode=cfg.mode, dev_locked=dev_locked)
    print(mode.banner(m))

    # Real-stakes guardrails: explicit opt-in confirmation. Certification is enforced
    # server-side at enqueue (we surface a friendly message if it's missing).
    if m == mode.RANKED:
        if not mode.confirm_ranked(assume_yes=getattr(args, "yes", False)):
            print("aborted — staying safe. (Use --yes in CI to skip the prompt.)")
            return 1

    # Persist the agent id back into pyyol.toml so future runs are zero-config.
    if agent_id and not cfg.agent_id:
        cfgmod.set_agent_id(agent_id)

    try:
        agent = _load_agent_from_config(cfg)
    except Exception as e:  # noqa: BLE001
        print(f"{BAD} could not load your agent: {e}", file=sys.stderr)
        return 2

    console = build_console(quiet=getattr(args, "quiet", False))
    conn = RuntimeConnector(
        agent, url=connect_url, agent_id=agent_id, token=token,
        name=agent.name, games=agent.supported_games, console=console,
    )
    stop = threading.Event()

    def kicker():
        # Give the socket a moment to register, then start match(es). pushplay/queue
        # drive the just-connected agent; retry briefly while it comes online.
        matches = max(1, getattr(args, "matches", 1))
        if m == mode.RANKED:
            _start_ranked(base, token, arena, args, console)
            return
        for i in range(matches):
            if stop.is_set():
                return
            _start_sandbox(base, token, arena, console, attempt_label=f"{i + 1}/{matches}")
            time.sleep(2.0)

    threading.Thread(target=kicker, daemon=True, name="pyyol-kicker").start()
    try:
        conn.run()  # blocks: connect + heartbeat + reconnect + serve turns
    except KeyboardInterrupt:
        pass
    finally:
        stop.set()
        conn.stop()
    print("\nstopped.")
    return 0


def _start_sandbox(base, token, arena, console, attempt_label="") -> None:
    path = _PLAY_PATH.get(arena, _PLAY_PATH["goofspiel"])
    last = {}
    for _ in range(6):  # ~9s: wait for the socket to be registered before starting
        st, resp = _api_post(f"{base}{path}", token, {})
        if st in (200, 201):
            mid = resp.get("match_id") or resp.get("id") or ""
            console.emit("match", f"started {arena} match {mid} {attempt_label}".rstrip())
            return
        last = resp
        code = str(resp.get("code") or resp.get("error") or "")
        if "transport" in code or "no_agent" in code or st in (409, 425):
            time.sleep(1.5)
            continue
        break
    console.emit("error", f"could not start {arena} match: {last}")


def _start_ranked(base, token, arena, args, console) -> None:
    body: Dict[str, object] = {"game": arena}
    tier = getattr(args, "tier", "") or "low"
    body["tier"] = tier
    st, resp = _api_post(f"{base}/v1/queue", token, body)
    if st in (200, 202):
        console.emit("match", f"queued for RANKED {arena} (tier {tier}) — you play when matched")
        return
    code = str(resp.get("code") or resp.get("error") or "")
    if "certified" in code:
        console.emit(
            "error",
            "agent not certified for ranked — run `pyyol publish` first (ranked needs a verified endpoint).",
        )
    else:
        console.emit("error", f"could not queue ranked ({st}): {resp}")


# --- v2: informational commands (whoami / arenas / leaderboard / profile / replay) ---


def cmd_whoami(args: argparse.Namespace) -> int:
    from . import config as cfgmod
    from . import credentials

    creds = credentials.load()
    if creds is None or not creds.access_token:
        print(f"{BAD} not logged in — run `pyyol login`.", file=sys.stderr)
        return 2
    base = _http_base(args, creds)
    st, me = _api_get(f"{base}/v1/me", creds.access_token) if base else (0, {})
    user = me.get("user_id") or "(unknown)"
    agent = me.get("agent_id") or creds.agent_id or "(none)"
    cfg = cfgmod.load()
    print(f"user      {user}")
    print(f"agent     {agent}")
    print(f"platform  {creds.url or base or '(unset)'}")
    if cfg is not None:
        print(f"project   {cfg.name}  ·  arena {cfg.arena}  ·  mode {cfg.mode}")
    return 0


def cmd_arenas(args: argparse.Namespace) -> int:
    from . import credentials

    base = _http_base(args, credentials.load())
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`.", file=sys.stderr)
        return 2
    st, resp = _api_get(f"{base}/v1/arenas")
    if st != 200:
        print(f"{BAD} could not fetch arenas ({st}): {resp.get('error') or resp}", file=sys.stderr)
        return 1
    arenas = resp.get("arenas") or []
    print(f"{'ARENA':<12}{'PLAYERS':<10}{'SANDBOX':<9}{'RANKED':<8}STATUS")
    for a in arenas:
        players = f"{a.get('min_players')}-{a.get('max_players')}"
        print(
            f"{a.get('id',''):<12}{players:<10}"
            f"{('yes' if a.get('sandbox') else 'no'):<9}"
            f"{('yes' if a.get('ranked') else 'no'):<8}{a.get('status','')}"
        )
    return 0


def cmd_leaderboard(args: argparse.Namespace) -> int:
    from . import credentials

    base = _http_base(args, credentials.load())
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`.", file=sys.stderr)
        return 2
    if args.developers:
        url = f"{base}/v1/leaderboard/developers"
        if args.season:
            url += f"?season={args.season}"
        st, resp = _api_get(url)
        rows = resp.get("entries") or []
        print(f"{'#':<5}{'DEVELOPER':<24}P-INDEX")
        for r in rows:
            who = r.get("username") or r.get("developer") or "?"
            print(f"{r.get('rank',''):<5}{who:<24}{r.get('p_index','')}")
        return 0 if st == 200 else 1
    q = []
    if args.game:
        q.append(f"game={urllib.parse.quote(args.game)}")
    if args.season:
        q.append(f"season={args.season}")
    url = f"{base}/v1/leaderboard" + (("?" + "&".join(q)) if q else "")
    st, resp = _api_get(url)
    if st != 200:
        print(f"{BAD} could not fetch leaderboard ({st}): {resp.get('error') or resp}", file=sys.stderr)
        return 1
    rows = resp.get("entries") or []
    print(f"{'#':<5}{'AGENT':<24}{'ELO':<7}W-L-T")
    for r in rows:
        wlt = f"{r.get('wins',0)}-{r.get('losses',0)}-{r.get('ties',0)}"
        print(f"{r.get('rank',''):<5}{(r.get('name') or r.get('slug') or '?'):<24}{r.get('elo',''):<7}{wlt}")
    return 0


def cmd_profile(args: argparse.Namespace) -> int:
    from . import credentials

    creds = credentials.load()
    base = _http_base(args, creds)
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`.", file=sys.stderr)
        return 2
    handle = args.handle
    if not handle:  # self
        _, me = _api_get(f"{base}/v1/me", creds.access_token if creds else "")
        handle = me.get("user_id") or ""
        if not handle:
            print(f"{BAD} pass a handle: `pyyol profile <@handle>`", file=sys.stderr)
            return 2
    st, p = _api_get(f"{base}/v1/developers/{urllib.parse.quote(handle)}")
    if st != 200:
        print(f"{BAD} no such developer {handle!r} ({st}).", file=sys.stderr)
        return 1
    dev = p.get("developer", {})
    pidx = p.get("p_index") or {}
    stats = p.get("stats") or {}
    print(f"@{dev.get('username') or dev.get('developer','?')}")
    if pidx:
        print(f"  P-Index   {pidx.get('p_index','?')}  (rank #{pidx.get('global_rank','?')}, top {pidx.get('percentile','?')}%)")
    print(f"  Record    {stats.get('wins',0)}W-{stats.get('losses',0)}L-{stats.get('draws',0)}D over {stats.get('total_matches',0)} matches")
    if stats.get("favorite_arena"):
        print(f"  Favorite  {stats.get('favorite_arena')}")
    print(f"  Agents    {len(p.get('agents') or [])}   Followers {p.get('followers',0)}")
    return 0


_REPLAY_PATH = {
    "goofspiel": "/v1/match/{id}/replay",
    "mafia": "/v1/mafia/{id}/replay",
    "monopoly": "/v1/monopoly/{id}/replay",
}


def cmd_replay(args: argparse.Namespace) -> int:
    from . import credentials

    creds = credentials.load()
    base = _http_base(args, creds)
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`.", file=sys.stderr)
        return 2
    game = args.game or (creds and _cfg_arena()) or "goofspiel"
    path = _REPLAY_PATH.get(game, _REPLAY_PATH["goofspiel"]).format(id=urllib.parse.quote(args.match))
    st, resp = _api_get(f"{base}{path}")
    if st != 200:
        print(f"{BAD} could not fetch replay ({st}): {resp.get('error') or resp}", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps(resp, indent=2))
        return 0
    events = resp.get("events") or resp.get("moves") or []
    winner, scores = _replay_outcome(resp)
    print(f"replay {args.match} ({game}) — {len(events)} events, status {resp.get('status', '?')}")
    if winner:
        line = f"  winner    {winner}"
        if scores:
            line += f"  (scores {'-'.join(str(s) for s in scores)})"
        print(line)
    if resp.get("moves_verified") is not None:
        print(f"  verified  {resp.get('moves_verified')} (every move signed + valid)")
    print(f"  full JSON: pyyol replay {args.match} --game {game} --json")
    return 0


def _winner_label(w) -> str:
    """Map a winner value to a display label. Seat ints (0/1/-1) become
    'seat N'/'tie'; strings (mafia team / monopoly seat / agent id) pass through."""
    if w is None or w == "":
        return ""
    if isinstance(w, bool):
        return ""
    if isinstance(w, int):
        return "tie" if w < 0 else f"seat {w}"
    return str(w)


def _replay_outcome(resp: Dict[str, Any]):
    """Extract (winner_label, scores) from a replay doc. Goofspiel encodes the result
    in a terminal `match_finished` event (winner seat + scores); mafia/monopoly may
    carry a top-level winner. Returns ("", None) when it can't be determined."""
    for k in ("winner", "winner_team", "winner_agent"):
        if resp.get(k) not in (None, ""):
            return _winner_label(resp[k]), None
    for ev in reversed(resp.get("events") or []):
        if not isinstance(ev, dict):
            continue
        payload = ev.get("payload") if isinstance(ev.get("payload"), dict) else {}
        if ev.get("type") in ("match_finished", "game_over", "victory", "finished") or "winner" in payload:
            return _winner_label(payload.get("winner")), payload.get("scores")
    return "", None


def _cfg_arena() -> str:
    from . import config as cfgmod

    cfg = cfgmod.load()
    return cfg.arena if cfg else ""


def cmd_doctor(args: argparse.Namespace) -> int:
    from . import config as cfgmod
    from . import credentials

    checks: List[tuple[str, bool, str]] = []
    creds = credentials.load()
    checks.append(("logged in", bool(creds and creds.access_token), creds.url if creds else "run `pyyol login`"))

    cfg = cfgmod.load()
    if cfg is None:
        checks.append(("pyyol.toml", False, "run `pyyol init`"))
    else:
        problems = cfgmod.validate(cfg)
        checks.append(("pyyol.toml", not problems, "; ".join(problems) or f"{cfg.name} · {cfg.arena} · {cfg.mode}"))
        # Agent module imports?
        try:
            _load_agent_from_config(cfg)
            checks.append(("agent loads", True, cfg.entry))
        except Exception as e:  # noqa: BLE001
            checks.append(("agent loads", False, str(e)))

    base = _http_base(args, creds)
    if base:
        st, _ = _api_get(f"{base}/v1/arenas")
        checks.append(("platform reachable", st == 200, f"{base} ({st})"))
    else:
        checks.append(("platform reachable", False, "no API url"))

    checks.append(("sdk version", True, __version__))

    print("pyyol doctor\n")
    all_ok = True
    for name, ok, detail in checks:
        all_ok = all_ok and ok
        print(f"  {OK if ok else BAD} {name:<20} {detail}")
    ready = all_ok
    print("\n" + ("✓ ready — `pyyol dev` to practice, `pyyol play <arena>` to compete." if ready
                  else "fix the ✗ items above."))
    return 0 if ready else 1


def cmd_update(args: argparse.Namespace) -> int:
    import urllib.request

    print(f"pyyol {__version__}")
    latest = ""
    try:
        with urllib.request.urlopen("https://pypi.org/pypi/pyyol/json", timeout=5) as resp:
            latest = json.loads(resp.read()).get("info", {}).get("version", "")
    except Exception:  # noqa: BLE001 — offline / not published yet
        pass
    if latest and latest != __version__:
        print(f"  update available: {latest}")
        print("  run:  pip install -U pyyol")
    elif latest:
        print("  you're up to date.")
    else:
        print("  run:  pip install -U pyyol")
    return 0


def cmd_dev(args: argparse.Namespace) -> int:
    """Local development loop — sandbox-locked (never real stakes)."""
    return _orchestrate(args, dev_locked=True)


def cmd_play(args: argparse.Namespace) -> int:
    """Start competing in a chosen arena. Sandbox by default; --ranked = real stakes."""
    return _orchestrate(args, dev_locked=False)


# --- entry point ---------------------------------------------------------------


def _add_api(sp):
    sp.add_argument("--api", default="", help="platform API base (defaults to the logged-in one)")


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(
        prog="pyyol",
        description="Pyyol — build, run, and rank autonomous AI agents. "
        "Quickstart: pyyol login → pyyol init → pyyol dev.",
    )
    p.add_argument("--version", action="version", version=f"pyyol {__version__}")
    sub = p.add_subparsers(dest="command", required=True, metavar="<command>")

    # --- auth ---
    pl = sub.add_parser("login", help="log in via the browser (GitHub/Google/wallet/email)")
    pl.add_argument("--with", dest="provider", default="", choices=["github", "google", "wallet"],
                    help="pre-select a provider on the login page")
    pl.add_argument("--dashboard", default="", help="dashboard base URL that serves /cli-login (default: https://pyyol.com; or $PYYOL_DASHBOARD)")
    pl.add_argument("--api", default="", help="platform API base URL to record (default: https://api.pyyol.com; or $PYYOL_API)")
    pl.add_argument("--connect", default="", help="override the WSS connect URL")
    pl.add_argument("--agent", default="", help="agent public id (if known)")
    pl.add_argument("--token", default="", help="paste a token / PAT directly (CI / headless)")
    pl.set_defaults(func=cmd_login)

    sub.add_parser("logout", help="remove stored credentials").set_defaults(func=cmd_logout)

    pwho = sub.add_parser("whoami", help="show who you're logged in as")
    _add_api(pwho)
    pwho.set_defaults(func=cmd_whoami)

    # --- project ---
    pi = sub.add_parser("init", help="scaffold a new agent project (agent + pyyol.toml)")
    pi.add_argument("dir")
    pi.add_argument("--lang", choices=["python", "js"], default="python")
    pi.add_argument("--framework", default="", help="e.g. langgraph, crewai, openai-agents")
    pi.add_argument("--arena", choices=["goofspiel", "mafia", "monopoly"], default="goofspiel")
    pi.add_argument("--name", default="")
    pi.set_defaults(func=cmd_init)

    # --- develop (sandbox-locked) ---
    pdev = sub.add_parser("dev", help="run your agent locally in SANDBOX (no stakes) — the dev loop")
    pdev.add_argument("--matches", type=int, default=3, help="practice matches to auto-start")
    pdev.add_argument("--url", default="", help="connect URL (or PYYOL_URL; defaults to login)")
    pdev.add_argument("--agent", default="", help="agent id (or PYYOL_AGENT_ID; defaults to login)")
    pdev.add_argument("--token", default="", help="token (or PYYOL_TOKEN; defaults to login)")
    pdev.add_argument("--quiet", action="store_true")
    pdev.add_argument("--no-color", action="store_true")
    _add_api(pdev)
    pdev.set_defaults(func=cmd_dev)

    # --- compete (explicit; --ranked = real stakes) ---
    pp = sub.add_parser("play", help="compete in an arena. SANDBOX by default; --ranked = real stakes")
    pp.add_argument("arena", choices=["goofspiel", "mafia", "monopoly"])
    pp.add_argument("--ranked", action="store_true", help="REAL stakes (needs `pyyol publish`; confirmed)")
    pp.add_argument("--tier", default="low", help="ranked stake tier: low|mid|high")
    pp.add_argument("--matches", type=int, default=1, help="sandbox matches to start")
    pp.add_argument("--yes", action="store_true", help="skip the ranked confirmation (CI)")
    pp.add_argument("--url", default="")
    pp.add_argument("--agent", default="")
    pp.add_argument("--token", default="")
    pp.add_argument("--quiet", action="store_true")
    pp.add_argument("--no-color", action="store_true")
    _add_api(pp)
    pp.set_defaults(func=cmd_play)

    ppub = sub.add_parser("publish", help="certify your agent for RANKED play (verify a hosted endpoint)")
    ppub.add_argument("--api", default="", help="platform API base (or from login)")
    ppub.add_argument("--agent", default="", help="agent public id (or from login)")
    ppub.add_argument("--token", default="", help="dashboard/access token (or from login)")
    ppub.add_argument("--manifest", required=True, help="path to manifest.json (hosted endpoint)")
    ppub.add_argument("--secret", default="", help="endpoint secret to store before verify")
    ppub.set_defaults(func=cmd_publish)

    # --- discover / inspect ---
    prep = sub.add_parser("replay", help="fetch a match replay")
    prep.add_argument("match")
    prep.add_argument("--game", choices=["goofspiel", "mafia", "monopoly"], default="")
    prep.add_argument("--json", action="store_true")
    _add_api(prep)
    prep.set_defaults(func=cmd_replay)

    ppro = sub.add_parser("profile", help="show a developer profile + P-Index (self if omitted)")
    ppro.add_argument("handle", nargs="?", default="")
    _add_api(ppro)
    ppro.set_defaults(func=cmd_profile)

    plb = sub.add_parser("leaderboard", help="show the leaderboard")
    plb.add_argument("--game", default="", help="per-arena agent board")
    plb.add_argument("--developers", action="store_true", help="developer (P-Index) board")
    plb.add_argument("--season", type=int, default=0)
    _add_api(plb)
    plb.set_defaults(func=cmd_leaderboard)

    par = sub.add_parser("arenas", help="list available arenas")
    _add_api(par)
    par.set_defaults(func=cmd_arenas)

    pdoc = sub.add_parser("doctor", help="diagnose your setup (login, config, agent, platform)")
    _add_api(pdoc)
    pdoc.set_defaults(func=cmd_doctor)

    sub.add_parser("update", help="check for a newer pyyol").set_defaults(func=cmd_update)

    # --- advanced / compatibility aliases (lower-level; dev/play front-end these) ---
    pv = sub.add_parser("validate", help="[advanced] probe a hosted endpoint like the platform does")
    pv.add_argument("--url", required=True)
    pv.add_argument("--secret", default="")
    pv.add_argument("--game", choices=["goofspiel", "monopoly", "mafia"], default="goofspiel")
    pv.set_defaults(func=cmd_validate)

    ps = sub.add_parser("simulate", help="[advanced] drive a full local match against a hosted endpoint")
    ps.add_argument("--url", required=True)
    ps.add_argument("--secret", default="")
    ps.add_argument("--game", choices=["goofspiel"], default="goofspiel")
    ps.add_argument("--hand", type=int, default=13)
    ps.set_defaults(func=cmd_simulate)

    prun = sub.add_parser("run", help="[advanced] connect your agent over WSS (dev/play front-end this)")
    prun.add_argument("--file", default="agent.py")
    prun.add_argument("--var", default="agent")
    prun.add_argument("--url", default="")
    prun.add_argument("--agent", default="")
    prun.add_argument("--token", default="")
    prun.add_argument("--json", action="store_true")
    prun.add_argument("--quiet", action="store_true")
    prun.add_argument("--no-color", action="store_true")
    prun.set_defaults(func=cmd_run)

    psv = sub.add_parser("serve", help="deploy-once worker: enable auto-play + hold the connection so your agent plays anytime")
    psv.add_argument("--file", default="agent.py")
    psv.add_argument("--var", default="agent")
    psv.add_argument("--url", default="")
    psv.add_argument("--agent", default="")
    psv.add_argument("--token", default="")
    _add_api(psv)
    psv.add_argument("--ranked", action="store_true", help="auto-play RANKED (real stakes); default sandbox")
    psv.add_argument("--mode", default="", choices=["", "sandbox", "ranked"], help="explicit mode (overrides pyyol.toml)")
    psv.add_argument("--bid", type=int, default=0, help="ranked stake per match")
    psv.add_argument("--games", default="", help="comma-separated games to rotate (sandbox); default = your arena")
    psv.add_argument("--json", action="store_true")
    psv.add_argument("--quiet", action="store_true")
    psv.add_argument("--no-color", action="store_true")
    psv.set_defaults(func=cmd_serve)

    pap = sub.add_parser("autoplay", help="toggle auto-play without holding a connection (for hosted endpoints)")
    pap.add_argument("state", choices=["on", "off"])
    _add_api(pap)
    pap.add_argument("--token", default="")
    pap.add_argument("--ranked", action="store_true", help="auto-play RANKED (real stakes); default sandbox")
    pap.add_argument("--mode", default="", choices=["", "sandbox", "ranked"])
    pap.add_argument("--bid", type=int, default=0)
    pap.add_argument("--games", default="")
    pap.set_defaults(func=cmd_autoplay)

    pst = sub.add_parser("status", help="[advanced] is your agent connected?")
    _add_api(pst)
    pst.add_argument("--agent", default="")
    pst.set_defaults(func=cmd_status)

    plg = sub.add_parser("logs", help="[advanced] recent local agent logs")
    plg.add_argument("--file", default="")
    plg.add_argument("-n", type=int, default=50)
    plg.set_defaults(func=cmd_logs)

    pw = sub.add_parser("watch", help="[advanced] spectate a live match (read-only)")
    pw.add_argument("match")
    _add_api(pw)
    pw.add_argument("--json", action="store_true")
    pw.add_argument("--no-color", action="store_true")
    pw.set_defaults(func=cmd_watch)

    return p


def main(argv: Optional[List[str]] = None) -> int:
    args = build_parser().parse_args(argv)
    return args.func(args)


if __name__ == "__main__":
    raise SystemExit(main())
