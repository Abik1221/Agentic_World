"""The Pyyol Runtime Connector — the local-runtime transport.

Your agent runs on your own machine and dials OUT over a single persistent
WebSocket to the platform. The platform pushes match lifecycle down that socket
and reads your decisions back over it, so a laptop behind NAT works with **zero
networking config** — you never host an inbound endpoint. This is the Beta model.

The connector owns everything transport: registration, heartbeats, automatic
reconnection with exponential backoff, request/response correlation, and
dispatching frames to the handlers you registered on your ``Agent``. It contains
no game logic — your ``@agent.on_turn`` handler returns the move.

    from pyyol import Agent
    agent = Agent(supported_games=["goofspiel"], name="OlympAI")

    @agent.on_turn("goofspiel")
    def decide(v):
        return {"round": v.round, "card": max(v.legal_actions)}

    agent.run(url="wss://pyyol.example/v1/agent/connect", agent_id="ag_…", token="…")
"""

from __future__ import annotations

import json
import logging
import threading
import time
from typing import Any, Dict, List, Optional
from urllib.parse import urlsplit

from . import __version__
from .console import Console
from .telemetry import Tracer, turn_usage

log = logging.getLogger("pyyol")

_LOCAL_HOSTS = {"localhost", "127.0.0.1", "::1", "[::1]"}

# Frame types — byte-identical to the Go gateway (internal/agentgw/frame.go).
HELLO, REGISTERED, PONG = "hello", "registered", "pong"
INITIALIZE, TURN, EVENT, GAME_END, ERROR = "initialize", "turn", "event", "game_end", "error"
REGISTER, PING, RESPONSE, ACK = "register", "ping", "response", "ack"

PROTOCOL_VERSION = "1.0"


def _version_tuple(v: str) -> tuple:
    """Parse a dotted version into a comparable int tuple; unparseable ⇒ (0,)
    so a garbage `latest_sdk` never triggers a spurious upgrade nudge."""
    try:
        core = v.strip().lstrip("v").split("-", 1)[0].split("+", 1)[0]
        return tuple(int(p) for p in core.split("."))
    except (ValueError, AttributeError):
        return (0,)


class ConnectorError(RuntimeError):
    """Terminal connector failure (e.g. auth rejected) — not retried."""


class _RefreshRetry(Exception):
    """Internal: the access token was just refreshed; reconnect immediately with
    the new token (not a transport error, so no backoff / scary log)."""


def _default_refresh_http(api_url: str, refresh_token: str):
    """POST {api_url}/v1/auth/refresh {refresh_token} → (access, refresh) or None.

    Uses only the stdlib so the connector stays dependency-light. A non-200 (the
    refresh token itself is expired/revoked) returns None → the caller treats the
    session as terminally unauthenticated and the developer must `pyyol login`."""
    import urllib.request

    url = api_url.rstrip("/") + "/v1/auth/refresh"
    body = json.dumps({"refresh_token": refresh_token}).encode("utf-8")
    req = urllib.request.Request(
        url, data=body, method="POST", headers={"content-type": "application/json"}
    )
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:  # noqa: S310 — our own API
            if resp.status != 200:
                return None
            data = json.loads(resp.read().decode("utf-8"))
    except Exception:  # noqa: BLE001 — 401/network/parse all mean "cannot refresh"
        return None
    access = data.get("dashboard_token") or ""
    if not access:
        return None
    return access, data.get("refresh_token") or ""


class RuntimeConnector:
    """Drives one agent over an outbound WebSocket to the platform.

    Reconnects automatically on transport errors; a rejected register (bad token)
    is terminal and raises. Heartbeats run on their own thread so a slow turn
    handler never trips the platform's liveness check.
    """

    def __init__(
        self,
        agent,
        url: str,
        agent_id: str = "",
        token: str = "",
        name: str = "pyyol-agent",
        games: Optional[List[str]] = None,
        version: str = "1.0.0",
        heartbeat_interval: float = 10.0,
        reconnect: bool = True,
        max_backoff: float = 30.0,
        console: Optional[Console] = None,
        refresh_token: str = "",
        api_url: str = "",
        on_tokens=None,  # callback(access, refresh) to persist a rotated pair
        _connect=None,  # injectable for tests (defaults to websockets.sync.client.connect)
        _refresh_http=None,  # injectable for tests (defaults to _default_refresh_http)
    ):
        self.agent = agent
        self.url = url
        self.agent_id = agent_id
        self.token = token
        self.name = name
        self.games = list(games or [])
        self.version = version
        self.heartbeat_interval = heartbeat_interval
        self.reconnect = reconnect
        self.max_backoff = max_backoff
        self.console = console or Console()  # silent base unless the CLI installs one
        # Token refresh (opt-in): the access token is short-lived; when register is
        # rejected we spend the rotating refresh token for a fresh one and reconnect,
        # so a long-running agent stays authenticated. No-op unless both are set.
        self.refresh_token = refresh_token
        self.api_url = api_url
        self.on_tokens = on_tokens
        self._refresh_http = _refresh_http or _default_refresh_http
        self._refresh_attempts = 0  # per-connection guard against a refresh loop
        self._connect = _connect
        self._stop = threading.Event()
        self._turn_no = 0
        self._nudged = False  # print the "upgrade available" notice at most once
        # Opt-in Pyyol Lens telemetry (no-op unless PYYOL_LENS_ENDPOINT+KEY set).
        # Correlated to the match trace so the agent's model/tool calls render
        # alongside the platform's authoritative gateway spans.
        self._tracer = Tracer.from_env(agent_id=agent_id, service=name)

    # --- lifecycle emit (file log + live console, never secrets) --------------

    def _emit(self, kind: str, msg: str, level: int = logging.INFO, **fields: Any) -> None:
        # File/debug log (what `pyyol logs` tails) + the live terminal console.
        log.log(level, "%s %s", kind, msg)
        self.console.emit(kind, msg, **fields)

    def _maybe_nudge(self, latest: Any) -> None:
        # The gateway echoes the newest published version on the registered frame.
        # Print a one-line upgrade hint at most once (not on every reconnect).
        if self._nudged or not isinstance(latest, str) or not latest:
            return
        if _version_tuple(latest) > _version_tuple(__version__):
            self._nudged = True
            self._emit(
                "upgrade",
                f"a new pyyol {latest} is available (you have {__version__}) — "
                "upgrade with `pip install -U pyyol`",
                level=logging.WARNING,
            )

    def _security_check(self) -> None:
        # The register token authenticates the socket; over plaintext ws:// to a
        # non-local host it would be exposed. Warn loudly (but never print it).
        host = urlsplit(self.url).hostname or ""
        if self.url.startswith("ws://") and host not in _LOCAL_HOSTS:
            self._emit(
                "warn",
                f"insecure transport: {self.url} sends your token in cleartext — use wss://",
                level=logging.WARNING,
            )

    # --- public API -----------------------------------------------------------

    def run(self) -> None:
        """Connect and serve until interrupted, reconnecting with backoff."""
        self.console.banner(self.name, self.url)
        self._security_check()
        backoff = 1.0
        while not self._stop.is_set():
            try:
                self._session()
                backoff = 1.0  # a clean session resets backoff
            except _RefreshRetry:
                # Token was just refreshed — reconnect right away with the new one,
                # no backoff and no "connection lost" noise.
                self._emit("reauth", "access token refreshed — reconnecting")
                backoff = 1.0
                continue
            except ConnectorError:
                raise  # terminal (auth) — don't spin
            except KeyboardInterrupt:
                break
            except Exception as e:  # noqa: BLE001 — any transport error is retryable
                if not self.reconnect or self._stop.is_set():
                    self._emit("disconnected", "connection closed", level=logging.WARNING)
                    raise
                self._emit(
                    "reconnecting",
                    f"connection lost — retrying in {backoff:.0f}s",
                    level=logging.WARNING,
                    reason=str(e),
                )
                if self._stop.wait(backoff):
                    break
                backoff = min(backoff * 2, self.max_backoff)
            else:
                if not self.reconnect:
                    break

    def stop(self) -> None:
        self._stop.set()
        self._tracer.close()

    # --- one session ----------------------------------------------------------

    def _session(self) -> None:
        # Feedback before the (bounded) connect, so a slow/dead link doesn't look like
        # a frozen terminal while the 10s open_timeout runs.
        host = urlsplit(self.url).netloc or self.url
        self._emit("connecting", f"connecting to {host}…")
        connect = self._connect
        if connect is None:
            from websockets.sync.client import connect as _ws_connect  # lazy import

            # Protocol-level keepalive so a half-open (silently dropped) TCP link is
            # detected and closed — the blocked recv then raises and run() reconnects.
            # Tuned for flaky/low-bandwidth links; the app-level heartbeat is separate.
            ws = _ws_connect(self.url, open_timeout=10, ping_interval=15, ping_timeout=15)
        else:
            ws = connect(self.url, open_timeout=10)
        send_lock = threading.Lock()

        def send(frame: Dict[str, Any]) -> None:
            with send_lock:
                ws.send(json.dumps(frame))

        try:
            # 1. hello → register → registered handshake.
            hello = _recv(ws)
            if hello.get("t") != HELLO:
                raise ConnectorError(f"expected hello, got {hello.get('t')!r}")
            send(
                {
                    "t": REGISTER,
                    "agent_id": self.agent_id,
                    "token": self.token,
                    "agent_name": self.name,
                    "version": self.version,
                    "games": self.games,
                    "sdk_version": __version__,
                    "sdk_language": "python",
                }
            )
            reg = _recv(ws)
            if reg.get("t") == ERROR:
                # A rejected register with valid-looking creds is almost always an
                # expired access token. If we hold a refresh token, spend it for a
                # fresh access token and reconnect; only give up (terminal) when the
                # refresh itself fails (refresh token expired/revoked → must re-login).
                if self._try_refresh():
                    raise _RefreshRetry()
                raise ConnectorError(f"register rejected: {reg.get('error')} ({reg.get('reason')})")
            if reg.get("t") != REGISTERED:
                raise ConnectorError(f"expected registered, got {reg.get('t')!r}")
            self._refresh_attempts = 0  # a good register clears the refresh guard
            self._maybe_nudge(reg.get("latest_sdk"))
            self._emit(
                "connected",
                "ready — playing as this agent",
                agent=reg.get("agent_id") or self.agent_id,
                games=",".join(self.games),
            )
            self._emit("waiting", "waiting for a match…")

            # 2. heartbeat thread — keeps liveness green even during a slow turn.
            hb_stop = threading.Event()
            hb = threading.Thread(
                target=self._heartbeat, args=(send, hb_stop), daemon=True, name="pyyol-heartbeat"
            )
            hb.start()

            # 3. read/dispatch loop (runs on this thread until the socket closes).
            try:
                while not self._stop.is_set():
                    frame = _recv(ws)
                    self._dispatch(frame, send)
            finally:
                hb_stop.set()
        finally:
            try:
                ws.close()
            except Exception:  # noqa: BLE001
                pass

    def _try_refresh(self) -> bool:
        """Spend the refresh token for a fresh access token. Returns True on success
        (self.token updated + persisted). Bounded per connection so a server that
        keeps rejecting can never spin us in a refresh loop."""
        if not (self.refresh_token and self.api_url) or self._refresh_attempts >= 2:
            return False
        self._refresh_attempts += 1
        try:
            pair = self._refresh_http(self.api_url, self.refresh_token)
        except Exception:  # noqa: BLE001 — any failure ⇒ can't refresh ⇒ terminal
            return False
        if not pair:
            return False
        access, refresh = pair
        if not access:
            return False
        self.token = access
        if refresh:
            self.refresh_token = refresh
        if self.on_tokens:
            try:
                self.on_tokens(access, self.refresh_token)
            except Exception:  # noqa: BLE001 — persistence is best-effort
                pass
        return True

    def _heartbeat(self, send, stop: threading.Event) -> None:
        while not stop.wait(self.heartbeat_interval):
            try:
                send({"t": PING})
            except Exception:  # noqa: BLE001 — socket gone; the read loop will exit too
                return

    # --- frame dispatch -------------------------------------------------------

    def _dispatch(self, frame: Dict[str, Any], send) -> None:
        t = frame.get("t")
        if t == PING:
            send({"t": PONG, "id": frame.get("id", "")})
        elif t == PONG:
            pass
        elif t == TURN:
            self._handle_turn(frame, send)
        elif t == INITIALIZE:
            payload = frame.get("payload") or {}
            self._turn_no = 0
            self._emit(
                "match",
                f"{payload.get('game', '')} match started",
                match=payload.get("match_id"),
                seat=payload.get("seat"),
                role=payload.get("role"),
            )
            ack = self.agent.ack_initialize(payload)
            send({"t": RESPONSE, "id": frame.get("id", ""), "payload": ack})
        elif t == EVENT:
            self._emit("event", frame.get("kind", "event"), seq=frame.get("seq"))
            self.agent.notify_event(self._event_dict(frame))
        elif t == GAME_END:
            result = frame.get("payload")
            self._emit("game_end", _summarize_result(result), match=frame.get("match_id"))
            self.agent.notify_game_end(
                {
                    "match_id": frame.get("match_id", ""),
                    "game": frame.get("game", ""),
                    "result": result,
                }
            )
            self._emit("waiting", "waiting for a match…")
        elif t == ERROR:
            self._emit(
                "error", f"{frame.get('error')} ({frame.get('reason')})", level=logging.WARNING
            )
        else:
            log.debug("ignoring frame %r", t)

    def _handle_turn(self, frame: Dict[str, Any], send) -> None:
        view = frame.get("payload") or {}
        self._turn_no += 1
        game = view.get("game", "")
        started = time.perf_counter()
        # Bracket the developer's handler in a Lens span AND a turn-local usage
        # accumulator. Inside on_turn the author can reach the span via
        # pyyol.current_span(); if pyyol.instrument() is active, every LLM call is
        # captured into the accumulator automatically. The accumulator is always on
        # (independent of Lens) so usage rides the move to the arena regardless.
        with self._tracer.turn_span(
            match_id=view.get("match_id", ""),
            game=game,
            round_no=int(view.get("round", 0) or 0),
            agent_id=self.agent_id,
        ), turn_usage(match_id=view.get("match_id", ""), turn=int(view.get("round", 0) or 0)) as usage:
            status, move = self.agent.decide_turn(view)
        ms = int((time.perf_counter() - started) * 1000)
        # Auto-attach captured model/token/cost to the move so the arena benchmark
        # records real usage with no developer boilerplate. A dev-supplied `usage`
        # (manual reporting) always wins — we never overwrite it.
        if status == 200 and isinstance(move, dict) and not usage.empty and "usage" not in move:
            move["usage"] = usage.to_move_usage()
        rid = frame.get("id", "")
        # Send the move FIRST, then log — a console flush / log-file write must never
        # sit on the move's latency path (the platform is waiting on this response).
        # Mirrors the JS connector's send-before-feed ordering.
        if status == 200:
            send({"t": RESPONSE, "id": rid, "payload": move})
            self._emit("decision", f"turn {self._turn_no}: {_summarize_move(game, move)}", ms=ms)
        else:
            # Signal an error so the platform applies its deterministic fallback.
            send({"t": RESPONSE, "id": rid, "error": move.get("error", "handler_error")})
            self._emit(
                "error", f"turn {self._turn_no}: handler error → fallback", level=logging.WARNING
            )

    @staticmethod
    def _event_dict(frame: Dict[str, Any]) -> Dict[str, Any]:
        # Gateway event frames use `kind` for the sub-type; EventNotification uses `type`.
        return {
            "match_id": frame.get("match_id", ""),
            "game": frame.get("game", ""),
            "seq": frame.get("seq", 0),
            "type": frame.get("kind", ""),
            "payload": frame.get("payload"),
        }


def _recv(ws) -> Dict[str, Any]:
    msg = ws.recv()
    if isinstance(msg, (bytes, bytearray)):
        msg = msg.decode("utf-8")
    obj = json.loads(msg)
    return obj if isinstance(obj, dict) else {}


def _summarize_move(game: str, move: Any) -> str:
    """A short, human-readable description of the move for the live feed."""
    if not isinstance(move, dict):
        return str(move)
    if game == "goofspiel" and "card" in move:
        return f"bid {move['card']}"
    action = move.get("action")
    if action:
        extra = ""
        if move.get("target"):
            extra = f" → {move['target']}"
        elif move.get("property"):
            extra = f" #{move['property']}"
        if move.get("text"):
            extra += f' "{str(move["text"])[:40]}"'
        return f"{action}{extra}"
    return json.dumps(move, separators=(",", ":"))[:60]


def _summarize_result(result: Any) -> str:
    """A short outcome summary for the game_end line."""
    if not isinstance(result, dict):
        return "game finished"
    nested = result.get("result")
    inner = nested if isinstance(nested, dict) else result
    bits = []
    winner = inner.get("winner")
    if winner is not None:
        bits.append(f"winner: {winner}")
    for k in ("coins_delta", "your_coins", "coins"):
        if inner.get(k):
            bits.append(f"{k}={inner[k]}")
            break
    return "game finished" + (" · " + " · ".join(str(b) for b in bits) if bits else "")
