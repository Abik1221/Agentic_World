"""The Pyyol agent server.

You register decision handlers; the SDK owns the wire protocol — routing,
signature verification, replay protection, payload parsing, and response
serialization. It contains no game strategy: your turn handler returns the move.

    from pyyol import Agent
    from pyyol.models import GoofspielView, GoofspielMove

    agent = Agent(secret="your-endpoint-secret", supported_games=["goofspiel"])

    @agent.on_turn("goofspiel")
    def decide(view: GoofspielView) -> GoofspielMove:
        # lowest legal card — replace with your own logic
        return GoofspielMove(card=min(view.legal_actions), round=view.round)

    agent.serve(port=9099)

The routes served (base path is taken from your manifest ``endpoint.url``; the
others are its siblings):

    GET  /health      liveness (never signature-checked)
    POST /handshake   capability check at verify time
    POST /initialize  match start -> on_initialize
    POST <endpoint>   the turn handler -> on_turn(game)
    POST /event       async notification -> on_event
    POST /game-end    async notification -> on_game_end
"""

from __future__ import annotations

import asyncio
import inspect
import json
import logging
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any, Callable, Dict, Optional, Tuple

from . import __version__
from .models import (
    EventNotification,
    GameEndNotification,
    InitializeRequest,
    SUPPORTED_GAMES,
    move_to_dict,
    parse_view,
)
from .signing import ReplayGuard, VerificationError, verify_request

log = logging.getLogger("pyyol")

# (status_code, json_body) — what every dispatch returns.
Response = Tuple[int, Dict[str, Any]]


class Agent:
    """A developer agent: register handlers, then ``serve()`` or mount ``handle()``.

    ``secret`` is the manifest endpoint secret — the shared key the platform signs
    with. When set (recommended), every POST is signature-verified with replay
    protection. Leave it empty only for local, unsigned experimentation.
    """

    def __init__(
        self,
        secret: str = "",
        supported_games=None,
        name: str = "pyyol-agent",
        skew_seconds: int = 300,
        verify: Optional[bool] = None,
    ):
        self.secret = secret
        self.supported_games = list(supported_games or SUPPORTED_GAMES)
        self.name = name
        self._skew = skew_seconds
        self._verify = bool(secret) if verify is None else verify
        self._replay = ReplayGuard()

        self._turn_handlers: Dict[str, Callable[[Any], Any]] = {}
        self._default_turn: Optional[Callable[[Any], Any]] = None
        self._on_initialize: Optional[Callable[[InitializeRequest], Any]] = None
        self._on_event: Optional[Callable[[EventNotification], Any]] = None
        self._on_game_end: Optional[Callable[[GameEndNotification], Any]] = None

    # --- handler registration (decorators) ---

    def on_turn(self, game: Optional[str] = None):
        """Register the per-turn decision handler. Pass a game name to scope it;
        omit it for a catch-all used when no game-specific handler is set."""

        def deco(fn: Callable[[Any], Any]):
            if game is None:
                self._default_turn = fn
            else:
                self._turn_handlers[game] = fn
            return fn

        return deco

    def on_initialize(self, fn: Callable[[InitializeRequest], Any]):
        self._on_initialize = fn
        return fn

    def on_event(self, fn: Callable[[EventNotification], Any]):
        self._on_event = fn
        return fn

    def on_game_end(self, fn: Callable[[GameEndNotification], Any]):
        self._on_game_end = fn
        return fn

    # --- transport-agnostic dispatch ---

    def handle(self, method: str, path: str, headers, body: bytes) -> Response:
        """Route one request and return ``(status, body)``. Framework-agnostic:
        call it from the built-in server or any web framework you already run.
        ``path`` must be the request path as received (no query)."""
        method = method.upper()
        suffix = path.rstrip("/").rsplit("/", 1)[-1]

        # Health is an unauthenticated liveness probe — never signature-checked.
        if method == "GET" and suffix == "health":
            return 200, {"status": "healthy", "agent": self.name, "version": __version__}

        if method != "POST":
            return 405, {"error": "method_not_allowed"}

        # Every POST is signed by the platform (when a secret is configured).
        if self._verify:
            try:
                verify_request(
                    self.secret,
                    headers,
                    method,
                    path,
                    body,
                    skew_seconds=self._skew,
                    replay_guard=self._replay,
                )
            except VerificationError as e:
                log.warning("signature rejected: %s", e.reason)
                return 401, {"error": "unauthorized", "reason": e.reason}

        data = _load_json(body)

        if suffix == "handshake":
            return 200, {
                "accepted": True,
                "sdkVersion": __version__,
                "supportedGames": self.supported_games,
            }

        if suffix == "initialize":
            req = InitializeRequest.from_dict(data)
            ack = self._on_initialize(req) if self._on_initialize else None
            if isinstance(ack, dict):
                return 200, ack
            return 200, {"ready": True, "display_name": self.name}

        if suffix == "event":
            if self._on_event:
                self._on_event(EventNotification.from_dict(data))
            return 200, {"ok": True}

        if suffix == "game-end":
            if self._on_game_end:
                self._on_game_end(GameEndNotification.from_dict(data))
            return 200, {"ok": True}

        # Anything else POSTed is the turn handler (the manifest endpoint.url).
        return self._handle_turn(data)

    def _handle_turn(self, data: Dict[str, Any]) -> Response:
        game = data.get("game", "")
        handler = self._turn_handlers.get(game) or self._default_turn
        if handler is None:
            log.error("no turn handler registered for game %r", game)
            return 501, {"error": "no_turn_handler", "game": game}
        view = parse_view(data)
        try:
            move = handler(view)
            # Support `async def step`: async LLM clients are first-class, so an
            # awaitable move is run to completion here (the runtime/serve loops are
            # synchronous, so there's no already-running loop to clash with).
            if inspect.isawaitable(move):
                move = asyncio.run(move)
        except Exception as e:  # a crashing handler must not take the server down
            # Log the full traceback AND return the message so the runtime can surface
            # it in the `pyyol dev` feed — a silently-swallowed crash is the #1
            # "why doesn't my agent work" trap. The engine still applies a fallback.
            log.exception("turn handler raised for game %r", game)
            return 500, {"error": "handler_error", "message": str(e)}
        return 200, move_to_dict(move)

    # --- shared handler invocation (used by both the HTTP path and the socket
    # RuntimeConnector, so both transports run identical decision logic) ---

    def decide_turn(self, view_data: Dict[str, Any]) -> Response:
        """Run the turn handler for a raw view dict; return ``(status, move)``."""
        return self._handle_turn(view_data)

    def ack_initialize(self, data: Dict[str, Any]) -> Dict[str, Any]:
        """Run the initialize handler and return the ack dict."""
        ack = (
            self._on_initialize(InitializeRequest.from_dict(data)) if self._on_initialize else None
        )
        return ack if isinstance(ack, dict) else {"ready": True, "display_name": self.name}

    def notify_event(self, data: Dict[str, Any]) -> None:
        if self._on_event:
            self._on_event(EventNotification.from_dict(data))

    def notify_game_end(self, data: Dict[str, Any]) -> None:
        if self._on_game_end:
            self._on_game_end(GameEndNotification.from_dict(data))

    # --- local runtime (dial OUT to the platform over WSS) ---

    def run(self, url: str, agent_id: str = "", token: str = "", **kwargs) -> None:
        """Connect to the platform and serve matches over an outbound WebSocket.

        This is the Beta local-runtime path: your machine dials the platform, so
        no inbound endpoint or networking config is needed. Blocks until
        interrupted; reconnects automatically. ``token``/``agent_id`` come from
        ``pyyol login`` (or pass them explicitly / via env)."""
        from .runtime import RuntimeConnector

        RuntimeConnector(
            self,
            url=url,
            agent_id=agent_id,
            token=token,
            name=self.name,
            games=self.supported_games,
            **kwargs,
        ).run()

    # --- built-in HTTP server ---

    def serve(self, host: str = "127.0.0.1", port: int = 9099) -> None:
        """Run a threaded HTTP server until interrupted. Zero dependencies."""
        agent = self

        class _Handler(BaseHTTPRequestHandler):
            def _dispatch(self, method: str):
                length = int(self.headers.get("Content-Length", 0) or 0)
                raw = self.rfile.read(length) if length else b""
                status, payload = agent.handle(method, self.path, self.headers, raw)
                body = json.dumps(payload).encode()
                self.send_response(status)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def do_GET(self):
                self._dispatch("GET")

            def do_POST(self):
                self._dispatch("POST")

            def log_message(self, *_args):
                pass  # quiet by default; use the "pyyol" logger instead

        httpd = ThreadingHTTPServer((host, port), _Handler)
        log.info(
            "pyyol agent %r listening on http://%s:%d (verify=%s)",
            self.name,
            host,
            port,
            self._verify,
        )
        try:
            httpd.serve_forever()
        except KeyboardInterrupt:
            pass
        finally:
            httpd.server_close()


class Adapter:
    """The v2 agent interface: subclass and implement ``step`` (the only required
    method); ``initialize`` and ``shutdown`` are optional. This is the minimal,
    framework-agnostic contract the spec describes — the SDK owns everything else
    (transport, auth, matchmaking, replay). Wrap any framework (LangGraph, CrewAI,
    OpenAI Agents SDK, custom) inside ``step``.

        from pyyol import Adapter
        from pyyol.models import GoofspielView, GoofspielMove

        class Atlas(Adapter):
            name = "atlas"
            supported_games = ["goofspiel"]

            def step(self, view: GoofspielView) -> GoofspielMove:
                return GoofspielMove(card=min(view.legal_actions), round=view.round)

        agent = Atlas()   # `pyyol dev` / `pyyol play` discover this via pyyol.toml

    Under the hood an ``Adapter`` is turned into an :class:`Agent`, so it runs over
    the exact same transport as the decorator API — nothing else changes.
    """

    name: str = "pyyol-agent"
    supported_games = list(SUPPORTED_GAMES)
    secret: str = ""

    def initialize(self, ctx: "InitializeRequest") -> Any:  # noqa: D401
        """Called once at match start. Return a dict ack or None. Optional."""
        return None

    def step(self, view: Any) -> Any:
        """Decide one move for ``view`` and return it. REQUIRED."""
        raise NotImplementedError("implement step(self, view) -> move")

    def shutdown(self, result: "GameEndNotification") -> None:
        """Called once when the match ends. Optional."""
        return None

    def to_agent(self) -> "Agent":
        """Build the underlying :class:`Agent` that drives the real transport."""
        a = Agent(
            secret=self.secret or os.environ.get("PYYOL_SECRET", ""),
            supported_games=self.supported_games,
            name=self.name,
        )
        a.on_turn()(self.step)
        a.on_initialize(self.initialize)
        a.on_game_end(self.shutdown)
        a.on_event(lambda _e: None)
        return a


def as_agent(obj: Any) -> Agent:
    """Normalize a developer's exported object into an :class:`Agent`.

    Accepts an :class:`Agent` (decorator style), an :class:`Adapter` instance, or an
    :class:`Adapter` subclass (instantiated with no args). Raises TypeError otherwise
    — the CLI turns that into a friendly message pointing at ``entry`` in pyyol.toml.
    """
    if isinstance(obj, Agent):
        return obj
    if isinstance(obj, Adapter):
        return obj.to_agent()
    if isinstance(obj, type) and issubclass(obj, Adapter):
        return obj().to_agent()
    raise TypeError(
        "expected a pyyol Agent or Adapter (got "
        f"{type(obj).__name__}); export one as the variable named in pyyol.toml `entry`"
    )


def _load_json(body: bytes) -> Dict[str, Any]:
    if not body:
        return {}
    try:
        obj = json.loads(body)
        return obj if isinstance(obj, dict) else {}
    except json.JSONDecodeError:
        return {}
