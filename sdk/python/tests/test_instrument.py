"""Unit + integration tests for automatic LLM usage capture.

Covers the pure extractor (extract_usage), cost/accumulation (record_response),
the monkeypatch mechanism (instrument/uninstrument against a fake provider module),
and the end-to-end auto-attach of `usage` to a move through the runtime.
"""

from __future__ import annotations

import json
import queue
import sys
import types
from types import SimpleNamespace

import pytest

from pyyol import _instrument as instr
from pyyol._instrument import extract_usage, instrument, record_response, uninstrument
from pyyol.telemetry import turn_usage


@pytest.fixture(autouse=True)
def _clean_instrumentation():
    """Restore any patched methods after every test so instrumentation state never
    leaks between tests (patches live on module globals)."""
    yield
    uninstrument()


# --- fake provider responses ---------------------------------------------------


def _openai_chat_resp(model="gpt-4o", prompt=1200, completion=80, cached=0, reasoning=0):
    return SimpleNamespace(
        model=model,
        usage=SimpleNamespace(
            prompt_tokens=prompt,
            completion_tokens=completion,
            total_tokens=prompt + completion,
            prompt_tokens_details=SimpleNamespace(cached_tokens=cached),
            completion_tokens_details=SimpleNamespace(reasoning_tokens=reasoning),
        ),
    )


def _anthropic_resp(model="claude-sonnet-4-5", inp=900, out=120, cache_read=0, cache_write=0):
    return SimpleNamespace(
        model=model,
        usage=SimpleNamespace(
            input_tokens=inp,
            output_tokens=out,
            cache_read_input_tokens=cache_read,
            cache_creation_input_tokens=cache_write,
        ),
    )


def _responses_api_resp(model="gpt-4.1", inp=500, out=40):
    return SimpleNamespace(
        model=model,
        usage=SimpleNamespace(input_tokens=inp, output_tokens=out),
    )


# --- extract_usage -------------------------------------------------------------


def test_extract_openai_chat():
    info = extract_usage(_openai_chat_resp(cached=300, reasoning=20))
    assert info == {
        "model": "gpt-4o",
        "provider": "openai",
        "prompt_tokens": 1200,
        "completion_tokens": 80,
        "cached_tokens": 300,
        "cached_write_tokens": 0,
        "reasoning_tokens": 20,
    }


def test_extract_anthropic():
    info = extract_usage(_anthropic_resp(cache_read=100))
    assert info["provider"] == "anthropic"
    # 900 uncached + 100 cache reads. Anthropic's `input_tokens` counts only the
    # uncached remainder, so the cache fields are ADDED to recover billable input —
    # unlike OpenAI, where `prompt_tokens` already contains them.
    assert info["prompt_tokens"] == 1000 and info["completion_tokens"] == 120
    assert info["cached_tokens"] == 100


def test_openai_cached_tokens_stay_a_subset_and_are_not_double_counted():
    """The mirror image of the Anthropic case. OpenAI reports cached tokens INSIDE
    prompt_tokens, so adding them would inflate billable input — the same normalization
    must not fire here."""
    info = extract_usage(_openai_chat_resp(prompt=1200, cached=300))
    assert info["prompt_tokens"] == 1200
    assert info["cached_tokens"] == 300


def test_extract_anthropic_cache_write():
    """Cache CREATION tokens are billed at 1.25x input and were previously not read at
    all, so a cache-heavy agent's most expensive tokens were recorded as zero."""
    info = extract_usage(_anthropic_resp(inp=420, out=90, cache_read=1500, cache_write=600))
    assert info["cached_write_tokens"] == 600
    assert info["cached_tokens"] == 1500
    # Every billable input token is accounted for: 420 uncached + 1500 read + 600 written.
    assert info["prompt_tokens"] == 2520


def test_extract_responses_api():
    info = extract_usage(_responses_api_resp())
    assert info["prompt_tokens"] == 500 and info["completion_tokens"] == 40
    assert info["model"] == "gpt-4.1"


def test_extract_dict_response():
    info = extract_usage(
        {"model": "gpt-4o", "usage": {"prompt_tokens": 10, "completion_tokens": 5}}
    )
    assert info["prompt_tokens"] == 10 and info["completion_tokens"] == 5


def test_extract_none_when_no_usage():
    assert extract_usage(SimpleNamespace(model="gpt-4o")) is None
    assert extract_usage({}) is None


# --- record_response: accumulation + cost --------------------------------------


def test_record_response_accumulates_and_costs():
    with turn_usage() as acc:
        record_response(_openai_chat_resp(prompt=1000, completion=500))
        assert acc.calls == 1
        assert acc.prompt_tokens == 1000 and acc.completion_tokens == 500
        assert acc.total_tokens == 1500
        # gpt-4o: (1000*2.5 + 500*10)/1e6
        assert acc.estimated_cost == pytest.approx((1000 * 2.5 + 500 * 10) / 1_000_000)
        assert acc.models == ["gpt-4o"] and acc.providers == ["openai"]


def test_record_response_sums_multiple_calls_in_a_turn():
    with turn_usage() as acc:
        record_response(_openai_chat_resp(prompt=100, completion=10))
        record_response(_anthropic_resp(inp=200, out=20))
        assert acc.calls == 2
        assert acc.prompt_tokens == 300 and acc.completion_tokens == 30
        assert set(acc.providers) == {"openai", "anthropic"}


def test_record_response_outside_turn_is_safe_noop():
    # No active accumulator → returns the info but records nowhere, never raises.
    info = record_response(_openai_chat_resp())
    assert info is not None


def test_to_move_usage_shape_matches_arena_contract():
    with turn_usage() as acc:
        record_response(_openai_chat_resp(prompt=100, completion=50, reasoning=10))
        usage = acc.to_move_usage()
    # Keys the arena's TokenUsage decode reads:
    assert usage["prompt_tokens"] == 100
    assert usage["completion_tokens"] == 50
    assert usage["total_tokens"] == 150
    assert usage["reasoning_tokens"] == 10
    assert usage["model"] == "gpt-4o"
    assert usage["estimated_cost"] > 0


# --- instrument()/uninstrument() against a fake provider module ----------------


@pytest.fixture()
def fake_openai():
    """Install a minimal fake `openai.resources.chat.completions.Completions` in
    sys.modules and restore whatever was there afterwards."""
    names = [
        "openai",
        "openai.resources",
        "openai.resources.chat",
        "openai.resources.chat.completions",
    ]
    saved = {n: sys.modules.get(n) for n in names}

    for n in names:
        sys.modules[n] = types.ModuleType(n)

    class Completions:
        def create(self, *args, **kwargs):
            return _openai_chat_resp(prompt=1000, completion=100)

    sys.modules["openai.resources.chat.completions"].Completions = Completions
    try:
        yield Completions
    finally:
        uninstrument()
        for n, mod in saved.items():
            if mod is None:
                sys.modules.pop(n, None)
            else:
                sys.modules[n] = mod


def test_instrument_patches_and_captures(fake_openai):
    done = instrument(["openai"])
    assert "openai" in done
    # A second call is idempotent (already patched).
    assert instrument(["openai"]) == []

    client = fake_openai()
    with turn_usage() as acc:
        resp = client.create(model="gpt-4o", messages=[{"role": "user", "content": "hi"}])
        # The real response is returned untouched to the caller...
        assert resp.usage.prompt_tokens == 1000
        # ...and usage was captured automatically, no manual logging.
        assert acc.calls == 1 and acc.prompt_tokens == 1000 and acc.completion_tokens == 100
        assert acc.estimated_cost > 0

    uninstrument()
    # After uninstrument the method no longer records.
    with turn_usage() as acc2:
        fake_openai().create(model="gpt-4o", messages=[])
        assert acc2.calls == 0


def test_instrument_unknown_provider_is_skipped():
    # An unsupported provider name is filtered out; nothing is patched, no error.
    done = instrument(["definitely-not-a-provider"])
    assert done == []


def test_instrument_absent_module_returns_false(monkeypatch):
    # When a provider module can't be imported, the patch is skipped gracefully.
    def _boom(name):
        raise ImportError(name)

    monkeypatch.setattr(instr.importlib, "import_module", _boom)
    assert (
        instr._patch_method("openai.resources.chat.completions", "Completions", "create", "openai")
        is False
    )


# --- end-to-end: auto-attach usage to the move through the runtime -------------


class FakeWS:
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


def _run_turn(agent):
    from pyyol.runtime import RuntimeConnector

    turn_view = {
        "game": "goofspiel",
        "match_id": "m1",
        "round": 2,
        "your_hand": [3, 7, 9],
        "legal_actions": [3, 7, 9],
        "seat": 0,
    }
    ws = FakeWS(
        [
            {"t": "hello", "version": "1.0"},
            {"t": "registered", "agent_id": "ag"},
            {"t": "turn", "id": "r1", "payload": turn_view},
        ]
    )
    conn = RuntimeConnector(
        agent,
        url="ws://x",
        agent_id="ag",
        token="s",
        games=["goofspiel"],
        heartbeat_interval=100,
        _connect=lambda *a, **k: ws,
    )
    try:
        conn._session()
    except ConnectionError:
        pass
    return next(f for f in ws.sent if f["t"] == "response" and f["id"] == "r1")


def test_runtime_auto_attaches_usage(fake_openai):
    from pyyol import Agent

    instrument(["openai"])
    client = fake_openai()

    agent = Agent(supported_games=["goofspiel"], name="t")

    @agent.on_turn("goofspiel")
    def decide(v):
        # ordinary LLM call inside the handler — no telemetry code
        client.create(model="gpt-4o", messages=[{"role": "user", "content": "pick"}])
        return {"round": v.round, "card": max(v.legal_actions)}

    resp = _run_turn(agent)
    assert resp["payload"]["card"] == 9  # move preserved
    usage = resp["payload"]["usage"]  # usage auto-attached
    assert usage["prompt_tokens"] == 1000 and usage["completion_tokens"] == 100
    assert usage["total_tokens"] == 1100
    assert usage["model"] == "gpt-4o" and usage["estimated_cost"] > 0


def test_runtime_respects_dev_supplied_usage(fake_openai):
    from pyyol import Agent

    instrument(["openai"])
    client = fake_openai()
    agent = Agent(supported_games=["goofspiel"], name="t")

    @agent.on_turn("goofspiel")
    def decide(v):
        client.create(model="gpt-4o", messages=[])
        # Dev reports their own usage explicitly — must NOT be overwritten.
        return {
            "round": v.round,
            "card": 3,
            "usage": {"prompt_tokens": 42, "completion_tokens": 0, "total_tokens": 42},
        }

    resp = _run_turn(agent)
    assert resp["payload"]["usage"]["prompt_tokens"] == 42


def test_runtime_no_llm_call_no_usage_key():
    from pyyol import Agent

    agent = Agent(supported_games=["goofspiel"], name="t")

    @agent.on_turn("goofspiel")
    def decide(v):
        return {"round": v.round, "card": 7}

    resp = _run_turn(agent)
    assert "usage" not in resp["payload"]


# --- The HOSTED-ENDPOINT (webhook) transport ------------------------------------
#
# These cover the path a live agent exposed as broken: the usage accumulator was installed
# only by the socket runtime, so an agent served over its manifest `endpoint.url` — the path
# the platform's own verification flow uses — captured NOTHING. No tokens, no cost, no model,
# no scaffold. And because the gateway identity headers are read off that accumulator, those
# agents sent no turn proof either and could never earn Verified however faithfully they
# routed. The live run showed 13 calls proxied with bound=false on every one.


def _post_turn(agent, view=None):
    """Drive the webhook transport the way the platform does: an unsigned POST to /play."""
    body = json.dumps(
        view
        or {
            "game": "goofspiel",
            "match_id": "m_webhook",
            "round": 4,
            "turn_proof": "proof-for-round-4",
            "your_hand": [3, 7, 9],
            "legal_actions": [3, 7, 9],
        }
    ).encode()
    status, payload = agent.handle("POST", "/play", {}, body)
    assert status == 200, payload
    return payload


def test_webhook_transport_attaches_usage(fake_openai):
    """The regression itself. A hosted-endpoint agent must report the same usage a
    socket-connected one does — the transport is not supposed to change what is measured."""
    from pyyol import Agent

    instrument(["openai"])
    client = fake_openai()
    agent = Agent(supported_games=["goofspiel"], name="t")

    @agent.on_turn("goofspiel")
    def decide(v):
        client.create(model="gpt-4o", messages=[{"role": "user", "content": "pick"}])
        return {"round": v.round, "card": max(v.legal_actions)}

    move = _post_turn(agent)
    assert move["card"] == 9  # the move still works
    usage = move.get("usage")
    assert usage, "no usage attached over the webhook transport — the verified tier is unreachable"
    assert usage["prompt_tokens"] == 1000 and usage["completion_tokens"] == 100
    assert usage["model"] == "gpt-4o" and usage["estimated_cost"] > 0


def test_webhook_transport_exposes_the_turn_proof_to_the_gateway(fake_openai):
    """The reason the missing accumulator cost Verified rather than only analytics.

    The gateway identity headers are built from the turn-local accumulator, so with no
    accumulator there is no match, no round and no proof on the outgoing call — and an
    unproven call is forwarded but never credited.
    """
    from pyyol import Agent
    from pyyol._instrument import disable_gateway, enable_gateway, gateway_headers

    instrument(["openai"])
    client = fake_openai()
    enable_gateway("sk_arena_testkey", "http://pyyol.test/v1")
    seen = {}
    agent = Agent(supported_games=["goofspiel"], name="t")

    @agent.on_turn("goofspiel")
    def decide(v):
        seen.update(gateway_headers())
        client.create(model="gpt-4o", messages=[{"role": "user", "content": "pick"}])
        return {"round": v.round, "card": max(v.legal_actions)}

    try:
        _post_turn(agent)
    finally:
        disable_gateway()

    assert seen.get("X-Pyyol-Match") == "m_webhook"
    assert seen.get("X-Pyyol-Turn") == "4"
    assert seen.get("X-Pyyol-Proof") == "proof-for-round-4"


def test_webhook_transport_respects_dev_supplied_usage(fake_openai):
    """Manual reporting is an explicit choice and must not be overwritten by observation."""
    from pyyol import Agent

    instrument(["openai"])
    client = fake_openai()
    agent = Agent(supported_games=["goofspiel"], name="t")

    @agent.on_turn("goofspiel")
    def decide(v):
        client.create(model="gpt-4o", messages=[{"role": "user", "content": "pick"}])
        return {"round": v.round, "card": max(v.legal_actions), "usage": {"prompt_tokens": 7}}

    assert _post_turn(agent)["usage"] == {"prompt_tokens": 7}


def test_webhook_transport_omits_usage_when_no_model_was_called():
    """A rules-based agent must not ship an empty usage block; absent and zero are different
    claims, and only one of them is true."""
    from pyyol import Agent

    agent = Agent(supported_games=["goofspiel"], name="t")

    @agent.on_turn("goofspiel")
    def decide(v):
        return {"round": v.round, "card": max(v.legal_actions)}

    assert "usage" not in _post_turn(agent)


def test_mafia_day_is_used_as_the_round_over_the_webhook_transport(fake_openai):
    """Mafia calls its round `day`. Missing it made every Mafia proof bind to round 0, so a
    whole game's decisions failed verification for a field-name reason."""
    from pyyol import Agent
    from pyyol._instrument import disable_gateway, enable_gateway, gateway_headers

    instrument(["openai"])
    client = fake_openai()
    enable_gateway("sk_arena_testkey", "http://pyyol.test/v1")
    seen = {}
    agent = Agent(supported_games=["mafia"], name="t")

    @agent.on_turn("mafia")
    def decide(v):
        seen.update(gateway_headers())
        client.create(model="gpt-4o", messages=[{"role": "user", "content": "who"}])
        return {"action": "vote", "target": 1}

    try:
        agent.handle(
            "POST",
            "/play",
            {},
            json.dumps(
                {
                    "game": "mafia",
                    "match_id": "m_mafia",
                    "day": 3,
                    "turn_proof": "p3",
                    "alive": {"0": True, "1": True},
                    "your_role": "villager",
                    "seat": 0,
                }
            ).encode(),
        )
    finally:
        disable_gateway()

    assert seen.get("X-Pyyol-Turn") == "3"
