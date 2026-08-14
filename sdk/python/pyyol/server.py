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
from typing import Any, cast
from collections.abc import Callable, Coroutine

from . import __version__
from .models import (
    EventNotification,
    GameEndNotification,
    InitializeRequest,
    SUPPORTED_GAMES,
    move_to_dict,
    parse_view,
)
from .telemetry import turn_usage
from .signing import ReplayGuard, VerificationError, verify_request

log = logging.getLogger("pyyol")

# (status_code, json_body) — what every dispatch returns.
Response = tuple[int, dict[str, Any]]


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
        verify: bool | None = None,
    ):
        self.secret = secret
        self.supported_games = list(supported_games or SUPPORTED_GAMES)
        self.name = name
        self._skew = skew_seconds
        self._verify = bool(secret) if verify is None else verify
        self._replay = ReplayGuard()

        self._turn_handlers: dict[str, Callable[[Any], Any]] = {}
        self._default_turn: Callable[[Any], Any] | None = None
        self._on_initialize: Callable[[InitializeRequest], Any] | None = None
        self._on_event: Callable[[EventNotification], Any] | None = None
        self._on_game_end: Callable[[GameEndNotification], Any] | None = None

    # --- handler registration (decorators) ---

    def on_turn(self, game: str | None = None):
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

    def _handle_turn(self, data: dict[str, Any], turn_no: int | None = None) -> Response:
        game = data.get("game", "")
        handler = self._turn_handlers.get(game) or self._default_turn
        if handler is None:
            log.error("no turn handler registered for game %r", game)
            return 501, {"error": "no_turn_handler", "game": game}
        view = parse_view(data)
        # The turn-local usage accumulator lives HERE, in the one place both transports
        # share, rather than in each of them.
        #
        # It used to live only in the socket runtime, which meant an agent served over its
        # hosted endpoint — the manifest `endpoint.url` path, the one the platform's own
        # verification flow uses — captured nothing at all. No tokens, no cost, no model, no
        # scaffold fingerprint. Worse, the gateway identity headers are read off this
        # accumulator, so those agents also sent no turn proof and could NEVER earn Verified
        # no matter how faithfully they routed. A live webhook agent found it: 13 calls
        # proxied, all of them bound=false.
        #
        # The comment below this method already claimed both transports "run identical
        # decision logic". This is what makes that true.
        with turn_usage(
            match_id=data.get("match_id", "") or "",
            turn=turn_no if turn_no is not None else _turn_number(data),
            turn_proof=data.get("turn_proof", "") or "",
        ) as usage:
            status, move = self._invoke_turn(handler, game, view)
        # A developer-supplied `usage` always wins: manual reporting is an explicit choice
        # and must not be overwritten by what we happened to observe.
        if status == 200 and isinstance(move, dict) and not usage.empty and "usage" not in move:
            move["usage"] = usage.to_move_usage()
        self._count_calls_per_decision(usage.calls)
        return status, move

    # Running tally of model calls against decisions, for the `pyyol dev` feed.
    _calls_seen = 0
    _decisions_seen = 0
    _cost_warned = False

    def _count_calls_per_decision(self, calls: int) -> None:
        """Tell a developer when their agent is calling the model more than once per decision.

        # Why this is worth a line of output

        The single largest avoidable cost on this platform is an agent that reasons per EVENT
        rather than per decision. Mafia broadcasts an event for every line spoken, so an agent
        that calls its model inside an event handler makes roughly one call per message in the
        phase instead of one per turn — and free tiers are tight enough that it matters:
        OpenRouter allows 50 requests a DAY, Groq 6,000 tokens a minute.

        A developer cannot optimise what they cannot see, and nothing here showed it. The
        platform-side cost is visible on the model board; the ratio that causes it was not.

        # Why a RATIO and not a per-turn number

        One turn legitimately makes several calls — a best-of-N vote, a retry after a refusal,
        a tool loop. None of those is a mistake. What is worth flagging is the sustained
        average, so this waits for a handful of decisions before saying anything and then says
        it ONCE. A warning that fires every turn is one a developer learns to scroll past.
        """
        self._decisions_seen += 1
        self._calls_seen += max(0, calls)
        if self._cost_warned or self._decisions_seen < 5:
            return
        # 2.0 rather than 1.0: a retry or a best-of-2 is ordinary practice and must not be
        # called out. Sustained 2x+ is the shape that means "per event", not "per decision".
        if self._calls_seen >= self._decisions_seen * 2:
            self._cost_warned = True
            ratio = self._calls_seen / self._decisions_seen
            log.warning(
                "%d model calls for %d decisions (%.1f per decision). If you are calling your "
                "model inside an event handler, move it into step(): the turn view is already "
                "complete, and on a free tier of 50 requests/day this ratio is the difference "
                "between finishing a match and running out mid-game.",
                self._calls_seen, self._decisions_seen, ratio,
            )

    def _invoke_turn(self, handler, game: str, view) -> Response:
        try:
            move = handler(view)
            # Support `async def step`: async LLM clients are first-class, so an
            # awaitable move is run to completion here (the runtime/serve loops are
            # synchronous, so there's no already-running loop to clash with).
            if inspect.isawaitable(move):
                # isawaitable narrows to Awaitable; asyncio.run wants a Coroutine.
                move = asyncio.run(cast("Coroutine[Any, Any, Any]", move))
        except Exception as e:  # a crashing handler must not take the server down
            # Log the full traceback AND return the message so the runtime can surface
            # it in the `pyyol dev` feed — a silently-swallowed crash is the #1
            # "why doesn't my agent work" trap. The engine still applies a fallback.
            log.exception("turn handler raised for game %r", game)
            return 500, {"error": "handler_error", "message": str(e)}
        return 200, move_to_dict(move)

    # --- shared handler invocation (used by both the HTTP path and the socket
    # RuntimeConnector, so both transports run identical decision logic) ---

    def decide_turn(self, view_data: dict[str, Any], turn_no: int | None = None) -> Response:
        """Run the turn handler for a raw view dict; return ``(status, move)``.

        ``turn_no`` lets the socket runtime supply the round it already derived, for any view
        that does not publish one. Getting this wrong is not cosmetic — a turn proof is bound to
        (agent, match, round), so a round that disagrees with the platform's verifies against
        nothing and the decision silently fails to earn Verified.

        Monopoly USED to be the reason this parameter existed: its view carried no numeric
        round, so the runtime fell back to a per-match counter the server could not predict and
        no Monopoly turn could ever bind. The platform now publishes `round` (the engine's turn
        counter) and `_turn_number` reads it first, so the fallback is a safety net rather than
        the Monopoly path.
        """
        return self._handle_turn(view_data, turn_no=turn_no)

    def ack_initialize(self, data: dict[str, Any]) -> dict[str, Any]:
        """Run the initialize handler and return the ack dict."""
        ack = (
            self._on_initialize(InitializeRequest.from_dict(data)) if self._on_initialize else None
        )
        return ack if isinstance(ack, dict) else {"ready": True, "display_name": self.name}

    def notify_event(self, data: dict[str, Any]) -> None:
        if self._on_event:
            self._on_event(EventNotification.from_dict(data))

    def notify_game_end(self, data: dict[str, Any]) -> None:
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

    def initialize(self, ctx: InitializeRequest) -> Any:  # noqa: D401
        """Called at match start — but NOT guaranteed, and NOT once per match.

        You may be handed a match already in progress (after a reconnect, or when the
        platform attaches you to a running table), in which case your first callback
        is ``step`` and this never fires. One connection also serves many matches.

        So do NOT build per-match state here. Key it on ``view.match_id`` and create
        it lazily in ``step``. State initialised here and reused leaks across matches:
        the agent plays match two with match one's memory, which looks like a strategy
        bug and is not one.

        Return a dict ack or None. Optional.
        """
        return None

    def step(self, view: Any) -> Any:
        """Decide one move for ``view`` and return it. REQUIRED."""
        raise NotImplementedError("implement step(self, view) -> move")

    def shutdown(self, result: GameEndNotification) -> None:
        """Called when a match ends. Optional. Like ``initialize``, not guaranteed —
        a dropped connection ends the match without it."""
        return None

    def on_event(self, event: EventNotification) -> None:
        """Async match events (round results, opponent actions). Optional.

        This used to be unreachable: ``to_agent`` wired the transport's event hook to
        a no-op, so an Adapter silently received nothing no matter what it defined.
        For a game where reading the opponent IS the strategy, that quietly removed
        the information the agent needed and gave no indication it had.
        """
        return None

    def to_agent(self) -> Agent:
        """Build the underlying :class:`Agent` that drives the real transport."""
        a = Agent(
            secret=self.secret or os.environ.get("PYYOL_SECRET", ""),
            supported_games=self.supported_games,
            name=self.name,
        )
        a.on_turn()(self.step)
        a.on_initialize(self.initialize)
        a.on_game_end(self.shutdown)
        # Dispatch to the subclass hook rather than dropping the event on the floor.
        a.on_event(self.on_event)
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


def _load_json(body: bytes) -> dict[str, Any]:
    if not body:
        return {}
    try:
        obj = json.loads(body)
        return obj if isinstance(obj, dict) else {}
    except json.JSONDecodeError:
        return {}


def _turn_number(data: dict[str, Any]) -> int:
    """The round this view is asking about.

    Read defensively across the names the platform has used for it: a turn proof is bound to
    (agent, match, round), so a wrong round means the proof verifies against nothing and the
    decision silently fails to earn Verified.
    """
    # `day` is Mafia's round field. Leaving it out made X-Pyyol-Turn 0 for every Mafia turn,
    # which the existing gateway test caught the moment this logic moved.
    for key in ("round", "day", "turn", "round_no", "turn_no"):
        v = data.get(key)
        if isinstance(v, bool):
            continue
        if isinstance(v, int):
            return v
        if isinstance(v, str) and v.isdigit():
            return int(v)
    return 0
