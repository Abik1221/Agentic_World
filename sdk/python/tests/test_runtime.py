"""Runtime connector tests — frame dispatch + register handshake against a fake
socket, so the SDK's own suite proves the connector without a live Go server.
The true cross-language proof lives in the backend (agentgw crosslang test)."""

import json
import queue

import pytest

from onavion import Agent
from onavion.runtime import ConnectorError, RuntimeConnector


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
    conn = RuntimeConnector(agent, url="ws://x", agent_id="ag", token="s",
                            games=["goofspiel"], heartbeat_interval=100, _connect=lambda *a, **k: ws, **kw)
    try:
        conn._session()
    except ConnectionError:
        pass  # normal end-of-script
    return ws


def test_register_handshake_and_turn():
    turn_view = {"game": "goofspiel", "round": 2, "your_hand": [3, 7, 9],
                 "legal_actions": [3, 7, 9], "current_prize": 5, "prize_pool": 5,
                 "scores": [0, 0], "seat": 0}
    ws = _run_session(_agent(), [
        {"t": "hello", "version": "1.0"},
        {"t": "registered", "agent_id": "ag"},
        {"t": "turn", "id": "r1", "payload": turn_view},
    ])
    # First sent frame is the register with capabilities.
    reg = ws.sent[0]
    assert reg["t"] == "register" and reg["games"] == ["goofspiel"] and reg["token"] == "s"
    # The turn produced a correlated response with the highest legal card.
    resp = next(f for f in ws.sent if f["t"] == "response" and f["id"] == "r1")
    assert resp["payload"] == {"round": 2, "card": 9}


def test_ping_gets_pong():
    ws = _run_session(_agent(), [
        {"t": "hello"}, {"t": "registered", "agent_id": "ag"},
        {"t": "ping", "id": "hb1"},
    ])
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

    _run_session(a, [
        {"t": "hello"}, {"t": "registered", "agent_id": "ag"},
        {"t": "event", "game": "goofspiel", "match_id": "m", "seq": 4,
         "kind": "round_revealed", "payload": {"prize": 5}},
        {"t": "game_end", "game": "goofspiel", "match_id": "m",
         "payload": {"winner": 0, "scores": [7, 3]}},
    ])
    assert got["events"] == [("round_revealed", 4)]
    assert got["end"] == {"winner": 0, "scores": [7, 3]}


def test_bad_token_is_terminal():
    ws = FakeWS([{"t": "hello"}, {"t": "error", "error": "unauthorized", "reason": "nope"}])
    conn = RuntimeConnector(_agent(), url="ws://x", token="bad", _connect=lambda *a, **k: ws)
    with pytest.raises(ConnectorError):
        conn._session()


def test_handler_error_sends_error_response():
    a = Agent(supported_games=["goofspiel"])

    @a.on_turn("goofspiel")
    def boom(v):
        raise RuntimeError("strategy blew up")

    ws = _run_session(a, [
        {"t": "hello"}, {"t": "registered", "agent_id": "ag"},
        {"t": "turn", "id": "r9", "payload": {"game": "goofspiel", "legal_actions": [1], "your_hand": [1]}},
    ])
    resp = next(f for f in ws.sent if f["t"] == "response" and f["id"] == "r9")
    assert resp.get("error")  # signals the platform to apply its fallback
