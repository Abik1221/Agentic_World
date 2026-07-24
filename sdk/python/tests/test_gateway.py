"""Tests for verified-tier gateway routing (Phase 4c): URL resolution, per-turn
identity headers, header injection into instrumented calls, and route()."""

from __future__ import annotations

from types import SimpleNamespace

import pytest

from pyyol import instrument as instr
from pyyol.instrument import (
    disable_gateway,
    enable_gateway,
    gateway_base_url,
    gateway_headers,
    instrument,
    route,
    uninstrument,
)
from pyyol.telemetry import turn_usage


@pytest.fixture(autouse=True)
def _clean():
    yield
    disable_gateway()
    uninstrument()


def test_gateway_base_url_per_provider():
    enable_gateway("agentA", "https://gateway.pyyol.com/")
    assert gateway_base_url("openai") == "https://gateway.pyyol.com/gw/openai/v1"
    assert gateway_base_url("anthropic") == "https://gateway.pyyol.com/gw/anthropic"
    assert gateway_base_url("unknown") == ""


def test_gateway_base_url_empty_when_disabled():
    assert gateway_base_url("openai") == ""


def test_gateway_headers_empty_when_disabled():
    assert gateway_headers() == {}


def test_gateway_headers_reads_turn_context():
    enable_gateway("agentA", "https://gw")
    with turn_usage(match_id="m42", turn=3):
        h = gateway_headers()
    assert h["X-Pyyol-Key"] == "agentA"
    assert h["X-Pyyol-Match"] == "m42"
    assert h["X-Pyyol-Turn"] == "3"


def test_gateway_headers_without_match():
    enable_gateway("agentA", "https://gw")
    h = gateway_headers()  # outside a turn
    assert h == {"X-Pyyol-Key": "agentA"}


def test_inject_headers_merges_dev_wins():
    enable_gateway("agentA", "https://gw")
    with turn_usage(match_id="m1", turn=1):
        kwargs = {"extra_headers": {"X-Custom": "1", "X-Pyyol-Key": "dev-override"}}
        instr._inject_gateway_headers(kwargs)
    eh = kwargs["extra_headers"]
    assert eh["X-Custom"] == "1"
    assert eh["X-Pyyol-Match"] == "m1"
    assert eh["X-Pyyol-Key"] == "dev-override"  # dev-supplied wins


def test_inject_headers_noop_when_disabled():
    kwargs: dict = {}
    instr._inject_gateway_headers(kwargs)
    assert kwargs == {}


def test_route_sets_base_url_and_detects_provider():
    enable_gateway("agentA", "https://gw")

    class FakeOpenAI:
        base_url = "https://api.openai.com/v1"

    # give it an openai-ish module so provider detection works
    FakeOpenAI.__module__ = "openai._client"
    c = FakeOpenAI()
    route(c)
    assert c.base_url == "https://gw/gw/openai/v1"


def test_route_noop_when_disabled():
    class FakeOpenAI:
        base_url = "orig"

    FakeOpenAI.__module__ = "openai._client"
    c = FakeOpenAI()
    route(c)
    assert c.base_url == "orig"


def _fake_openai_module(recorder):
    import sys
    import types

    names = ["openai", "openai.resources", "openai.resources.chat", "openai.resources.chat.completions"]
    for n in names:
        sys.modules[n] = types.ModuleType(n)

    class Completions:
        def create(self, *args, **kwargs):
            recorder["kwargs"] = kwargs
            return SimpleNamespace(
                model="gpt-4o",
                usage=SimpleNamespace(prompt_tokens=10, completion_tokens=5),
            )

    sys.modules["openai.resources.chat.completions"].Completions = Completions
    return Completions, names


def test_instrumented_call_injects_headers(monkeypatch):
    import sys

    recorder: dict = {}
    Completions, names = _fake_openai_module(recorder)
    try:
        instrument(["openai"])
        enable_gateway("agentA", "https://gw")
        client = Completions()
        with turn_usage(match_id="m7", turn=2):
            client.create(model="gpt-4o", messages=[])
        eh = recorder["kwargs"]["extra_headers"]
        assert eh["X-Pyyol-Key"] == "agentA"
        assert eh["X-Pyyol-Match"] == "m7"
        assert eh["X-Pyyol-Turn"] == "2"
    finally:
        for n in names:
            sys.modules.pop(n, None)


class _FakeWS:
    def __init__(self, incoming):
        import json
        import queue as _q

        self._in = _q.Queue()
        for f in incoming:
            self._in.put(json.dumps(f))
        self.sent = []

    def send(self, msg):
        import json

        self.sent.append(json.loads(msg))

    def recv(self, *_a, **_k):
        import queue as _q

        try:
            return self._in.get_nowait()
        except _q.Empty:
            raise ConnectionError("closed")

    def close(self):
        pass


def test_turn_attribution_uses_day_for_mafia(monkeypatch):
    """Regression: X-Pyyol-Turn was always 0 for Mafia/Monopoly (no `round`). It must
    use `day` (Mafia) so per-turn telemetry attribution isn't collapsed."""
    import sys

    from pyyol import Agent
    from pyyol.runtime import RuntimeConnector

    recorder: dict = {}
    Completions, names = _fake_openai_module(recorder)
    try:
        instrument(["openai"])
        enable_gateway("agentM", "https://gw")
        client = Completions()

        agent = Agent(supported_games=["mafia"], name="t")

        @agent.on_turn("mafia")
        def decide(v):
            client.create(model="gpt-4o", messages=[])
            return {"action": "vote", "target": 1}

        ws = _FakeWS(
            [
                {"t": "hello", "version": "1.0"},
                {"t": "registered", "agent_id": "ag"},
                {"t": "turn", "id": "r1", "payload": {"game": "mafia", "match_id": "m1", "day": 3, "your_seat": 0, "legal": ["vote"]}},
            ]
        )
        conn = RuntimeConnector(
            agent, url="ws://x", agent_id="agentM", token="s", games=["mafia"],
            heartbeat_interval=100, _connect=lambda *a, **k: ws,
        )
        try:
            conn._session()
        except ConnectionError:
            pass

        eh = recorder["kwargs"]["extra_headers"]
        assert eh["X-Pyyol-Turn"] == "3"  # the Mafia `day`, not 0
        assert eh["X-Pyyol-Match"] == "m1"
    finally:
        for n in names:
            sys.modules.pop(n, None)
