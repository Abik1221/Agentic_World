"""Runtime connector tests — frame dispatch + register handshake against a fake
socket, so the SDK's own suite proves the connector without a live Go server.
The true cross-language proof lives in the backend (agentgw crosslang test)."""

import json
import queue

import pytest

from pyyol import Agent
from pyyol.runtime import ConnectorError, RuntimeConnector


class FakeWS:
    """A scripted WebSocket: recv() drains a queue of server frames; send()
    records what the connector emits. When the queue empties, recv() raises to
    end the session (simulating the socket closing)."""

    def __init__(self, incoming):
        self._in = queue.Queue()
        for f in incoming:
            self._in.put(json.dumps(f))
        self.sent = []

    def send(self, msg):
        self.sent.append(json.loads(msg))

    def recv(self, *_a, **_k):
        try:
            return self._in.get_nowait()
        except queue.Empty:
            raise ConnectionError("closed")

    def close(self):
        pass


def _agent():
    a = Agent(supported_games=["goofspiel"], name="t")

    @a.on_turn("goofspiel")
    def decide(v):
        return {"round": v.round, "card": max(v.legal_actions)}

    return a


def _run_session(agent, incoming, **kw):
    ws = FakeWS(incoming)
    conn = RuntimeConnector(
        agent,
        url="ws://x",
        agent_id="ag",
        token="s",
        games=["goofspiel"],
        heartbeat_interval=100,
        _connect=lambda *a, **k: ws,
        **kw,
    )
    try:
        conn._session()
    except ConnectionError:
        pass  # normal end-of-script
    return ws


def test_register_handshake_and_turn():
    turn_view = {
        "game": "goofspiel",
        "round": 2,
        "your_hand": [3, 7, 9],
        "legal_actions": [3, 7, 9],
        "current_prize": 5,
        "prize_pool": 5,
        "scores": [0, 0],
        "seat": 0,
    }
    ws = _run_session(
        _agent(),
        [
            {"t": "hello", "version": "1.0"},
            {"t": "registered", "agent_id": "ag"},
            {"t": "turn", "id": "r1", "payload": turn_view},
        ],
    )
    # First sent frame is the register with capabilities.
    reg = ws.sent[0]
    assert reg["t"] == "register" and reg["games"] == ["goofspiel"] and reg["token"] == "s"
    # The turn produced a correlated response with the highest legal card.
    resp = next(f for f in ws.sent if f["t"] == "response" and f["id"] == "r1")
    assert resp["payload"] == {"round": 2, "card": 9}


def test_ping_gets_pong():
    ws = _run_session(
        _agent(),
        [
            {"t": "hello"},
            {"t": "registered", "agent_id": "ag"},
            {"t": "ping", "id": "hb1"},
        ],
    )
    assert any(f["t"] == "pong" and f["id"] == "hb1" for f in ws.sent)


def test_event_and_game_end_callbacks_fire():
    got = {"events": [], "end": None}
    a = _agent()

    @a.on_event
    def on_ev(n):
        got["events"].append((n.type, n.seq))

    @a.on_game_end
    def on_end(n):
        got["end"] = n.result

    _run_session(
        a,
        [
            {"t": "hello"},
            {"t": "registered", "agent_id": "ag"},
            {
                "t": "event",
                "game": "goofspiel",
                "match_id": "m",
                "seq": 4,
                "kind": "round_revealed",
                "payload": {"prize": 5},
            },
            {
                "t": "game_end",
                "game": "goofspiel",
                "match_id": "m",
                "payload": {"winner": 0, "scores": [7, 3]},
            },
        ],
    )
    assert got["events"] == [("round_revealed", 4)]
    assert got["end"] == {"winner": 0, "scores": [7, 3]}


def test_bad_token_is_terminal():
    # No refresh creds → a rejected register is terminal (must re-login).
    ws = FakeWS([{"t": "hello"}, {"t": "error", "error": "unauthorized", "reason": "nope"}])
    conn = RuntimeConnector(_agent(), url="ws://x", token="bad", _connect=lambda *a, **k: ws)
    with pytest.raises(ConnectorError):
        conn._session()


def test_register_error_refreshes_and_retries():
    """An expired access token → the connector spends the refresh token for a fresh
    one, persists the rotated pair, and signals a reconnect (not a terminal error)."""
    from pyyol.runtime import _RefreshRetry

    persisted = {}

    def fake_refresh(api_url, rt):
        assert api_url == "http://api" and rt == "old-rt"
        return ("new-access", "new-rt")

    ws = FakeWS([{"t": "hello"}, {"t": "error", "error": "unauthorized", "reason": "token expired"}])
    conn = RuntimeConnector(
        _agent(),
        url="ws://x",
        agent_id="ag",
        token="expired",
        refresh_token="old-rt",
        api_url="http://api",
        on_tokens=lambda a, r: persisted.update(access=a, refresh=r),
        _connect=lambda *a, **k: ws,
        _refresh_http=fake_refresh,
    )
    with pytest.raises(_RefreshRetry):
        conn._session()
    assert conn.token == "new-access"  # rotated in memory
    assert conn.refresh_token == "new-rt"
    assert persisted == {"access": "new-access", "refresh": "new-rt"}  # persisted to disk


def test_refresh_failure_is_terminal():
    """When the refresh token itself is dead (endpoint returns nothing), the session
    is terminally unauthenticated — no infinite retry."""
    ws = FakeWS([{"t": "hello"}, {"t": "error", "error": "unauthorized"}])
    conn = RuntimeConnector(
        _agent(),
        url="ws://x",
        token="expired",
        refresh_token="old-rt",
        api_url="http://api",
        _connect=lambda *a, **k: ws,
        _refresh_http=lambda *a: None,
    )
    with pytest.raises(ConnectorError):
        conn._session()


class RecordingConsole:
    """Captures the lifecycle events the connector emits, for assertions."""

    def __init__(self):
        self.events = []

    def banner(self, name, url):
        pass

    def emit(self, kind, msg, **fields):
        self.events.append((kind, msg, fields))


def test_console_receives_lifecycle_events():
    a = _agent()
    console = RecordingConsole()
    ws = FakeWS(
        [
            {"t": "hello"},
            {"t": "registered", "agent_id": "ag"},
            {
                "t": "initialize",
                "id": "i1",
                "payload": {"match_id": "m1", "game": "goofspiel", "seat": 0},
            },
            {
                "t": "turn",
                "id": "t1",
                "payload": {
                    "game": "goofspiel",
                    "round": 1,
                    "your_hand": [3, 7, 9],
                    "legal_actions": [3, 7, 9],
                },
            },
            {
                "t": "event",
                "game": "goofspiel",
                "match_id": "m1",
                "seq": 1,
                "kind": "round_revealed",
                "payload": {},
            },
            {
                "t": "game_end",
                "game": "goofspiel",
                "match_id": "m1",
                "payload": {"winner": 0, "coins_delta": 18},
            },
        ]
    )
    conn = RuntimeConnector(
        a,
        url="ws://x",
        agent_id="ag",
        token="s",
        games=["goofspiel"],
        console=console,
        _connect=lambda *args, **kw: ws,
    )
    try:
        conn._session()
    except ConnectionError:
        pass

    kinds = [e[0] for e in console.events]
    # Connecting → connected → waiting → match → decision → event → game_end → waiting.
    assert kinds == [
        "connecting", "connected", "waiting", "match", "decision", "event", "game_end", "waiting",
    ]
    decision = next(e for e in console.events if e[0] == "decision")
    assert "bid 9" in decision[1]  # the move is summarized
    assert "ms" in decision[2]  # latency is captured
    # No event ever carries the token/secret.
    assert all("s" not in str(f.values()) or "token" not in f for _, _, f in console.events)


def test_insecure_ws_warns_without_leaking_token():
    a = _agent()
    console = RecordingConsole()
    conn = RuntimeConnector(
        a,
        url="ws://example.com/connect",
        token="supersecret",
        games=["goofspiel"],
        reconnect=False,
        console=console,
        _connect=lambda *args, **kw: FakeWS([{"t": "hello"}, {"t": "registered"}]),
    )
    try:
        conn.run()
    except Exception:
        pass
    warn = next((e for e in console.events if e[0] == "warn"), None)
    assert warn is not None and "cleartext" in warn[1]
    assert "supersecret" not in warn[1]  # never leak the token


def test_handler_error_sends_error_response():
    a = Agent(supported_games=["goofspiel"])

    @a.on_turn("goofspiel")
    def boom(v):
        raise RuntimeError("strategy blew up")

    ws = _run_session(
        a,
        [
            {"t": "hello"},
            {"t": "registered", "agent_id": "ag"},
            {
                "t": "turn",
                "id": "r9",
                "payload": {"game": "goofspiel", "legal_actions": [1], "your_hand": [1]},
            },
        ],
    )
    resp = next(f for f in ws.sent if f["t"] == "response" and f["id"] == "r9")
    assert resp.get("error")  # signals the platform to apply its fallback


def test_register_sends_sdk_language_and_newer_latest_triggers_one_nudge():
    a = _agent()
    console = RecordingConsole()
    ws = _run_session(
        a,
        [
            {"t": "hello"},
            {"t": "registered", "agent_id": "ag", "latest_sdk": "9.9.9"},
        ],
        console=console,
    )
    assert ws.sent[0]["sdk_language"] == "python"
    upgrades = [e for e in console.events if e[0] == "upgrade"]
    assert len(upgrades) == 1
    assert "9.9.9" in upgrades[0][1]


def test_older_or_equal_latest_triggers_no_nudge():
    a = _agent()
    console = RecordingConsole()
    _run_session(
        a,
        [
            {"t": "hello"},
            {"t": "registered", "agent_id": "ag", "latest_sdk": "0.0.1"},
        ],
        console=console,
    )
    assert [e for e in console.events if e[0] == "upgrade"] == []
