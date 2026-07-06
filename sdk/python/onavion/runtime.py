"""The Onavion Runtime Connector — the local-runtime transport.

Your agent runs on your own machine and dials OUT over a single persistent
WebSocket to the platform. The platform pushes match lifecycle down that socket
and reads your decisions back over it, so a laptop behind NAT works with **zero
networking config** — you never host an inbound endpoint. This is the Beta model.

The connector owns everything transport: registration, heartbeats, automatic
reconnection with exponential backoff, request/response correlation, and
dispatching frames to the handlers you registered on your ``Agent``. It contains
no game logic — your ``@agent.on_turn`` handler returns the move.

    from onavion import Agent
    agent = Agent(supported_games=["goofspiel"], name="OlympAI")

    @agent.on_turn("goofspiel")
    def decide(v):
        return {"round": v.round, "card": max(v.legal_actions)}

    agent.run(url="wss://onavion.example/v1/agent/connect", agent_id="ag_…", token="…")
"""

from __future__ import annotations

import json
import logging
import threading
import time
from typing import Any, Dict, List, Optional

from . import __version__

log = logging.getLogger("onavion")

# Frame types — byte-identical to the Go gateway (internal/agentgw/frame.go).
HELLO, REGISTERED, PONG = "hello", "registered", "pong"
INITIALIZE, TURN, EVENT, GAME_END, ERROR = "initialize", "turn", "event", "game_end", "error"
REGISTER, PING, RESPONSE, ACK = "register", "ping", "response", "ack"

PROTOCOL_VERSION = "1.0"


class ConnectorError(RuntimeError):
    """Terminal connector failure (e.g. auth rejected) — not retried."""


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
        name: str = "onavion-agent",
        games: Optional[List[str]] = None,
        version: str = "1.0.0",
        heartbeat_interval: float = 10.0,
        reconnect: bool = True,
        max_backoff: float = 30.0,
        _connect=None,  # injectable for tests (defaults to websockets.sync.client.connect)
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
        self._connect = _connect
        self._stop = threading.Event()

    # --- public API -----------------------------------------------------------

    def run(self) -> None:
        """Connect and serve until interrupted, reconnecting with backoff."""
        backoff = 1.0
        while not self._stop.is_set():
            try:
                self._session()
                backoff = 1.0  # a clean session resets backoff
            except ConnectorError:
                raise  # terminal (auth) — don't spin
            except KeyboardInterrupt:
                break
            except Exception as e:  # noqa: BLE001 — any transport error is retryable
                if not self.reconnect or self._stop.is_set():
                    raise
                log.warning("connection lost (%s); reconnecting in %.1fs", e, backoff)
                if self._stop.wait(backoff):
                    break
                backoff = min(backoff * 2, self.max_backoff)
            else:
                if not self.reconnect:
                    break

    def stop(self) -> None:
        self._stop.set()

    # --- one session ----------------------------------------------------------

    def _session(self) -> None:
        connect = self._connect
        if connect is None:
            from websockets.sync.client import connect as connect  # lazy import
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
            send({
                "t": REGISTER, "agent_id": self.agent_id, "token": self.token,
                "agent_name": self.name, "version": self.version,
                "games": self.games, "sdk_version": __version__,
            })
            reg = _recv(ws)
            if reg.get("t") == ERROR:
                raise ConnectorError(f"register rejected: {reg.get('error')} ({reg.get('reason')})")
            if reg.get("t") != REGISTERED:
                raise ConnectorError(f"expected registered, got {reg.get('t')!r}")
            log.info("connected to %s as %s (games=%s)", self.url, reg.get("agent_id") or self.agent_id, self.games)

            # 2. heartbeat thread — keeps liveness green even during a slow turn.
            hb_stop = threading.Event()
            hb = threading.Thread(
                target=self._heartbeat, args=(send, hb_stop), daemon=True, name="onavion-heartbeat")
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
            ack = self.agent.ack_initialize(frame.get("payload") or {})
            send({"t": RESPONSE, "id": frame.get("id", ""), "payload": ack})
        elif t == EVENT:
            self.agent.notify_event(self._event_dict(frame))
        elif t == GAME_END:
            self.agent.notify_game_end({
                "match_id": frame.get("match_id", ""),
                "game": frame.get("game", ""),
                "result": frame.get("payload"),
            })
        elif t == ERROR:
            log.warning("gateway error: %s (%s)", frame.get("error"), frame.get("reason"))
        else:
            log.debug("ignoring frame %r", t)

    def _handle_turn(self, frame: Dict[str, Any], send) -> None:
        view = frame.get("payload") or {}
        status, move = self.agent.decide_turn(view)
        rid = frame.get("id", "")
        if status == 200:
            send({"t": RESPONSE, "id": rid, "payload": move})
        else:
            # Signal an error so the platform applies its deterministic fallback.
            send({"t": RESPONSE, "id": rid, "error": move.get("error", "handler_error")})

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
