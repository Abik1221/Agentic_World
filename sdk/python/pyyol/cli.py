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
import urllib.request  # module-level: _urlopen resolves it at call time, in any import order
from typing import Any

from . import __version__

# NOTE: `urllib.request`/`urllib.error` (which pull in `ssl`, `http.client`, `email`
# ≈40ms) and `.signing` are imported LAZILY inside the functions that make network
# calls, so no-network commands (init / --version / --help / doctor-offline) stay
# instant. Do not add them at module top.

OK = "✓"
BAD = "✗"
WARN = "•"

# N-player games use the group matchmaking queue (/v1/group-queue); Goofspiel (1v1)
# uses the 2-player queue (/v1/queue). Same enqueue request shape, different endpoint.
GROUP_GAMES = frozenset({"mafia"})


def queue_path_for(game: str) -> str:
    """Return the matchmaking endpoint for a game.

    Goofspiel is 1v1 and uses the 2-player queue; Mafia is N-player and
    pool into a full table via the group queue. The request shape is identical, only
    the endpoint differs — which is exactly why this must not be inlined at each call
    site: `pyyol play --ranked mafia` hardcoded /v1/queue, and that queue rejects
    every game but Goofspiel, so ranked Mafia could not be entered at all
    from the CLI.
    """
    return "/v1/group-queue" if game in GROUP_GAMES else "/v1/queue"


# Agent API keys look like "sk_arena_<lookup>_<secret>" — the long-lived, revocable
# connection credential (mirrors backend platform.PrefixKey).
_AGENT_KEY_PREFIX = "sk_arena_"

# Public platform defaults. A dev who `pip install pyyol` and runs `pyyol login`
# hits the live platform with no flags; self-hosted/local users override via
# PYYOL_API / PYYOL_DASHBOARD env vars (or --api / --dashboard). The API host
# serves the arena /v1/* endpoints; the dashboard host serves the /cli-login page
# — they are DIFFERENT hosts in the split-domain deployment.
DEFAULT_API_BASE = os.environ.get("PYYOL_API", "").rstrip("/") or "https://api.pyyol.com"
DEFAULT_DASHBOARD = os.environ.get("PYYOL_DASHBOARD", "").rstrip("/") or "https://pyyol.com"
# Verified-tier LLM gateway base (Phase 4). In ranked mode the CLI enables gateway
# routing so `pyyol.route(client)` sends the agent's LLM calls through it for
# server-observed (unfakeable) model/token/cost. Override with $PYYOL_GATEWAY.
DEFAULT_GATEWAY = os.environ.get("PYYOL_GATEWAY", "").rstrip("/") or "https://gateway.pyyol.com"


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


def _status(st: int) -> str:
    """ " (404)" for a real HTTP status, and NOTHING for 0.

    The HTTP helpers return 0 to mean "no response at all" — offline, refused, DNS. Printing
    that verbatim gave developers "could not fetch leaderboard (0)", where the one number on
    the line is fake and the reader's first thought is that zero is a status code they should
    look up. The cause is already in the message that follows it.
    """
    return f" ({st})" if st else ""


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


# The scheme guard lives in _urlguard so the CLI, the agent runtime and the telemetry client
# all share one implementation — see that module for why the check sits at the open.
from ._urlguard import UnsafeURLError  # noqa: E402
from ._urlguard import urlopen as _urlguard_urlopen  # noqa: E402


def _urlopen(req, timeout):
    """Guarded urlopen; an unsafe scheme becomes a CLI-shaped message, not a traceback."""
    try:
        return _urlguard_urlopen(req, timeout)
    except UnsafeURLError as e:
        raise SystemExit(f"{e}\nCheck --api, PYYOL_API, or your saved config.") from e


def _request(
    url: str,
    method: str,
    secret: str,
    payload: dict[str, Any] | None,
    sign_path: str | None = None,
) -> tuple[int, dict[str, Any]]:
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
    headers: dict[str, str] = {}
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
        with _urlopen(req, timeout=10) as resp:
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
    checks: list[tuple[str, bool, str]] = []

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
            f"simulate runs a full in-process match for goofspiel only (got {args.game!r}). "
            f"For {args.game}, iterate with `pyyol dev` — sandbox practice vs house agents, "
            f"no stakes, no publish needed.",
            file=sys.stderr,
        )
        return 2

    # No --url → run the match IN-PROCESS against the agent from pyyol.toml (parity
    # with the JS CLI). Fast, offline, no endpoint — the quickest way to smoke-test
    # a decision loop. --url keeps the legacy "drive a hosted HTTP endpoint" mode.
    if not args.url:
        from .simulator import SimulationError, simulate_goofspiel

        cfg = _load_config_or_die()
        if cfg is None:
            return 2
        try:
            agent = _load_agent_from_config(cfg)
        except Exception as e:  # noqa: BLE001
            print(f"{BAD} could not load your agent: {e}", file=sys.stderr)
            return 2
        try:
            r = simulate_goofspiel(agent, hand_size=args.hand, seed=args.seed)
        except SimulationError as e:
            print(f"{BAD} simulate failed: {e}", file=sys.stderr)
            return 1
        s = r.get("scores", {})
        print(
            f"simulate goofspiel ({args.hand} rounds, seed {args.seed}): "
            f"winner={r.get('winner')} scores {s}"
        )
        return 0

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

    creds = credentials.load() if getattr(args, "api", "") else _ensure_login(args)
    api = (args.api or (creds.url if creds else "")).rstrip("/")
    agent = args.agent or (creds.agent_id if creds else "")
    # Refreshed, not read raw: see _owner_token. An expired JWT here is what made
    # `pyyol publish` fail hours after a successful login.
    token = _owner_token(creds, args.token)
    if _already_reported(creds):
        return 2
    if not (api and agent and token):
        print(
            f"{BAD} publish needs a signed-in device with an agent. "
            "Run `pyyol login`, then `pyyol init <dir>` if you have no agent yet.",
            file=sys.stderr,
        )
        return 2
    if args.token:
        _warn_argv_secret()
    with open(args.manifest, "rb") as f:
        manifest = f.read()

    import urllib.error
    import urllib.request

    _warn_insecure_transport(api, bool(token))
    agent_q = urllib.parse.quote(agent, safe="")  # never interpolate a raw id into the path

    def api_req(method: str, path: str, body: bytes | None, ctype: str = "application/json"):
        req = urllib.request.Request(
            api + path,
            data=body,
            method=method,
            headers={"Authorization": "Bearer " + token, "Content-Type": ctype},
        )
        try:
            with _urlopen(req, timeout=15) as resp:
                raw = resp.read()
                return resp.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            raw = e.read()
            return e.code, (json.loads(raw) if raw else {"raw": raw.decode(errors="replace")})
        except (urllib.error.URLError, TimeoutError, OSError) as e:
            return 0, {"error": _net_err(e)}

    st, m = api_req("POST", f"/v1/agents/{agent_q}/manifest", manifest)
    if st != 201:
        print(f"{BAD} submit failed{_status(st)}: {m}", file=sys.stderr)
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
            print(f"{BAD} set endpoint secret failed{_status(st)}: {r}", file=sys.stderr)
            return 1
        print(f"{OK} endpoint secret stored")

    st, report = api_req("POST", f"/v1/agents/{agent_q}/manifest/{mid}/verify", b"")
    verified = st == 200 and (report.get("verified") or report.get("status") == "verified")
    mark = OK if verified else BAD
    print(f"{mark} verify{_status(st)}: {json.dumps(report)}")
    return 0 if verified else 1


# --- login / logout ------------------------------------------------------------


def _login_and_save(api: str, dashboard: str, connect: str = "", provider: str = ""):
    """Run the browser login flow, mint a persistent agent key, and store creds.
    Shared by `pyyol login` and the auto-login prompt on game commands."""
    from . import credentials, login

    creds = login.run_login_flow(dashboard, api_url=api, provider=provider)
    if connect:
        creds.connect_url = connect
    if not creds.api_key and creds.agent_id and creds.access_token:
        # Label the key after this machine so re-issuing replaces THIS device's key
        # and leaves other machines and deployments connected (migration 0071).
        st, resp = _api_post(
            f"{api}/v1/agent/keys",
            creds.access_token,
            {"agent_id": creds.agent_id, "label": login.device_label()},
        )
        if st == 201 and resp.get("api_key"):
            creds.api_key = resp["api_key"]
    credentials.save(creds)
    return creds


def _already_reported(creds) -> bool:
    """True when _ensure_login has ALREADY told the developer what to do.

    It prints a complete, actionable line ("not logged in on this device. Run `pyyol login`…")
    and returns None. Callers used to print a second line on top of it, in flag language:

        ✗ not logged in on this device. Run `pyyol login` (opens the browser)…
        ✗ need --api, --agent and --token (or `pyyol login` first)

    Two errors for one problem, and the second one is worse — it describes the plumbing rather
    than the fix, and reads as a tool that does not know what went wrong. A caller that sees
    None should exit quietly; the message a developer needs has already been said.
    """
    return creds is None


def _ensure_login(args: argparse.Namespace):
    """Return valid creds for a game/sandbox command, launching the browser login when
    this DEVICE isn't logged in — so `pyyol dev`/`play`/`queue` just work after install.
    Returns None (with guidance) when non-interactive (CI/headless) so the caller errors
    cleanly instead of hanging on a browser that can't open."""
    from . import credentials

    creds = credentials.load()
    if creds is not None and (creds.access_token or creds.api_key):
        return creds
    api = (getattr(args, "api", "") or DEFAULT_API_BASE).rstrip("/")
    dashboard = (getattr(args, "dashboard", "") or DEFAULT_DASHBOARD).rstrip("/")
    if not (sys.stdin.isatty() and sys.stdout.isatty()):
        print(
            f"{BAD} not logged in on this device. Run `pyyol login` (opens the browser) "
            "or set PYYOL_TOKEN, then retry.",
            file=sys.stderr,
        )
        return None
    print("you're not logged in on this device — opening the browser to sign in…")
    try:
        creds = _login_and_save(
            api, dashboard, getattr(args, "connect", "") or "", getattr(args, "provider", "") or ""
        )
    except Exception as e:  # noqa: BLE001
        print(f"{BAD} login failed: {e} — run `pyyol login` and retry.", file=sys.stderr)
        return None
    who = creds.agent_id or "(no agent yet)"
    print(f"{OK} logged in as {who}. continuing…")
    return creds


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
    # Mint a long-lived agent key for THIS machine (unless the dashboard already
    # handed one back). This is the credential the agent connection uses — like an
    # OpenAI/`gh` token, it never expires on a timer, so `pyyol dev`/`serve` keeps
    # working forever until you revoke it or lose the machine.
    #
    # Logging in ELSEWHERE no longer kills it: keys are named per machine and issuing
    # replaces only the matching name (migration 0071). Before that, every login
    # revoked every live key for the agent, so a second machine — or the dashboard
    # button — silently knocked this one offline.
    # Best-effort: if it fails we still store the session and fall back to the
    # short-lived JWT + refresh for the connection.
    if not creds.api_key and creds.agent_id and creds.access_token:
        # Label the key after this machine so re-issuing replaces THIS device's key
        # and leaves other machines and deployments connected (migration 0071).
        st, resp = _api_post(
            f"{api}/v1/agent/keys",
            creds.access_token,
            {"agent_id": creds.agent_id, "label": login.device_label()},
        )
        if st == 201 and resp.get("api_key"):
            creds.api_key = resp["api_key"]
        else:
            print(
                f"note: couldn't mint a persistent agent key ({st}); "
                "using the refreshable session instead.",
                file=sys.stderr,
            )
    backend = credentials.save(creds)
    who = creds.agent_id or "(no agent yet)"
    persist = " · persistent agent key" if creds.api_key else ""
    print(f"{OK} logged in as {who} — credentials stored ({backend}){persist}")
    return 0


def cmd_logout(_args: argparse.Namespace) -> int:
    from . import credentials

    print(f"{OK} logged out" if credentials.clear() else "not logged in")
    return 0


# --- status / logs -------------------------------------------------------------


def cmd_wallet(args: argparse.Namespace) -> int:
    """Show the owner's coin balance + per-agent playing wallets, so a developer can
    see why ranked play was refused ("not enough coins") without leaving the CLI."""
    creds = _ensure_login(args)
    if creds is None or not creds.access_token:
        return 2
    base = _http_base(args, creds)
    st, w = _api_get(f"{base}/v1/user/wallet", creds.access_token)
    if st != 200:
        print(f"{BAD} could not fetch wallet{_status(st)}: {w}", file=sys.stderr)
        return 1
    if args.json:
        print(json.dumps(w, indent=2))
        return 0
    cents = w.get("coin_cents") or 1
    avail = int(w.get("available_balance", 0) or 0)

    def _usd(coins: int) -> str:
        return f"${(coins * cents) / 100:,.2f}"

    print("Treasury")
    print(f"  Available   {avail:,} coins  ({_usd(avail)})")
    if w.get("locked_balance"):
        print(f"  Locked      {int(w['locked_balance']):,} coins (in active matches)")
    if w.get("lifetime_earnings"):
        print(f"  Earned      {int(w['lifetime_earnings']):,} coins (lifetime)")
    agents = w.get("agents") or []
    if agents:
        print("\nAgent wallets")
        for ag in agents:
            wd = int(ag.get("withdrawable", 0) or 0)
            print(
                f"  {str(ag.get('name') or ag.get('agent') or '?'):<20} "
                f"{int(ag.get('balance', 0) or 0):>10,} coins   withdrawable {wd:,}"
            )
    return 0


def cmd_status(args: argparse.Namespace) -> int:
    import urllib.error
    import urllib.request

    creds = _ensure_login(args)
    if creds is None or not creds.access_token:
        return 2
    api = (args.api or creds.url).rstrip("/")
    agent_id = args.agent or creds.agent_id
    if not api or not agent_id:
        if _already_reported(creds):
            return 2
        print(
            f"{BAD} no agent on this device yet — run `pyyol init <dir>` to create one.",
            file=sys.stderr,
        )
        return 2
    _warn_insecure_transport(api, True)
    req = urllib.request.Request(
        f"{api}/v1/agent/status?agent_id={urllib.parse.quote(agent_id)}",
        headers={"Authorization": "Bearer " + creds.access_token},
    )
    try:
        with _urlopen(req, timeout=10) as resp:
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
    # errors="replace", not just encoding="utf-8". A log is arbitrary agent output: it can
    # hold a half-written line from a killed process, or bytes from an agent that logged in
    # some other encoding. `pyyol logs` exists to show a developer what went wrong, so it is
    # the one command that must never itself crash — a mojibake character beats a traceback.
    with open(path, encoding="utf-8", errors="replace") as f:
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

    # Watching a live match is one of the first things a new developer tries, so it
    # prompts to sign in rather than turning them away with an error — same treatment
    # as `dev`/`play`. Passing --api explicitly still skips the prompt entirely.
    creds = credentials.load() if getattr(args, "api", "") else _ensure_login(args)
    base = _http_base(args, creds)
    if not base:
        if _already_reported(creds):
            return 2
        print(f"{BAD} no arena to talk to — run `pyyol login`, or pass --api.", file=sys.stderr)
        return 2
    game = args.game

    # `--list`: show the admin-configured stake tiers for the game (public) and exit.
    if args.list:
        st, resp = _api_get(f"{base}/v1/games/{game}/stakes")
        if st != 200:
            print(f"{BAD} could not fetch tiers{_status(st)}: {resp}", file=sys.stderr)
            return 1
        tiers = resp.get("tiers") or []
        if not tiers:
            print(f"no stake tiers configured for {game} — use --bid <coins>")
            return 0
        print(f"{game} stake tiers:")
        for t in tiers:
            print(
                f"  {str(t.get('key', '')):8} {int(t.get('coins', 0)):>8} coins  {t.get('label', '')}"
            )
        return 0

    # Queuing is an AGENT action, so it needs the AGENT key.
    #
    # /v1/queue is registered with RequireScope(ScopeAgent). This sent the dashboard
    # session token instead, so every ranked queue attempt came back
    # `forbidden_scope: This credential is not allowed to access this resource` — for
    # every developer, every time. It is the command the scaffold prints as THE way to
    # play ranked ("pyyol queue <game> --tier low"), so ranked matchmaking was
    # unreachable from the CLI.
    #
    # _connection_token already encodes the right preference (agent key first, falling
    # back to the dashboard JWT) and is what the play/dev commands use to reach the same
    # agent-scoped surface.
    token, _ = _connection_token(args, creds)
    if not token:
        creds = _ensure_login(args)
        if creds is None:
            return 2
        token, _ = _connection_token(args, creds)

    body: dict[str, object] = {"game": game}
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

    queue_path = queue_path_for(game)
    st, resp = _api_post(f"{base}{queue_path}", token, body)
    if st not in (200, 202):
        code = str(resp.get("code") or resp.get("error") or "")
        msg = resp.get("message") or ""
        if "certified" in code:
            print(
                f"{BAD} agent not certified — run `pyyol publish` to verify your endpoint first.",
                file=sys.stderr,
            )
        elif "tier" in code:
            print(f"{BAD} {msg or code} — see `pyyol queue --list`", file=sys.stderr)
        elif "balance" in code or "insufficient" in code:
            print(
                f"{BAD} not enough coins to stake this tier (or below your min balance).",
                file=sys.stderr,
            )
        else:
            print(f"{BAD} could not queue{_status(st)}: {resp}", file=sys.stderr)
        return 1

    print(
        f"{OK} queued for {game}. Keep your agent connected (`pyyol run`) — it plays automatically when matched."
    )
    deadline = time.time() + args.wait
    while time.time() < deadline:
        st, s = _api_get(f"{base}{queue_path}", token)
        if st == 200 and s.get("status") == "matched":
            mid = s.get("match_id") or ""
            print(f"{OK} matched → {mid}")
            if mid:
                print(f"    watch it:  pyyol watch {mid}")
            return 0
        time.sleep(1.5)
    print("still waiting for an opponent — leave `pyyol run` connected; check `pyyol status`.")
    return 0


def cmd_room(args: argparse.Namespace) -> int:
    """Create or join a PRIVATE staked table, shared by its id.

    The queue supplies whoever is waiting. A room is for the other case: two developers
    who want THEIR two agents to play each other. One creates it, sends the id, the other
    joins it.

    Deliberately the same match as everywhere else: same stake path, same escrow, same
    certification gate, same refusal to seat both sides on one account. The only thing a
    room changes is that it is not listed in the open lobby, so the seat cannot be taken
    by a stranger between the moment the code is shared and the moment it is used.
    """
    from . import credentials

    creds = credentials.load() if getattr(args, "api", "") else _ensure_login(args)
    base = _http_base(args, creds)
    if not base:
        if _already_reported(creds):
            return 2
        print(f"{BAD} no arena to talk to — run `pyyol login`, or pass --api.", file=sys.stderr)
        return 2

    # Rooms are AGENT actions, like the queue: /v1/room/create and /v1/lobby/join are
    # both registered with RequireScope(ScopeAgent). Sending the dashboard session token
    # here fails with `forbidden_scope` for the same reason queueing did.
    token, _ = _connection_token(args, creds)
    if not token:
        creds = _ensure_login(args)
        if creds is None:
            return 2
        token, _ = _connection_token(args, creds)

    if args.action == "join":
        if not args.id:
            print(f"{BAD} which room? `pyyol room join <room-id>`", file=sys.stderr)
            return 2
        st, resp = _api_post(f"{base}/v1/lobby/join", token, {"match_id": args.id})
        if st != 200:
            return _room_error(st, resp, "join")
        print(f"{OK} joined room {args.id}")
        print("    keep your agent connected (`pyyol run`) — it plays automatically.")
        print(f"    watch it:  pyyol watch {args.id}")
        return 0

    body: dict[str, object] = {}
    if args.tier:
        body["tier"] = args.tier
    elif args.bid > 0:
        body["bid"] = args.bid
    else:
        print(
            f"{BAD} a room is staked: pass --tier <low|mid|high> "
            f"(see `pyyol queue goofspiel --list`) or --bid <coins>.",
            file=sys.stderr,
        )
        return 2

    st, resp = _api_post(f"{base}/v1/room/create", token, body)
    if st not in (200, 201):
        return _room_error(st, resp, "create")

    room_id = resp.get("room_id") or resp.get("match_id") or ""
    bid = resp.get("bid")
    print(f"{OK} room created")
    if bid:
        print(f"    stake: {bid} coins each")
    # The id gets its own line with nothing around it, because the next thing anyone does
    # is drag-select it to paste into a chat, and a line with prose on it selects badly.
    print()
    print(f"    {room_id}")
    print()
    print("    send that to the other player. they run:")
    print(f"        pyyol room join {room_id}")
    print("    keep your agent connected (`pyyol run`) — it plays as soon as they join.")
    return 0


def _room_error(st: int, resp: dict, what: str) -> int:
    """Turn the arena's refusal codes into something a developer can act on.

    Every branch here is a real first-try failure. The raw JSON says what was refused and
    never what to do about it, which on a staked action is the difference between a retry
    and giving up.
    """
    code = str(resp.get("code") or resp.get("error") or "")
    msg = resp.get("message") or ""
    if "same_owner" in code:
        print(
            f"{BAD} that is your own room — a match needs two different accounts. "
            "Send the id to the other player.",
            file=sys.stderr,
        )
    elif "certified" in code:
        print(
            f"{BAD} agent not certified — run `pyyol publish` to verify your endpoint first.",
            file=sys.stderr,
        )
    elif "balance" in code or "insufficient" in code:
        print(f"{BAD} not enough coins to stake this room.", file=sys.stderr)
    elif "not_found" in code:
        print(f"{BAD} no such room — check the id, or it may have been cancelled.", file=sys.stderr)
    elif "not_waiting" in code:
        print(f"{BAD} that room is no longer open (already started or cancelled).", file=sys.stderr)
    else:
        print(f"{BAD} could not {what} room{_status(st)}: {msg or resp}", file=sys.stderr)
    return 1


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
        with _urlopen(req, timeout=30) as resp:
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
    event: str | None = None
    data: list[str] = []
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


def _retry_after_seconds(headers, attempt: int) -> float:
    """Delay before retrying a 429: honor the server's ``Retry-After`` (seconds,
    capped at 30s); otherwise a short exponential backoff (0.5s, 1s)."""
    ra = headers.get("Retry-After") if headers else None
    if ra:
        try:
            return min(float(int(ra)), 30.0)
        except (TypeError, ValueError):
            pass
    return 0.5 * (2**attempt)


def _urlopen_json(req, timeout: float = 15.0):
    """urlopen → (status, parsed_json), retrying a 429 up to 2× while honoring
    ``Retry-After``. A rate-limited call (e.g. `pyyol publish` → manifest verify)
    then waits and succeeds instead of failing the developer outright."""
    import urllib.error
    import urllib.request

    for attempt in range(3):
        try:
            with _urlopen(req, timeout=timeout) as resp:
                raw = resp.read()
                return resp.status, (json.loads(raw) if raw else {})
        except urllib.error.HTTPError as e:
            if e.code == 429 and attempt < 2:
                time.sleep(_retry_after_seconds(e.headers, attempt))
                continue
            raw = e.read()
            try:
                return e.code, (json.loads(raw) if raw else {})
            except json.JSONDecodeError:
                return e.code, {"raw": raw.decode(errors="replace")}
        except (urllib.error.URLError, TimeoutError, OSError) as e:
            return 0, {"error": _net_err(e)}
    return 0, {"error": "rate_limited"}  # retries exhausted (defensive; unreachable)


def _owner_token(creds, explicit: str = "") -> str:
    """The credential OWNER commands must send: the developer's dashboard JWT.

    Refreshes it first when a refresh token is held, because the access token is
    SHORT-LIVED and every owner command is one a developer runs occasionally rather
    than continuously. `pyyol publish` — the required step before a ranked match — read
    creds.access_token directly, so a developer who logged in in the morning and
    published in the afternoon sent an expired JWT and was told to log in again, on the
    one path that leads to competing for real.

    Best-effort: a failed refresh returns the stored token unchanged, so the command
    still runs and still reports the server's own error rather than a refresh failure
    the developer cannot act on.
    """
    if explicit:
        return explicit
    if not creds:
        return ""
    stored = getattr(creds, "access_token", "") or ""
    refresh = getattr(creds, "refresh_token", "") or ""
    base = (getattr(creds, "url", "") or "").rstrip("/")
    if not (refresh and base):
        return stored
    st, resp = _api_post(f"{base}/v1/auth/refresh", "", {"refresh_token": refresh})
    if st != 200 or not isinstance(resp, dict):
        return stored
    # `dashboard_token` is the field this endpoint actually returns — same key the
    # connector's refresh reads (runtime.py). Getting the name wrong here would not fail
    # loudly: it would fall through to the stored token and the refresh would silently
    # never happen, which is indistinguishable from not having written this at all.
    access = resp.get("dashboard_token") or ""
    if not access:
        return stored
    # Persist the rotated pair so the NEXT command starts from a fresh token instead of
    # refreshing again — and so a rotated refresh token is not thrown away, which would
    # invalidate the session on a server that rotates them.
    from . import credentials as _creds

    creds.access_token = access
    if resp.get("refresh_token"):
        creds.refresh_token = resp["refresh_token"]
    try:
        _creds.save(creds)
    except Exception:  # noqa: BLE001 — persistence is best-effort
        pass
    return access


def _api_get(url: str, token: str = ""):
    import urllib.request

    headers = {}
    if token:
        headers["Authorization"] = "Bearer " + token
        _warn_insecure_transport(url, True)
    return _urlopen_json(urllib.request.Request(url, method="GET", headers=headers))


def _api_post(url: str, token: str, body):
    import urllib.request

    _warn_insecure_transport(url, bool(token))
    data = json.dumps(body).encode() if body else b""
    req = urllib.request.Request(
        url,
        data=data,
        method="POST",
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    )
    return _urlopen_json(req)


# --- run (connect the local agent over WSS) ------------------------------------


def _log_file_handler():
    """Attach a file handler so `pyyol logs` has content. Propagation is off so
    the file is the only logger sink — the live feed is the Console (stdout)."""
    import logging

    from . import credentials

    d = os.path.join(credentials.config_dir(), "logs")
    os.makedirs(d, exist_ok=True)
    # UTF-8 explicitly: an agent logs whatever its model produced, so a log line can hold
    # any character. Without this the handler encodes in the locale's encoding, and a
    # single accented player name raises inside logging itself — which surfaces as
    # "--- Logging error ---" on stderr and a silently truncated log.
    handler = logging.FileHandler(os.path.join(d, "agent.log"), encoding="utf-8")
    handler.setFormatter(logging.Formatter("%(asctime)s %(levelname)s %(message)s"))
    logger = logging.getLogger("pyyol")
    logger.setLevel(logging.INFO)
    logger.propagate = False
    logger.addHandler(handler)


def _connection_token(args, creds):
    """The credential the agent CONNECTION registers with, and whether it's the
    long-lived agent key. Prefer the agent key (``sk_arena_…``, no timer expiry) so
    the connection persists forever — like an OpenAI/`gh` token — falling back to the
    short-lived dashboard JWT (which the connector then auto-refreshes).
    Returns ``(token, using_agent_key)``."""
    explicit = getattr(args, "token", "") or os.environ.get("PYYOL_TOKEN", "")
    if explicit:
        return explicit, explicit.startswith(_AGENT_KEY_PREFIX)
    if creds and getattr(creds, "api_key", ""):
        return creds.api_key, True
    if creds:
        return creds.access_token, False
    return "", False


def _refresh_kwargs(creds) -> dict:
    """Connector kwargs that enable silent access-token refresh from stored creds.

    Returns empty (refresh disabled) unless we hold both a refresh token and the API
    base URL. The ``on_tokens`` callback writes the rotated pair back to the same
    store so the next run/reconnect starts from a fresh token — this is what keeps a
    long-running ``pyyol dev``/``serve`` authenticated past the short access-token TTL."""
    from . import credentials

    if not creds or not getattr(creds, "refresh_token", "") or not getattr(creds, "url", ""):
        return {}

    def _persist(access: str, refresh: str) -> None:
        creds.access_token = access
        if refresh:
            creds.refresh_token = refresh
        credentials.save(creds)

    return {"refresh_token": creds.refresh_token, "api_url": creds.url, "on_tokens": _persist}


# --- forfeit guard --------------------------------------------------------------
#
# Quitting mid-match is a LOSS, not a pause. A staked table keeps running after the
# agent goes away: the server plays a deterministic fallback move for the missing
# seat each turn, so the match finishes and the absent agent loses on merit — its
# entry fee goes to the winner (minus the platform fee), and nothing is refunded.
#
# That is the intended rule, but the SDK used to swallow Ctrl-C with a bare
# "stopped.", so a developer could forfeit real coins with one keystroke and no idea
# it had cost them anything. This asks first.


def _live_staked_matches(api: str, token: str, agent_id: str) -> list:
    """Best-effort: staked matches this agent is currently seated in.

    Deliberately short-timeout and failure-tolerant — this runs on the way out, so
    it must never hang the exit or raise. An empty list means "nothing to warn
    about, as far as we can tell".
    """
    if not (api and token and agent_id):
        return []
    try:
        data = _api_get(
            f"{api.rstrip('/')}/v1/agent/status?agent_id={urllib.parse.quote(agent_id)}",
            token,
        )
    except Exception:  # noqa: BLE001 - never block the exit on a status call
        return []
    if not isinstance(data, dict):
        return []
    out = []
    for m in data.get("active_matches") or []:
        if not isinstance(m, dict):
            continue
        # Only a STAKED table can cost money; practice/sandbox tables are free, so
        # interrupting those needs no warning at all.
        fee = m.get("entry_fee") or m.get("bid") or 0
        try:
            fee = int(fee)
        except (TypeError, ValueError):
            fee = 0
        if fee > 0:
            out.append({"match_id": m.get("match_id") or m.get("id") or "?", "entry_fee": fee})
    return out


def _confirm_forfeit(matches: list) -> bool:
    """Show what quitting costs and require an explicit confirmation.

    Returns True when the developer confirms the forfeit. On a non-interactive
    stdin (CI, piped, nohup) we cannot ask, so we print the warning and allow the
    exit rather than hanging a pipeline forever.
    """
    total = sum(m["entry_fee"] for m in matches)
    plural = "match" if len(matches) == 1 else "matches"
    print("", file=sys.stderr)
    print(
        f"{BAD} you are still playing {len(matches)} staked {plural}.",
        file=sys.stderr,
    )
    for m in matches:
        print(f"    · {m['match_id']} — {m['entry_fee']} coins staked", file=sys.stderr)
    print("", file=sys.stderr)
    print(
        "  Quitting does NOT pause or cancel the game. The table keeps playing and\n"
        "  your seat forfeits every remaining turn, so you LOSE the match and your\n"
        f"  stake ({total} coins) goes to the winner. Nothing is refunded.\n"
        "  Leave this running until the match ends.",
        file=sys.stderr,
    )
    print("", file=sys.stderr)
    if not sys.stdin.isatty():
        print(
            "  (non-interactive shell — exiting anyway; the forfeit above will stand)",
            file=sys.stderr,
        )
        return True
    try:
        answer = input("  Type 'forfeit' to quit and take the loss, or Enter to keep playing: ")
    except (EOFError, KeyboardInterrupt):
        # A second Ctrl-C is an unambiguous "get me out" — honour it.
        print("", file=sys.stderr)
        return True
    return answer.strip().lower() == "forfeit"


def cmd_run(args: argparse.Namespace) -> int:
    """Load the developer's agent object and connect it to the platform over the
    outbound WebSocket. This is the local-runtime path: no inbound endpoint."""
    import importlib.util

    from . import credentials

    # An explicit --url/PYYOL_URL means the caller is wiring transport themselves
    # (CI, self-host); otherwise sign in rather than erroring out.
    creds = (
        credentials.load() if (args.url or os.environ.get("PYYOL_URL", "")) else _ensure_login(args)
    )
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
    token, _using_key = _connection_token(args, creds)
    _log_file_handler()

    # Load the agent module and find the `Agent` instance (var name configurable).
    _add_agent_dir_to_syspath(args.file)
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

    # Normalize whatever the developer exported into an Agent, exactly as
    # `_load_agent_from_config` does for `pyyol dev` / `pyyol play`.
    #
    # `pyyol init` scaffolds an Adapter SUBCLASS, and an Adapter has no .run() — so the
    # scaffold produced by our own quickstart crashed the moment it was handed to
    # `pyyol run`, with an AttributeError that reads like the developer's mistake. Every
    # other load path already normalized; this one alone did a raw getattr.
    #
    # TypeError carries the message naming `entry`, so a genuinely wrong export still gets
    # the friendly explanation rather than a stack trace.
    from .server import as_agent

    try:
        agent = as_agent(agent)
    except TypeError as e:
        print(f"{BAD} {e}", file=sys.stderr)
        return 2

    from .console import build_console

    console = build_console(
        mode="json" if args.json else "pretty",
        quiet=args.quiet,
        color=False if args.no_color else None,
    )
    try:
        agent.run(
            url=url,
            agent_id=agent_id,
            token=token,
            console=console,
            **({} if _using_key else _refresh_kwargs(creds)),
        )
    except KeyboardInterrupt:
        # Ctrl-C during a staked match is a forfeit, so confirm before honouring it.
        live = _live_staked_matches(url, token, agent_id)
        if live and not _confirm_forfeit(live):
            print("  still playing — leave this window open until the match ends.", file=sys.stderr)
            try:
                agent.run(
                    url=url,
                    agent_id=agent_id,
                    token=token,
                    console=console,
                    **({} if _using_key else _refresh_kwargs(creds)),
                )
            except KeyboardInterrupt:
                print("\nstopped (match forfeited).", file=sys.stderr)
            return 0
        print("\nstopped." if not live else "\nstopped (match forfeited).")
    return 0


# --- serve / autoplay (deploy once, plays anytime) -----------------------------


def _add_agent_dir_to_syspath(file: str) -> None:
    """Put the agent's own directory on sys.path before importing it.

    Loading by file path does NOT add the file's directory to sys.path, so any agent
    split across more than one module failed with ModuleNotFoundError on its own
    package — and `pyyol doctor` reported a bare "agent loads ✗" with no hint why.
    That effectively limited developers to single-file agents.

    Inserted at the front so the agent's own modules win over same-named installed
    packages, which is what a developer running from their project directory expects.
    """
    d = os.path.dirname(os.path.abspath(file))
    if d and d not in sys.path:
        sys.path.insert(0, d)


def _load_agent(file: str, var: str):
    """Import the developer's module and return the exposed Agent object (or None
    after printing why). Shared by `serve`."""
    import importlib.util

    _add_agent_dir_to_syspath(file)
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


def _autoplay_set(
    api: str, token: str, *, enabled: bool, mode: str, bid: int, games: list
) -> tuple:
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
        with _urlopen(req, timeout=15) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        return e.code, (json.loads(raw) if raw else {})
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return 0, {"error": _net_err(e)}


def _autoplay_get(api: str, token: str) -> tuple:
    """GET the agent's auto-play setting + last observed status. Returns (status, body)."""
    import urllib.error
    import urllib.request

    req = urllib.request.Request(
        api.rstrip("/") + "/v1/agent/autoplay",
        method="GET",
        headers={"Authorization": "Bearer " + token},
    )
    try:
        with _urlopen(req, timeout=15) as resp:
            raw = resp.read()
            return resp.status, (json.loads(raw) if raw else {})
    except urllib.error.HTTPError as e:
        raw = e.read()
        return e.code, (json.loads(raw) if raw else {})
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        return 0, {"error": _net_err(e)}


# How each reconciler status reads to a developer, and its marker.
_AUTOPLAY_STATUS_LABEL = {
    "playing": (OK, "playing"),
    "searching": (OK, "searching for an opponent"),
    "paused": (WARN, "paused"),
    "blocked": (BAD, "not playing"),
}


def _print_autoplay_status(body: dict) -> None:
    """Render `pyyol autoplay status` — is it on, and (crucially) WHY it is or isn't
    playing, so a quiet auto-play agent is never a mystery."""
    if not body.get("enabled"):
        print(f"{WARN} auto-play is OFF (turn it on with `pyyol autoplay on`)")
        return
    mode = body.get("mode", "sandbox")
    print(f"{OK} auto-play is ON — mode={mode}")
    status = body.get("last_status", "")
    reason = body.get("last_status_reason", "")
    if not status:
        print("  status: starting up — no activity recorded yet (check back in a moment)")
        return
    marker, label = _AUTOPLAY_STATUS_LABEL.get(status, (WARN, status))
    line = f"  {marker} {label}"
    if reason:
        line += f" — {reason}"
    print(line)
    if at := body.get("last_status_at"):
        print(f"  as of {at}")
    if status == "blocked":
        print(
            "  fix the reason above (e.g. connect your agent with `pyyol run`), and it resumes automatically."
        )


def _autoplay_opts(args, cfg) -> tuple:
    """Resolve (mode, games) from flags → pyyol.toml → defaults."""
    mode = (
        "ranked"
        if getattr(args, "ranked", False)
        else (args.mode or (cfg.mode if cfg else "") or "sandbox")
    )
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
    token, _using_key = _connection_token(args, creds)
    agent_id = (
        args.agent or os.environ.get("PYYOL_AGENT_ID", "") or (creds.agent_id if creds else "")
    )
    if not (api and token):
        print(f"{BAD} run `pyyol login` first (need the API base + token)", file=sys.stderr)
        return 2

    cfg = config.load()
    mode, games = _autoplay_opts(args, cfg)
    agent = _load_agent(args.file, args.var)
    if agent is None:
        return 2

    # Normalize an Adapter subclass into something with .run().
    #
    # `pyyol init` scaffolds an Adapter SUBCLASS and an Adapter has no .run(), so handing
    # one to `serve` raised `AttributeError: 'X' object has no attribute 'run'` — an
    # internal-error banner that reads like the developer broke something, on the agent
    # our own quickstart wrote for them.
    #
    # This is the SAME defect already fixed for `pyyol run`, whose comment notes that
    # "every other load path already normalized; this one alone did a raw getattr".
    # `serve` was the one it missed — and it is the command that plays ranked, so the
    # crash landed at the end of the setup rather than the start.
    from .server import as_agent

    try:
        agent = as_agent(agent)
    except TypeError as e:
        # TypeError names `entry`, so a genuinely wrong export still gets the friendly
        # explanation instead of a stack trace.
        print(f"{BAD} {e}", file=sys.stderr)
        return 2

    _warn_insecure_transport(api, bool(token))
    st, resp = _autoplay_set(api, token, enabled=True, mode=mode, bid=args.bid, games=games)
    if st and 200 <= st < 300:
        extra = f", bid={args.bid}" if mode == "ranked" else ""
        print(f"{OK} auto-play ON — mode={mode}{extra}, games={games or 'default'}")
    else:
        print(
            f"{BAD} could not enable auto-play (status {st}: {resp}); holding the connection anyway",
            file=sys.stderr,
        )

    print("serving — the platform will drive your agent as matches are paired. Ctrl-C to stop.")
    _log_file_handler()
    from .console import build_console

    console = build_console(
        mode="json" if args.json else "pretty",
        quiet=args.quiet,
        color=False if args.no_color else None,
    )
    try:
        agent.run(
            url=url,
            agent_id=agent_id,
            token=token,
            console=console,
            **({} if _using_key else _refresh_kwargs(creds)),
        )
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
    # Auto-play is agent-scoped (/v1/agent/autoplay), so it needs the agent key.
    token, _ = _connection_token(args, creds)
    if not (api and token):
        print(f"{BAD} run `pyyol login` first", file=sys.stderr)
        return 2
    _warn_insecure_transport(api, bool(token))
    # `pyyol autoplay status` READS the current state + why it is/isn't playing.
    if args.state == "status":
        st, resp = _autoplay_get(api, token)
        if st and 200 <= st < 300:
            _print_autoplay_status(resp)
            return 0
        print(f"{BAD} failed (status {st}): {resp}", file=sys.stderr)
        return 1
    on = args.state == "on"
    cfg = config.load()
    mode, games = _autoplay_opts(args, cfg)
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
        #
        # Driving moves with an LLM? Capture the real model/tokens/cost for free:
        #   import pyyol; pyyol.instrument()   # once at the top of this file
        #   client = pyyol.route(OpenAI())     # in ranked, routes via the gateway (verified)
        # then call `client` here. See docs -> "Verified LLM agents".
        #
        # TALK IS FREE IF IT RIDES ON THE MOVE. Set `rationale` and your opponent reads it,
        # spectators watch it, and the replay keeps it — no extra model call, because it
        # travels with the move you are already returning:
        #
        #     return GoofspielMove(card=..., round=..., rationale="I need the 13 later")
        #
        # Calling self.say() instead costs a WHOLE extra call per round — 26 for a 13-round
        # match instead of 13. On a free tier of 50 requests/day that is the difference
        # between about two matches and about four. Use say() when you want to speak
        # WITHOUT playing (reacting mid-round); it just should not be how you narrate a move
        # you are already making.
        #
        # ONE CALL PER DECISION, not per event. The SDK hands you a complete view here —
        # every past round, the whole chat — so you never need to reason on `/event`
        # notifications as they arrive. An agent that calls its model on each event instead
        # multiplies its bill by the number of messages in the phase and will hit a free
        # tier's limit long before the match ends.
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
        # Driving moves with an LLM? `import pyyol; pyyol.instrument()` once + in ranked
        # `client = pyyol.route(client)` captures the real model/tokens/cost (verified).
        # See docs -> "Verified LLM agents".
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
    # The model you run. Optional to the schema, but scaffolded because leaving it out
    # is how an agent ends up missing from the model benchmark: the board falls back to
    # this block for any match where neither the gateway nor the SDK saw the real model
    # name, and with no block there is nothing to fall back to. Placeholders, so an
    # unedited manifest cannot silently claim a model it does not run.
    "model": {"provider": "your-provider", "model": "your-model", "reasoning": False},
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
    # UTF-8, not the locale's encoding. The starter templates contain an em dash, so on any
    # machine whose preferred encoding is not UTF-8 this raised UnicodeEncodeError and
    # `pyyol init` — the very first command a developer runs — died. Verified failing under
    # LC_ALL=C with coercion off, and under a latin-1 locale, on Linux as well as Windows.
    with open(path, "w", encoding="utf-8") as f:
        f.write(code)

    # A manifest scaffold, because ranked REQUIRES one and there was no way to get a
    # correct schema: _MANIFEST_TMPL below was defined and never referenced, so the
    # only accurate copy of the schema in the whole product was dead code. Developers
    # had to reverse-engineer it from the source or guess.
    #
    # Written with placeholders rather than left out: the endpoint URL is the one
    # field only the developer can supply, and seeing it named makes the hosted-
    # endpoint requirement obvious at scaffold time rather than at the 403.
    manifest = json.loads(json.dumps(_MANIFEST_TMPL))  # deep copy — never mutate the template
    manifest["agent"]["name"] = name
    manifest["games"] = [arena]
    manifest["sdk"]["language"] = lang
    # Scaffold CONNECTED-RANKED: no endpoint. A placeholder URL here would be worse
    # than none — it validates, gets probed, fails, and the developer debugs a host
    # they never intended to run. Add a real URL only when you want always-on play.
    manifest.pop("endpoint", None)
    manifest_path = os.path.join(d, "manifest.json")
    with open(manifest_path, "w", encoding="utf-8") as f:
        json.dump(manifest, f, indent=2)
        f.write("\n")

    # Convention-over-configuration: a tiny pyyol.toml.
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
    print(f"    {manifest_path}   (for ranked — see below)")
    print("\nNext:")
    print("    pip install pyyol" if lang == "python" else "    npm install pyyol")
    print(f"    cd {d} && pyyol dev            # practice locally (sandbox — no stakes)")
    print(f"    pyyol play {arena}             # compete (sandbox)")
    print("\nTo play ranked for real coins — no hosting needed:")
    print("    pyyol publish --manifest manifest.json   # certifies you; no endpoint required")
    print(
        f"    pyyol queue {arena} --tier low            # keep it running; it plays automatically"
    )
    print("\n    Your agent must stay CONNECTED to play ranked this way.")
    print("    Want it to play while you're away? Add a hosted https endpoint to")
    print("    manifest.json and re-publish:  https://pyyol.com/docs/deploy.md")
    print("\n    Set your limits first:  https://pyyol.com/guardrails")
    print("    (stop-loss, max bid, daily cap — server-enforced)")
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
    _add_agent_dir_to_syspath(module_path)
    spec = importlib.util.spec_from_file_location("_pyyol_user_agent", module_path)
    if spec is None or spec.loader is None:
        raise ImportError(f"cannot load {module_path}")
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    obj = getattr(mod, var, None)
    if obj is None:
        raise AttributeError(f"no `{var}` in {module_path} (see pyyol.toml `entry`)")
    return as_agent(obj)


# How long a counted run (`--matches N`) waits with nothing finishing before it gives
# up. Generous enough to cover a slow model plus a reconnect, short enough that a
# scripted benchmark cannot hang a CI job.
_COUNTED_RUN_IDLE_TIMEOUT_S = 300.0


def _orchestrate(args: argparse.Namespace, *, dev_locked: bool) -> int:
    """Shared engine behind `pyyol dev` (develop) and `pyyol play` (compete): resolve
    mode, connect the agent over WSS, and drive matches — hiding all transport."""
    import threading

    from . import config as cfgmod
    from . import mode
    from .console import build_console
    from .runtime import RuntimeConnector

    cfg = _load_config_or_die()
    if cfg is None:
        return 2
    # Auto-login on this device if needed: a first-time user who installed the SDK and
    # ran `pyyol dev`/`play` gets the browser sign-in, then plays — no separate step.
    creds = _ensure_login(args)
    if creds is None:
        return 2

    connect_url = (
        args.url or os.environ.get("PYYOL_URL", "") or (creds.connect_url if creds else "")
    )
    base = _http_base(args, creds)
    # The agent id MUST match the token's owner. The token comes from creds, so
    # creds.agent_id (its matched pair) wins over a possibly-stale pyyol.toml pin —
    # else the socket's "key.agent == claimed agent_id" check rejects the register.
    agent_id = args.agent or (creds.agent_id if creds else "") or cfg.agent_id
    token, _using_key = _connection_token(args, creds)
    if not connect_url or not agent_id:
        print(
            f"{BAD} missing connect URL or agent id — run `pyyol login` (or pass --url/--agent).",
            file=sys.stderr,
        )
        return 2

    arena = getattr(args, "arena", "") or cfg.arena
    m = mode.resolve(
        ranked_flag=getattr(args, "ranked", False), cfg_mode=cfg.mode, dev_locked=dev_locked
    )
    print(mode.banner(m))

    # Real-stakes guardrails: explicit opt-in confirmation. Certification is enforced
    # server-side at enqueue (we surface a friendly message if it's missing).
    if m == mode.RANKED:
        if not mode.confirm_ranked(assume_yes=getattr(args, "yes", False)):
            print("aborted — staying safe. (Use --yes in CI to skip the prompt.)")
            return 1

    # Ranked → enable verified-tier gateway routing. Only when the connection token
    # is an agent key (sk_arena_…): the gateway authenticates X-Pyyol-Key via that
    # key. With this on, `pyyol.route(client)` sends the agent's LLM calls through the
    # gateway so model/token/cost are server-observed (unfakeable). A dashboard-JWT
    # session can't authenticate to the gateway, so routing stays off there.
    if m == mode.RANKED:
        if _using_key and token:
            from . import _instrument

            _instrument.enable_gateway(token, DEFAULT_GATEWAY)
            print(
                f"{OK} verified gateway routing on ({DEFAULT_GATEWAY}) — call pyyol.route(client)"
            )
        else:
            # Don't silently run unverified: the dev thinks they're competing verified.
            print(
                f"{BAD} verified gateway routing OFF — no agent key in this session "
                "(a dashboard-JWT login can't authenticate to the gateway). Run "
                "`pyyol login` to mint an agent key; your ranked LLM cost won't be verified."
            )

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
        agent,
        url=connect_url,
        agent_id=agent_id,
        token=token,
        name=agent.name,
        games=agent.supported_games,
        console=console,
        **({} if _using_key else _refresh_kwargs(creds)),
    )
    stop = threading.Event()

    # Stop after the requested number of matches.
    #
    # `--matches N` started N matches and then sat in "waiting for a match…" forever,
    # because conn.run() blocks serving turns and nothing counted completions. Every
    # scripted benchmark or CI job needed an external kill plus a game_end-counting
    # watchdog — which is exactly the bookkeeping the flag exists to do for you.
    #
    # Only armed when the developer asked for a specific count. Without --matches the
    # runner is a long-lived dev loop and must keep waiting, as before.
    wanted = getattr(args, "matches", None)
    if m != mode.RANKED and wanted and wanted > 0:
        finished = {"n": 0, "last": time.time()}
        prior = agent._on_game_end  # may be None; the developer's own handler

        def _count_and_forward(result):
            try:
                if prior:
                    return prior(result)
            finally:
                finished["n"] += 1
                finished["last"] = time.time()
                if finished["n"] >= wanted:
                    console.emit("match", f"completed {wanted} match(es) — stopping")
                    stop.set()
                    # Close the socket from another thread so the blocking run()
                    # returns; calling it inline would tear down the connection while
                    # this very notification is still being handled.
                    threading.Timer(0.5, conn.stop).start()

        agent.on_game_end(_count_and_forward)

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
            _start_sandbox(
                base, token, arena, console, attempt_label=f"{i + 1}/{matches}", args=args
            )
            time.sleep(2.0)

        # Watchdog for the counted run.
        #
        # A game_end frame only arrives if the agent is CONNECTED when the match ends.
        # A reconnect can therefore lose one, and counting completions alone would then
        # wait forever for a match that already finished — the exact hang --matches
        # exists to remove, reintroduced by a dropped frame. So once everything has
        # been started, stop after a stretch of silence instead of trusting the count.
        if wanted:
            # Silence since the last frame, NOT time since the run began.
            #
            # This counter used to start at zero and only ever climb, so a run was
            # capped at a flat 300 seconds however busy it was. Goofspiel and Mafia
            # finish inside that and never noticed. A Monopoly match does not: it was
            # cut off mid-play every single time, always at five minutes, and the CLI
            # reported it as "nothing finished" — which read as a broken match rather
            # than a stopwatch. No Monopoly game could be played to completion.
            #
            # conn.last_activity is stamped on every lifecycle event, so a game that is
            # producing turns keeps the countdown pinned no matter how long it runs, and
            # a genuinely dead connection still gives up on schedule. That is what the
            # comment above always said this did.
            while not stop.is_set():
                time.sleep(2.0)
                if time.monotonic() - conn.last_activity >= _COUNTED_RUN_IDLE_TIMEOUT_S:
                    break
            if not stop.is_set():
                # Say SILENCE, not "no match finished". The old wording described a
                # long healthy game as a failure to finish, which sent people looking
                # at their agent when the connection had simply gone quiet.
                console.emit(
                    "match",
                    "stopping: nothing heard from the arena for "
                    f"{int(_COUNTED_RUN_IDLE_TIMEOUT_S)}s — the match may still be "
                    "running without this agent. `pyyol replay` is authoritative.",
                )
                stop.set()
                threading.Timer(0.5, conn.stop).start()

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


# The route table moved to console._WATCH_ROUTE — the runtime needs the same routes and
# cannot import this module. Two copies would surface as the platform sending an honest
# developer to somebody else's live match.


def _watch_url(dashboard: str, arena: str, match_id: str) -> str:
    """Browser URL for a specific live match, or "" when it cannot be named exactly.

    Delegates to console.watch_url, which is the single copy — the runtime needs the same
    routes and cannot import this module. Kept as a thin wrapper because the CLI passes
    the dashboard positionally and first.
    """
    from .console import watch_url

    return watch_url(arena, match_id, dashboard)


# Only the FIRST match of a run opens a tab. Sandbox iteration means dozens of matches
# per session, and a browser tab per match is not a feature — it is something you
# learn to dread. After the first, the link is printed and the developer clicks when
# they want it. `--open` forces every match; `--no-open` suppresses entirely.
_opened_once = {"done": False}

# Where the developer said they want to watch, asked ONCE per run and remembered.
#
# Once, for two reasons. The obvious one: being asked the same question before every
# match of a sandbox loop is the thing you learn to dread. The load-bearing one: a
# prompt that timed out has left a reader on stdin, so asking again would find its
# answer swallowed by the previous question. See console.ask_watch.
_watch_choice: dict[str, str] = {}


def _resolve_watch(console, args, url: str, label: str) -> str:
    """The developer's watch choice for this run — asked at most once.

    `--watch` short-circuits the question entirely, which is what makes this safe in a
    script: a flag means the answer is already known, so nothing reads stdin at all.
    """
    flag = getattr(args, "watch", "ask") or "ask"
    if flag != "ask":
        return flag
    if "value" not in _watch_choice:
        from .console import ask_watch  # lazy, like every other console import here

        _watch_choice["value"] = ask_watch(label, url)
    return _watch_choice["value"]


def _announce_match(console, args, arena: str, match_id: str, label: str = "") -> None:
    """Report a started match and hand the developer a way to watch it.

    The terminal keeps streaming either way — this only adds the route into the UI,
    which previously did not exist at all: the CLI printed a match id and left you to
    find the game yourself.
    """
    console.emit("match", f"started {arena} match {match_id} {label}".rstrip())

    dashboard = (getattr(args, "dashboard", "") or DEFAULT_DASHBOARD).rstrip("/")
    url = _watch_url(dashboard, arena, match_id)
    if not url:
        return

    mode = getattr(args, "open_browser", "auto")
    if mode == "never":
        console.emit("match", f"watch it live: {url}")
        return

    # Ask before taking over the screen. The link is printed either way, so a developer
    # who picks the terminal still has the URL when they change their mind — the choice
    # is about what happens WITHOUT them clicking, not about what they are told.
    choice = _resolve_watch(console, args, url, f"{arena} · {match_id}")
    console.emit("match", f"watch it live: {url}")
    if choice != "browser":
        return

    should_open = mode == "always" or not _opened_once["done"]
    if not should_open:
        return
    # Never in CI/headless: a browser that cannot open would print a stack trace over
    # the match log for no benefit.
    if not (sys.stdout.isatty() and sys.stdin.isatty()):
        return
    _opened_once["done"] = True
    try:
        import webbrowser

        if webbrowser.open(url):
            console.emit("match", "opened it in your browser — logs keep streaming here")
    except Exception:  # noqa: BLE001 — the link is already printed; opening is a bonus
        pass


def _start_sandbox(base, token, arena, console, attempt_label="", args=None) -> None:
    path = _PLAY_PATH.get(arena, _PLAY_PATH["goofspiel"])
    last = {}
    for _ in range(6):  # ~9s: wait for the socket to be registered before starting
        st, resp = _api_post(f"{base}{path}", token, {})
        if st in (200, 201):
            mid = resp.get("match_id") or resp.get("id") or ""
            _announce_match(console, args or argparse.Namespace(), arena, mid, attempt_label)
            return
        last = resp
        code = str(resp.get("code") or resp.get("error") or "")
        if "transport" in code or "no_agent" in code or st in (409, 425):
            time.sleep(1.5)
            continue
        break
    console.emit("error", f"could not start {arena} match: {last}")


def _start_ranked(base, token, arena, args, console) -> None:
    body: dict[str, object] = {"game": arena}
    tier = getattr(args, "tier", "") or "low"
    body["tier"] = tier
    st, resp = _api_post(f"{base}{queue_path_for(arena)}", token, body)
    if st in (200, 202):
        # The queue can pair instantly, in which case the response already names the
        # match — link it, exactly like sandbox. Ranked is where real coins are on the
        # table, so being able to watch it immediately matters more here, not less.
        mid = str(resp.get("match_id") or "")
        if mid:
            _announce_match(console, args, arena, mid, f"RANKED · tier {tier}")
        else:
            console.emit(
                "match", f"queued for RANKED {arena} (tier {tier}) — you play when matched"
            )
        return
    code = str(resp.get("code") or resp.get("error") or "")
    if "certified" in code:
        console.emit(
            "error",
            "agent not certified for ranked — run `pyyol publish` first (ranked needs a verified endpoint).",
        )
    else:
        console.emit("error", f"could not queue ranked{_status(st)}: {resp}")


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
    # Persistent agent key ⇒ the connection never needs re-login (revoke/PC-change
    # only); otherwise the session rides the refreshable dashboard token.
    print(f"session   {'persistent agent key' if creds.api_key else 'refreshable token'}")
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
        print(
            f"{BAD} could not fetch arenas{_status(st)}: {resp.get('error') or resp}",
            file=sys.stderr,
        )
        return 1
    arenas = resp.get("arenas") or []
    print(f"{'ARENA':<12}{'PLAYERS':<10}{'SANDBOX':<9}{'RANKED':<8}STATUS")
    for a in arenas:
        players = f"{a.get('min_players')}-{a.get('max_players')}"
        print(
            f"{a.get('id', ''):<12}{players:<10}"
            f"{('yes' if a.get('sandbox') else 'no'):<9}"
            f"{('yes' if a.get('ranked') else 'no'):<8}{a.get('status', '')}"
        )
    return 0


def cmd_games(args: argparse.Namespace) -> int:
    """Show every game with how many matches are live and how many agents are playing
    or waiting for an opponent — so you know where the action is before you queue."""
    from . import credentials

    base = _http_base(args, credentials.load())
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`.", file=sys.stderr)
        return 2
    st, resp = _api_get(f"{base}/v1/games")
    if st != 200:
        print(
            f"{BAD} could not fetch games{_status(st)}: {resp.get('error') or resp}",
            file=sys.stderr,
        )
        return 1
    games = resp.get("games") or []
    if not games:
        print("no games available.")
        return 0

    print(f"  {'GAME':<11}{'LIVE':>6}{'PLAYING':>9}{'WAITING':>9}   STATUS")
    print(f"  {'─' * 44}")
    total_live = total_wait = 0
    for g in games:
        name = g.get("game", "?")
        live = int(g.get("live", 0))
        playing = int(g.get("playing", 0))
        waiting = int(g.get("waiting", 0))
        total_live += live
        total_wait += waiting
        if live > 0:
            status = f"{OK} {live} live"
        elif waiting > 0:
            status = f"{waiting} waiting — queue to start"
        else:
            status = "quiet — be the first"
        print(f"  {name:<11}{live:>6}{playing:>9}{waiting:>9}   {status}")
    print(f"  {'─' * 44}")
    if total_live == 0 and total_wait == 0:
        print("  nothing running right now — `pyyol queue <game>` to open a table.")
    else:
        print(
            f"  {total_live} live match(es), {total_wait} agent(s) waiting. `pyyol queue <game>` to join."
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
            print(f"{r.get('rank', ''):<5}{who:<24}{r.get('p_index', '')}")
        return 0 if st == 200 else 1
    q = []
    if args.game:
        q.append(f"game={urllib.parse.quote(args.game)}")
    if args.season:
        q.append(f"season={args.season}")
    url = f"{base}/v1/leaderboard" + (("?" + "&".join(q)) if q else "")
    st, resp = _api_get(url)
    if st != 200:
        print(
            f"{BAD} could not fetch leaderboard{_status(st)}: {resp.get('error') or resp}",
            file=sys.stderr,
        )
        return 1
    rows = resp.get("entries") or []
    print(f"{'#':<5}{'AGENT':<24}{'ELO':<7}W-L-T")
    for r in rows:
        wlt = f"{r.get('wins', 0)}-{r.get('losses', 0)}-{r.get('ties', 0)}"
        print(
            f"{r.get('rank', ''):<5}{(r.get('name') or r.get('slug') or '?'):<24}{r.get('elo', ''):<7}{wlt}"
        )
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
        # BEING LOGGED OUT IS THE COMMON CAUSE, AND IT USED TO BE INVISIBLE.
        #
        # `pyyol profile` is documented as "self if omitted". Logged out, /v1/me returns
        # nothing and the old message was "pass a handle" — technically true, and it hides the
        # actual fix. A developer reads it as "this command needs an argument" and never
        # learns that logging in is what they wanted.
        if not (creds and creds.access_token):
            print(
                f"{BAD} not logged in, so there is no 'self' to show — run `pyyol login`,"
                f" or name someone: `pyyol profile <@handle>`",
                file=sys.stderr,
            )
            return 2
        _, me = _api_get(f"{base}/v1/me", creds.access_token)
        handle = me.get("user_id") or ""
        if not handle:
            print(
                f"{BAD} logged in, but the platform did not return your handle."
                f" Try `pyyol whoami`, or name someone: `pyyol profile <@handle>`",
                file=sys.stderr,
            )
            return 2
    st, p = _api_get(f"{base}/v1/developers/{urllib.parse.quote(handle)}")
    if st != 200:
        print(f"{BAD} no such developer {handle!r}{_status(st)}.", file=sys.stderr)
        return 1
    dev = p.get("developer", {})
    pidx = p.get("p_index") or {}
    stats = p.get("stats") or {}

    # The NAME, then the handle. This printed only "@handle", so `pyyol profile` could not
    # tell you who a developer was — the one thing a profile command is for. The name is
    # omitted when unset rather than substituting the public id, which is not a name.
    name = (dev.get("display_name") or "").strip()
    handle_line = f"@{dev.get('username') or dev.get('developer', '?')}"
    print(f"{name}  {handle_line}" if name else handle_line)

    # The bio. It has been storable since the profile editor shipped and was readable
    # nowhere: the column lived on `agents` and nothing selected it back, so a developer
    # wrote a description of how their agent plays and it appeared on no surface at all.
    bio = (dev.get("bio") or "").strip()
    if bio:
        print(f"  {bio}")

    if pidx:
        print(
            f"  P-Index   {pidx.get('p_index', '?')}  (rank #{pidx.get('global_rank', '?')}, top {pidx.get('percentile', '?')}%)"
        )
    else:
        # Said plainly. An absent P-Index block simply printed nothing, so an unranked
        # developer's output looked like a truncated response rather than a fact.
        print("  P-Index   unranked — no ranked matches yet")
    print(
        f"  Record    {stats.get('wins', 0)}W-{stats.get('losses', 0)}L-{stats.get('draws', 0)}D over {stats.get('total_matches', 0)} matches"
    )
    if stats.get("favorite_arena"):
        print(f"  Favorite  {stats.get('favorite_arena')}")
    print(f"  Agents    {len(p.get('agents') or [])}   Followers {p.get('followers', 0)}")
    return 0


_REPLAY_PATH = {
    "goofspiel": "/v1/match/{id}/replay",
    "mafia": "/v1/mafia/{id}/replay",
}


def cmd_replay(args: argparse.Namespace) -> int:
    from . import credentials

    creds = credentials.load()
    base = _http_base(args, creds)
    if not base:
        print(f"{BAD} no API url — pass --api or run `pyyol login`.", file=sys.stderr)
        return 2
    game = args.game or (creds and _cfg_arena()) or "goofspiel"
    path = _REPLAY_PATH.get(game, _REPLAY_PATH["goofspiel"]).format(
        id=urllib.parse.quote(args.match)
    )
    st, resp = _api_get(f"{base}{path}")
    if st != 200:
        print(
            f"{BAD} could not fetch replay{_status(st)}: {resp.get('error') or resp}",
            file=sys.stderr,
        )
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
    'seat N'/'tie'; strings (mafia team / agent id) pass through."""
    if w is None or w == "":
        return ""
    if isinstance(w, bool):
        return ""
    if isinstance(w, int):
        return "tie" if w < 0 else f"seat {w}"
    return str(w)


def _replay_outcome(resp: dict[str, Any]):
    """Extract (winner_label, scores) from a replay doc. Goofspiel encodes the result
    in a terminal `match_finished` event (winner seat + scores); mafia may
    carry a top-level winner. Returns ("", None) when it can't be determined."""
    for k in ("winner", "winner_team", "winner_agent"):
        if resp.get(k) not in (None, ""):
            return _winner_label(resp[k]), None
    for ev in reversed(resp.get("events") or []):
        if not isinstance(ev, dict):
            continue
        pl = ev.get("payload")
        payload = pl if isinstance(pl, dict) else {}
        if (
            ev.get("type") in ("match_finished", "game_over", "victory", "finished")
            or "winner" in payload
        ):
            return _winner_label(payload.get("winner")), payload.get("scores")
    return "", None


def _cfg_arena() -> str:
    from . import config as cfgmod

    cfg = cfgmod.load()
    return cfg.arena if cfg else ""


def cmd_doctor(args: argparse.Namespace) -> int:
    from . import config as cfgmod
    from . import credentials

    checks: list[tuple[str, bool, str]] = []
    creds = credentials.load()
    checks.append(
        (
            "logged in",
            bool(creds and creds.access_token),
            creds.url if creds else "run `pyyol login`",
        )
    )

    cfg = cfgmod.load()
    if cfg is None:
        checks.append(("pyyol.toml", False, "run `pyyol init`"))
    else:
        problems = cfgmod.validate(cfg)
        checks.append(
            (
                "pyyol.toml",
                not problems,
                "; ".join(problems) or f"{cfg.name} · {cfg.arena} · {cfg.mode}",
            )
        )
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

    # --- Verified-tier readiness -----------------------------------------------
    #
    # Separate from the checks above because these are not errors: an agent can run perfectly
    # while earning nothing. That is exactly the failure this section exists to prevent — the
    # platform ranks VERIFIED play, and an agent whose calls are never proven is invisible to the
    # model board no matter how well it plays. A developer should learn that here, in one second,
    # rather than from an empty row on a leaderboard weeks later.
    _print_verified_readiness(base, creds, cfg)

    ready = all_ok
    print(
        "\n"
        + (
            "✓ ready — `pyyol dev` to practice, `pyyol play <arena>` to compete."
            if ready
            else "fix the ✗ items above."
        )
    )
    return 0 if ready else 1


def _print_verified_readiness(base: str, creds, cfg) -> None:
    """Report whether this agent will actually earn Verified, and if not, exactly why.

    Three things decide it, and each fails silently on its own:

      1. ROUTING — model calls have to go through the Pyyol gateway. Without it the platform sees
         no calls at all and every decision is unproven.
      2. A SYSTEM PROMPT — the scaffold fingerprint is what lets the model board compare two models
         across ONE harness. Instructions that live in the user turn cannot be told apart from the
         game state, so such an agent is excluded from paired comparison entirely.
      3. COVERAGE — the share of decisions actually proven. A badge earned on 5% of play is the
         thing coverage gating exists to refuse.

    Printed rather than returned as a check because none of these is a failure of the agent: it
    will run, it just will not be ranked, and conflating the two would train people to ignore a
    red mark that sometimes means nothing.
    """
    from . import _instrument, scaffold

    print("\nverified tier")

    routed = bool(
        _instrument.gateway_base_url("anthropic") or _instrument.gateway_base_url("openai")
    )
    print(
        f"  {OK if routed else WARN} {'gateway routing':<20} "
        + (
            "on — model calls are server-observed"
            if routed
            else "off — call pyyol.route(client) after pyyol.instrument(); without it no decision "
            "can be proven and this agent cannot appear on the model board"
        )
    )

    # The scaffold is read from the agent's own source rather than guessed: a developer asking
    # "why am I not on the board" needs the answer for THEIR code, not for a generic example.
    hint = _scaffold_hint(cfg)
    if hint is None:
        print(
            f"  {WARN} {'system prompt':<20} could not inspect the agent source; run `pyyol dev` "
            "and check `scaffold` on a decision in the trace"
        )
    elif hint:
        print(
            f"  {OK} {'system prompt':<20} found — the harness can be fingerprinted, so this "
            "agent is eligible for paired model comparison"
        )
    else:
        print(
            f"  {WARN} {'system prompt':<20} none found. {scaffold.explain(scaffold.ISSUE_NO_SYSTEM_PROMPT)}"
        )

    if base and creds and creds.access_token:
        st, body = _api_get(f"{base}/v1/gw/coverage", token=creds.access_token)
        if st == 200 and isinstance(body, dict) and body.get("decisions"):
            cov = float(body.get("coverage") or 0)
            bound, total = body.get("bound_decisions", 0), body.get("decisions", 0)
            mark = OK if cov >= 0.90 else WARN
            print(
                f"  {mark} {'coverage':<20} {bound}/{total} decisions proven ({cov * 100:.1f}%)"
                + ("" if cov >= 0.90 else " — below the 90% the verified tier requires")
            )
        elif st == 200:
            print(f"  {WARN} {'coverage':<20} no decisions recorded yet — play a match first")


def _scaffold_hint(cfg) -> bool | None:
    """True if the agent's source appears to send a system prompt, False if not, None if unknown.

    A source scan, deliberately shallow: it looks for the shapes the two provider SDKs use for a
    system prompt. Being approximate is acceptable because the consequence of a wrong answer here
    is a hint, not a decision — the authoritative answer is the `scaffold` field on a real
    decision, which is what the message points at when this cannot tell.
    """
    if cfg is None or not getattr(cfg, "entry", ""):
        return None
    path = str(cfg.entry).split(":", 1)[0]
    try:
        with open(path, encoding="utf-8") as fh:
            src = fh.read()
    except OSError:
        return None
    # Anthropic passes `system=`; OpenAI uses a message with role "system" (or "developer").
    for needle in ("system=", '"system"', "'system'", '"developer"', "'developer'"):
        if needle in src:
            return True
    return False


def cmd_update(args: argparse.Namespace) -> int:

    print(f"pyyol {__version__}")
    latest = ""
    try:
        with _urlopen("https://pypi.org/pypi/pyyol/json", timeout=5) as resp:
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


def cmd_usage(args: argparse.Namespace) -> int:
    """Show what the platform actually recorded for one match.

    The question this answers is "did my telemetry land?", which had no answer before:
    `pyyol replay` carries the game, not the metering, so an agent author could not
    confirm their tokens were captured or their decisions counted as LLM-backed — for
    the features the Verified badge and ranked validity depend on.
    """
    creds = _ensure_login(args)
    if creds is None or not creds.access_token:
        return 2
    api = (args.api or creds.url).rstrip("/")
    agent = args.agent or creds.agent_id
    if not (api and agent):
        if _already_reported(creds):
            return 2
        print(
            f"{BAD} no agent on this device yet — run `pyyol init <dir>` to create one.",
            file=sys.stderr,
        )
        return 2

    st, body = _api_get(
        f"{api}/v1/matches/{urllib.parse.quote(args.match, safe='')}/usage"
        f"?agent={urllib.parse.quote(agent, safe='')}",
        creds.access_token,
    )
    if st != 200:
        print(f"{BAD} could not read usage{_status(st)}: {body}", file=sys.stderr)
        return 1
    if getattr(args, "json", False):
        print(json.dumps(body, indent=2))
        return 0

    d = body.get("decisions", 0)
    fb = body.get("fallbacks", 0)
    print(f"match {body.get('match_id')}  ·  agent {body.get('agent_id')}")
    print(f"  decisions      {d}  ({body.get('legal', 0)} legal, {fb} played by the engine)")
    print(f"  avg latency    {body.get('avg_latency_ms', 0)} ms")
    print(f"  tokens         {body.get('tokens', 0)}  (self-reported)")
    print(
        f"  cost           ${body.get('self_reported_cost_usd', 0):.6f}  (self-reported estimate)"
    )
    print(
        f"  VERIFIED cost  ${body.get('verified_cost_usd', 0):.6f}  over {body.get('verified_calls', 0)} gateway call(s)"
    )
    print(f"  LLM-backed     {body.get('bound_decisions', 0)}/{d} decisions carried a turn proof")

    # The diagnosis, not just the numbers — an unrouted agent looks instrumented and
    # is not, which is the failure that is otherwise invisible until a match is voided.
    if d and not body.get("verified_calls"):
        if body.get("tokens"):
            print(
                f"\n{BAD} your agent reported tokens but NOTHING reached the gateway — "
                "it is not verified.\n    Wrap your client: client = pyyol.route(client), "
                "and call pyyol.instrument() once at startup."
            )
        else:
            print(f"\n{BAD} no telemetry recorded at all. Call pyyol.instrument() once at startup.")
    elif d and body.get("bound_decisions", 0) < d:
        print(
            f"\n! {body.get('bound_decisions', 0)} of {d} decisions carried a turn proof. "
            "Calls made outside a turn (batching, warm-up) do not count toward ranked integrity."
        )
    if fb:
        print(
            f"\n! {fb} move(s) were played by the engine because your agent was late, "
            "illegal or unreachable — those are recorded as your errors."
        )
    return 0


def cmd_dev(args: argparse.Namespace) -> int:
    """Local development loop — sandbox-locked (never real stakes)."""
    return _orchestrate(args, dev_locked=True)


def cmd_play(args: argparse.Namespace) -> int:
    """Start competing in a chosen arena. Sandbox by default; --ranked = real stakes."""
    return _orchestrate(args, dev_locked=False)


# --- entry point ---------------------------------------------------------------


def _add_api(sp):
    """Attach --api to a subcommand.

    default=SUPPRESS, not "". `pyyol --api URL leaderboard` parses the top level first and the
    subcommand second INTO THE SAME NAMESPACE, so an ordinary default would overwrite the
    global value with an empty string the moment the subcommand was reached — the flag would
    parse, and then be silently discarded. SUPPRESS makes argparse leave the attribute alone
    when the flag is absent, so whichever position the developer used is the one that survives.
    """
    sp.add_argument(
        "--api",
        default=argparse.SUPPRESS,
        help="platform API base (defaults to the logged-in one)",
    )


def build_parser() -> argparse.ArgumentParser:
    # The wordmark on --help too, not only inside the shell.
    #
    # `pyyol --help` is what a developer sees in CI, in a Dockerfile, and any time the tool is
    # piped — and it was bare argparse with no sign of what this is. Colour is decided by the
    # STREAM, so a pipe or a redirect gets clean ASCII and a terminal gets the brand; a
    # wordmark full of escape codes in a CI log is worse than none.
    #
    # RawDescriptionHelpFormatter because argparse otherwise re-wraps the description and
    # turns the art into rubble.
    banner = ""
    try:
        from .shell import wordmark_for

        art = wordmark_for(sys.stdout)
        banner = art + "\n\n" if art else ""
    except Exception:  # noqa: BLE001 — a decoration must never stop the tool from running
        banner = ""
    p = argparse.ArgumentParser(
        prog="pyyol",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        description=banner + "  Pyyol — build, run, and rank autonomous AI agents.\n"
        "  Quickstart: pyyol login → pyyol init → pyyol dev",
        epilog="Run `pyyol` with no arguments to open the interactive shell:\n"
        "  a command menu on `/`, tab completion, and every command below available inside it.",
    )
    p.add_argument("--version", action="version", version=f"pyyol {__version__}")
    # GLOBAL --api, accepted before the command as well as after it.
    #
    # It used to be per-command only, so `pyyol --api https://... leaderboard` — the position
    # every other tool accepts, and the one people reach for first — died with an argparse
    # error listing all 25 commands and claiming the URL was an invalid choice of command. The
    # message named the wrong problem entirely.
    p.add_argument(
        "--api",
        default="",
        metavar="URL",
        help="platform API base (defaults to the logged-in one). Accepted here or after the command.",
    )
    sub = p.add_subparsers(dest="command", required=True, metavar="<command>")

    # --- auth ---
    pl = sub.add_parser("login", help="log in via the browser (GitHub/Google/wallet/email)")
    pl.add_argument(
        "--with",
        dest="provider",
        default="",
        choices=["github", "google", "wallet"],
        help="pre-select a provider on the login page",
    )
    pl.add_argument(
        "--dashboard",
        default="",
        help="dashboard base URL that serves /cli-login (default: https://pyyol.com; or $PYYOL_DASHBOARD)",
    )
    pl.add_argument(
        "--api",
        default=argparse.SUPPRESS,
        help="platform API base URL to record (default: https://api.pyyol.com; or $PYYOL_API)",
    )
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
    pi.add_argument("--arena", choices=["goofspiel", "mafia"], default="goofspiel")
    pi.add_argument("--name", default="")
    pi.set_defaults(func=cmd_init)

    # --- develop (sandbox-locked) ---
    pdev = sub.add_parser(
        "dev", help="run your agent locally in SANDBOX (no stakes) — the dev loop"
    )
    pdev.add_argument("--matches", type=int, default=3, help="practice matches to auto-start")
    pdev.add_argument("--url", default="", help="connect URL (or PYYOL_URL; defaults to login)")
    pdev.add_argument("--agent", default="", help="agent id (or PYYOL_AGENT_ID; defaults to login)")
    pdev.add_argument("--token", default="", help="token (or PYYOL_TOKEN; defaults to login)")
    pdev.add_argument("--quiet", action="store_true")
    pdev.add_argument("--no-color", action="store_true")
    # Watching the match you just started should not require hunting for it. The link
    # is ALWAYS printed; this only controls the browser tab.
    #   auto (default) — open the first match of the run, print the rest
    #   always         — open every match
    #   never          — never open (CI, tmux, remote boxes)
    pdev.add_argument(
        "--open",
        dest="open_browser",
        choices=["auto", "always", "never"],
        default="auto",
        help="open the live match in your browser: auto (first only) | always | never",
    )
    # Where to watch, asked once per run when both ends are a TTY.
    #   ask (default) — the pop-up: browser or terminal, defaulting to terminal
    #   browser       — always open, never ask
    #   terminal      — never open, never ask (CI, tmux, remote boxes)
    # A non-"ask" value means nothing reads stdin, which is what makes it script-safe.
    pdev.add_argument(
        "--watch",
        choices=["ask", "browser", "terminal"],
        default="ask",
        help="where to watch a match: ask (default) | browser | terminal",
    )
    _add_api(pdev)
    pdev.set_defaults(func=cmd_dev)

    # `pyyol usage` — the answer to "did my telemetry land?", which previously had no
    # read path anywhere in the CLI or the API.
    pusage = sub.add_parser(
        "usage", help="what the platform recorded for one match (tokens, cost, verification)"
    )
    pusage.add_argument("match", help="match id, e.g. m_tqp7ze5jzmn7xoxu")
    pusage.add_argument("--agent", default="", help="agent id (defaults to the logged-in agent)")
    pusage.add_argument("--json", action="store_true", help="raw JSON")
    _add_api(pusage)
    pusage.set_defaults(func=cmd_usage)

    # --- compete (explicit; --ranked = real stakes) ---
    pp = sub.add_parser(
        "play", help="compete in an arena. SANDBOX by default; --ranked = real stakes"
    )
    pp.add_argument("arena", choices=["goofspiel", "mafia"])
    pp.add_argument(
        "--ranked", action="store_true", help="REAL stakes (needs `pyyol publish`; confirmed)"
    )
    pp.add_argument("--tier", default="low", help="ranked stake tier: low|mid|high")
    pp.add_argument("--matches", type=int, default=1, help="sandbox matches to start")
    pp.add_argument("--yes", action="store_true", help="skip the ranked confirmation (CI)")
    pp.add_argument("--url", default="")
    pp.add_argument("--agent", default="")
    pp.add_argument("--token", default="")
    pp.add_argument("--quiet", action="store_true")
    pp.add_argument("--no-color", action="store_true")
    # Watching the match you just started should not require hunting for it. The link
    # is ALWAYS printed; this only controls the browser tab.
    #   auto (default) — open the first match of the run, print the rest
    #   always         — open every match
    #   never          — never open (CI, tmux, remote boxes)
    pp.add_argument(
        "--open",
        dest="open_browser",
        choices=["auto", "always", "never"],
        default="auto",
        help="open the live match in your browser: auto (first only) | always | never",
    )
    _add_api(pp)
    # Where to watch, asked once per run when both ends are a TTY.
    #   ask (default) — the pop-up: browser or terminal, defaulting to terminal
    #   browser       — always open, never ask
    #   terminal      — never open, never ask (CI, tmux, remote boxes)
    # A non-"ask" value means nothing reads stdin, which is what makes it script-safe.
    pp.add_argument(
        "--watch",
        choices=["ask", "browser", "terminal"],
        default="ask",
        help="where to watch a match: ask (default) | browser | terminal",
    )
    pp.set_defaults(func=cmd_play)

    ppub = sub.add_parser(
        "publish", help="certify your agent for RANKED play (verify a hosted endpoint)"
    )
    ppub.add_argument("--api", default=argparse.SUPPRESS, help="platform API base (or from login)")
    ppub.add_argument("--agent", default="", help="agent public id (or from login)")
    ppub.add_argument("--token", default="", help="dashboard/access token (or from login)")
    ppub.add_argument("--manifest", required=True, help="path to manifest.json (hosted endpoint)")
    ppub.add_argument("--secret", default="", help="endpoint secret to store before verify")
    ppub.set_defaults(func=cmd_publish)

    # --- discover / inspect ---
    prep = sub.add_parser("replay", help="fetch a match replay")
    prep.add_argument("match")
    prep.add_argument("--game", choices=["goofspiel", "mafia"], default="")
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

    pg = sub.add_parser("games", help="show live + waiting agents per game")
    _add_api(pg)
    pg.set_defaults(func=cmd_games)

    pdoc = sub.add_parser("doctor", help="diagnose your setup (login, config, agent, platform)")
    _add_api(pdoc)
    pdoc.set_defaults(func=cmd_doctor)

    sub.add_parser("update", help="check for a newer pyyol").set_defaults(func=cmd_update)

    # --- advanced / compatibility aliases (lower-level; dev/play front-end these) ---
    pv = sub.add_parser(
        "validate", help="[advanced] probe a hosted endpoint like the platform does"
    )
    pv.add_argument("--url", required=True)
    pv.add_argument("--secret", default="")
    pv.add_argument("--game", choices=["goofspiel", "mafia"], default="goofspiel")
    pv.set_defaults(func=cmd_validate)

    ps = sub.add_parser(
        "simulate",
        help="run a full local Goofspiel match in-process (no network); "
        "or with --url, drive a hosted endpoint",
    )
    ps.add_argument("--url", default="", help="hosted endpoint to drive; omit for in-process")
    ps.add_argument("--secret", default="")
    ps.add_argument("--game", choices=["goofspiel"], default="goofspiel")
    ps.add_argument("--hand", type=int, default=13)
    ps.add_argument("--seed", type=int, default=1)
    ps.set_defaults(func=cmd_simulate)

    prun = sub.add_parser(
        "run", help="[advanced] connect your agent over WSS (dev/play front-end this)"
    )
    prun.add_argument("--file", default="agent.py")
    prun.add_argument("--var", default="agent")
    prun.add_argument("--url", default="")
    prun.add_argument("--agent", default="")
    prun.add_argument("--token", default="")
    prun.add_argument("--json", action="store_true")
    prun.add_argument("--quiet", action="store_true")
    prun.add_argument("--no-color", action="store_true")
    prun.set_defaults(func=cmd_run)

    psv = sub.add_parser(
        "serve",
        help="deploy-once worker: enable auto-play + hold the connection so your agent plays anytime",
    )
    psv.add_argument("--file", default="agent.py")
    psv.add_argument("--var", default="agent")
    psv.add_argument("--url", default="")
    psv.add_argument("--agent", default="")
    psv.add_argument("--token", default="")
    _add_api(psv)
    psv.add_argument(
        "--ranked", action="store_true", help="auto-play RANKED (real stakes); default sandbox"
    )
    psv.add_argument(
        "--mode",
        default="",
        choices=["", "sandbox", "ranked"],
        help="explicit mode (overrides pyyol.toml)",
    )
    psv.add_argument("--bid", type=int, default=0, help="ranked stake per match")
    psv.add_argument(
        "--games",
        default="",
        help="comma-separated games to rotate (sandbox); default = your arena",
    )
    psv.add_argument("--json", action="store_true")
    psv.add_argument("--quiet", action="store_true")
    psv.add_argument("--no-color", action="store_true")
    psv.set_defaults(func=cmd_serve)

    pap = sub.add_parser(
        "autoplay", help="toggle auto-play without holding a connection (for hosted endpoints)"
    )
    pap.add_argument("state", choices=["on", "off", "status"])
    _add_api(pap)
    pap.add_argument("--token", default="")
    pap.add_argument(
        "--ranked", action="store_true", help="auto-play RANKED (real stakes); default sandbox"
    )
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

    pq = sub.add_parser(
        "queue", help="enter ranked matchmaking at a stake tier (your connected agent plays)"
    )
    pq.add_argument("game")
    _add_api(pq)
    pq.add_argument("--list", action="store_true", help="show the game's stake tiers and exit")
    pq.add_argument("--tier", default="", help="stake tier key (see --list)")
    pq.add_argument("--bid", type=int, default=0, help="explicit coin stake for a tier-less game")
    # cmd_queue polls for a pairing after enqueueing, and read args.wait to bound it — but
    # nothing ever defined the flag, so the command CRASHED on every run with
    # `AttributeError: 'Namespace' object has no attribute 'wait'`, immediately after
    # printing "✓ queued". The enqueue had already succeeded, so the agent really was in
    # the queue and the developer was told the tool was broken.
    #
    # It survived because the tests build a Namespace by hand and supply wait=5.0
    # themselves — an attribute the real parser never produced. Nothing exercised the
    # actual argv path.
    pq.add_argument(
        "--wait",
        type=float,
        default=30.0,
        help="seconds to wait for a pairing before returning (the agent plays regardless)",
    )
    pq.add_argument("--token", default="")
    pq.set_defaults(func=cmd_queue)

    # Rooms. Two subcommands under one noun rather than `room-create`/`room-join`, so the
    # pair reads as one feature in `pyyol --help` instead of two unrelated verbs.
    prm = sub.add_parser("room", help="create or join a private staked table shared by its id")
    prm.add_argument("action", choices=["create", "join"])
    prm.add_argument("id", nargs="?", default="", help="the room id, when joining")
    _add_api(prm)
    prm.add_argument(
        "--tier", default="", help="stake tier key (see `pyyol queue goofspiel --list`)"
    )
    prm.add_argument("--bid", type=int, default=0, help="explicit coin stake")
    prm.add_argument("--token", default="")
    prm.set_defaults(func=cmd_room)

    pwal = sub.add_parser("wallet", help="show your coin balance + per-agent playing wallets")
    _add_api(pwal)
    pwal.add_argument("--json", action="store_true")
    pwal.set_defaults(func=cmd_wallet)

    return p


def _make_output_unicode_safe() -> None:
    """Stop a legacy console from turning output into a crash.

    Windows consoles still default to cp1252 in plenty of setups, and this CLI prints ‚Üí, ‚àí,
    ‚óè, box-drawing and the wordmark. Writing any of those to a cp1252 stream raises
    UnicodeEncodeError from inside `print` ‚Äî so `pyyol --help` died with a traceback before
    printing a single line of help, on the platform least equipped to debug it. Reported from
    a real Windows session, reproduced here with PYTHONIOENCODING=cp1252.

    Two steps, in order:

      1. Try to switch the stream to UTF-8. Modern Windows Terminal, PowerShell 7 and VS Code
         all render it correctly, so the right answer is usually "just use UTF-8".
      2. Failing that, keep the console's encoding but replace what it cannot draw. A "?"
         where an arrow should be is a cosmetic blemish; a traceback is a broken tool.

    Deliberately best-effort and silent: a stream that does not support reconfigure (a pipe
    wrapped by a test, an embedded runtime) is left exactly as it was.
    """
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if reconfigure is None:
            continue
        enc = (getattr(stream, "encoding", "") or "").lower().replace("-", "")
        if enc in ("utf8", "utf8mb4"):
            continue  # already fine; leave it alone
        try:
            reconfigure(encoding="utf-8")
            continue
        except (ValueError, OSError, LookupError):
            pass
        try:
            reconfigure(errors="replace")
        except (ValueError, OSError, LookupError):
            pass


def main(argv: list[str] | None = None) -> int:
    # FIRST, before anything can print: a cp1252 console must not turn our own output into a
    # traceback. See _make_output_unicode_safe.
    _make_output_unicode_safe()

    # BARE `pyyol` ON A TTY OPENS THE SHELL.
    #
    # The subcommand is required=True, so typing the tool's own name — the first thing anyone
    # does after installing it — printed a usage error and exited 2. Now it opens the home
    # screen instead, and every command remains available exactly as before on the command
    # line: the shell dispatches through this same parser (see pyyol/shell.py).
    #
    # TTY-GATED, and that is not a nicety. `pyyol | cat`, a CI step, a cron entry or a
    # Dockerfile RUN must print help and exit; a prompt waiting on stdin there hangs the
    # pipeline forever, in exactly the places nobody is watching. argv is checked rather than
    # sys.argv so a programmatic main([]) keeps its old behaviour.
    if argv is None and not sys.argv[1:]:
        if sys.stdin.isatty() and sys.stdout.isatty():
            from . import shell

            return shell.run_shell(build_parser, __version__, DEFAULT_API_BASE)
        build_parser().print_help()
        return 0

    args = build_parser().parse_args(argv)
    # Anonymous, once-per-version, fire-and-forget adoption ping (opt out with
    # PYYOL_NO_TELEMETRY / DO_NOT_TRACK). Never blocks or affects the command.
    from . import install_ping

    install_ping.maybe_ping(getattr(args, "api", "") or DEFAULT_API_BASE, __version__)
    # THE ERROR BOUNDARY. This call used to be bare, so any unexpected exception printed a raw
    # traceback — our file paths, our line numbers — to a developer who only wanted to know
    # whether their agent was ranked. Ctrl-C did the same. See pyyol/_crash.py.
    from ._crash import guard

    return guard(
        args.func, args, version=__version__, argv=argv if argv is not None else sys.argv[1:]
    )


if __name__ == "__main__":
    raise SystemExit(main())
