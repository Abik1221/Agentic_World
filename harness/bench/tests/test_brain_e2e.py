"""End-to-end through the real brain against a stand-in provider. No network, no spend.

This is the test that would have caught the failure the model board was actually bitten by:
a provider that answers with an error and zero tokens, whose call is nonetheless recorded as
a usable decision. Here that path is driven deliberately and the assertion is that the brain
falls back, records the failure, and settles the budget at zero — rather than returning a
move nobody chose.

The stand-in is a real HTTP server speaking the OpenAI wire format, not a mock object,
because the thing under test includes the OpenAI SDK's own request building and response
parsing. A mock would prove the brain agrees with itself.
"""

from __future__ import annotations

import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from types import SimpleNamespace

import pytest

from pyyolbench.brain import Brain
from pyyolbench.budget import Budget
from pyyolbench.games.goofspiel import GoofspielPolicy
from pyyolbench.ledger import Ledger
from pyyolbench.models import RANKED


class _Provider(BaseHTTPRequestHandler):
    """An OpenAI-wire stand-in whose behaviour the test dictates per instance."""

    mode = "tool_call"
    last_request: dict = {}

    def do_POST(self):  # noqa: N802
        n = int(self.headers.get("Content-Length", 0) or 0)
        body = json.loads(self.rfile.read(n) or b"{}")
        type(self).last_request = body

        if self.mode == "http_429":
            self.send_response(429)
            self.send_header("Content-Type", "application/json")
            payload = json.dumps({"error": {"message": "rate limited"}}).encode()
            self.send_header("Content-Length", str(len(payload)))
            self.end_headers()
            self.wfile.write(payload)
            return

        if self.mode == "prose_only":
            msg = {"role": "assistant", "content": "I'll play the 7."}
        else:
            msg = {
                "role": "assistant",
                "content": "Their highest is 12; 11 wins this pool cheaply.",
                "tool_calls": [
                    {
                        "id": "call_1",
                        "type": "function",
                        "function": {"name": "play_card", "arguments": json.dumps({"card": 11})},
                    }
                ],
            }
        out = {
            "id": "chatcmpl-1",
            "object": "chat.completion",
            "model": body.get("model", "x"),
            "choices": [{"index": 0, "message": msg, "finish_reason": "tool_calls"}],
            "usage": {
                "prompt_tokens": 1500,
                "completion_tokens": 300,
                "prompt_tokens_details": {"cached_tokens": 1200},
                "completion_tokens_details": {"reasoning_tokens": 210},
            },
        }
        payload = json.dumps(out).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)

    def log_message(self, *_a):
        pass


@pytest.fixture
def provider():
    srv = ThreadingHTTPServer(("127.0.0.1", 0), _Provider)
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    yield srv, f"http://127.0.0.1:{srv.server_address[1]}/v1"
    srv.shutdown()


def make_brain(tmp_path, base_url, limit=10.0):
    from openai import OpenAI

    budget = Budget(tmp_path / "budget.json", limit)
    ledger = Ledger(tmp_path / "l.jsonl")
    client = OpenAI(api_key="test-key", base_url=base_url)
    return Brain(
        model=RANKED["opus-5"],
        policy=GoofspielPolicy(),
        client=client,
        budget=budget,
        ledger=ledger,
    ), budget, ledger


def view(**kw):
    d = {
        "match_id": "m-e2e",
        "seat": 0,
        "round": 3,
        "current_prize": 9,
        "prize_pool": 9,
        "your_hand": [2, 5, 7, 11],
        "legal_actions": [2, 5, 7, 11],
        "scores": [12, 8],
        "history": [
            {"round": 1, "prize": 4, "prize_pool": 4, "your_card": 3, "opp_card": 1,
             "winner": 0, "scores": [4, 0]},
            {"round": 2, "prize": 8, "prize_pool": 8, "your_card": 12, "opp_card": 13,
             "winner": 1, "scores": [4, 8]},
        ],
    }
    d.update(kw)
    ns = SimpleNamespace(**d)
    ns.raw = d
    return ns


def read_ledger(path):
    return [json.loads(x) for x in path.read_text().splitlines() if x.strip()]


def test_happy_path_binds_and_bills(tmp_path, provider):
    _srv, url = provider
    _Provider.mode = "tool_call"
    brain, budget, ledger = make_brain(tmp_path, url)

    move = brain.decide(view())
    assert move == {"card": 11, "round": 3}

    recs = read_ledger(tmp_path / "l.jsonl")
    kinds = [r["kind"] for r in recs]
    assert "call_start" in kinds and "call_end" in kinds and "decision" in kinds

    end = next(r for r in recs if r["kind"] == "call_end")
    # The canonical bound form the gateway will store, computed by the SDK's own helper.
    assert end["bound_move"] == "card:11"
    # And what we actually submitted. Equal on an honest turn — this is the local half of
    # the substitution check.
    assert end["submitted_move"] == "card:11"
    assert end["cached_read"] == 1200
    assert end["reasoning_tokens"] == 210

    dec = next(r for r in recs if r["kind"] == "decision")
    assert dec["source"] == "model"

    # Cost must price the cached read at the cache rate, not at fresh input.
    snap = budget.snapshot()
    assert snap.calls == 1
    assert snap.reserved_usd == pytest.approx(0.0)  # reservation released
    expected = RANKED["opus-5"].cost_usd(1500, 300, cached_read=1200)
    assert snap.settled_usd == pytest.approx(expected)
    # Sanity: caching actually saved money versus paying full input price.
    assert expected < RANKED["opus-5"].cost_usd(1500, 300)


def test_request_carries_tool_choice_cache_marker_and_reasoning(tmp_path, provider):
    """The three things that decide whether a run is bindable, cacheable and comparable."""
    _srv, url = provider
    _Provider.mode = "tool_call"
    brain, _b, _l = make_brain(tmp_path, url)
    brain.decide(view())
    req = _Provider.last_request

    # Forced tool call: a prose answer is an unverified turn.
    assert req["tool_choice"]["function"]["name"] == "play_card"
    assert req["tools"][0]["function"]["name"] == "play_card"
    # Anthropic-family models get an explicit cache breakpoint on the system block.
    sys_msg = req["messages"][0]
    assert sys_msg["role"] == "system"
    assert sys_msg["content"][0]["cache_control"] == {"type": "ephemeral"}
    # Reasoning on, identically for every model.
    assert req["reasoning"] == {"effort": "medium"}
    # Game state must be in the USER turn, never the system prompt, or the fingerprint churns.
    assert "ROUND 3" in req["messages"][1]["content"]
    assert "ROUND 3" not in json.dumps(sys_msg)


def test_openai_family_gets_plain_string_system(tmp_path, provider):
    """OpenAI and DeepSeek cache prefixes automatically; a marker they ignore is noise."""
    from openai import OpenAI

    _srv, url = provider
    _Provider.mode = "tool_call"
    brain = Brain(
        model=RANKED["deepseek-v4-pro"],
        policy=GoofspielPolicy(),
        client=OpenAI(api_key="k", base_url=url),
        budget=Budget(tmp_path / "b.json", 10.0),
        ledger=Ledger(tmp_path / "l.jsonl"),
    )
    brain.decide(view())
    assert isinstance(_Provider.last_request["messages"][0]["content"], str)


def test_provider_error_falls_back_and_costs_nothing(tmp_path, provider):
    """The 429 case — the exact shape that produced phantom win rates on the real board.

    The brain must play on (an abandoned match wastes every decision already paid for),
    record the failure, mark the decision a fallback, and settle the reservation at zero so
    a rate-limited provider cannot strangle the budget.
    """
    _srv, url = provider
    _Provider.mode = "http_429"
    brain, budget, _l = make_brain(tmp_path, url)

    move = brain.decide(view())
    assert move == {"card": 2, "round": 3}  # the inert fallback: lowest card

    recs = read_ledger(tmp_path / "l.jsonl")
    end = next(r for r in recs if r["kind"] == "call_end")
    assert end["error"]
    assert end["prompt_tokens"] == 0 and end["completion_tokens"] == 0
    assert end["bound_move"] is None
    dec = next(r for r in recs if r["kind"] == "decision")
    assert dec["source"] == "fallback"

    snap = budget.snapshot()
    assert snap.settled_usd == pytest.approx(0.0)
    assert snap.reserved_usd == pytest.approx(0.0)  # no leak


def test_prose_answer_is_unverified_not_wrong(tmp_path, provider):
    """No tool call means the turn is unbound. It must never be guessed from the prose.

    "I'll play the 7" is parseable by a human and three different strings to a parser.
    Guessing would reject honest agents at scale, so the platform never does — and neither
    does this harness.
    """
    _srv, url = provider
    _Provider.mode = "prose_only"
    brain, _b, _l = make_brain(tmp_path, url)

    move = brain.decide(view())
    assert move["card"] == 2  # fallback, NOT the 7 mentioned in the prose
    recs = read_ledger(tmp_path / "l.jsonl")
    end = next(r for r in recs if r["kind"] == "call_end")
    assert end["error"] == "no_tool_call"
    assert end["bound_move"] is None
    # The call still cost money and that must be recorded — an unbound turn is not a free one.
    assert end["cost_usd"] > 0


def test_budget_ceiling_stops_calling_and_match_continues(tmp_path, provider):
    """At the ceiling the agent keeps playing, unbound. It does not abandon the match."""
    _srv, url = provider
    _Provider.mode = "tool_call"
    brain, budget, _l = make_brain(tmp_path, url, limit=0.001)  # too small for one call

    move = brain.decide(view())
    assert move == {"card": 2, "round": 3}
    recs = read_ledger(tmp_path / "l.jsonl")
    assert any(r["kind"] == "budget_exhausted" for r in recs)
    # No call was made at all, so nothing was spent.
    assert budget.snapshot().calls == 0
    assert budget.snapshot().settled_usd == pytest.approx(0.0)


def test_memory_carries_between_turns_but_not_between_matches(tmp_path, provider):
    """Per-match memory, keyed on match id and created lazily.

    The SDK warns that state built in `initialize` leaks match one's memory into match two,
    which reads as a strategy bug. This pins the correct behaviour.
    """
    _srv, url = provider
    _Provider.mode = "tool_call"
    brain, _b, _l = make_brain(tmp_path, url)

    brain.decide(view(match_id="A", round=1))
    brain.decide(view(match_id="A", round=2))
    assert "played card:11" in brain.memory.render("A")
    assert brain.memory.render("B") == ""  # a different match starts clean

    brain.on_match_end("A")
    assert brain.memory.render("A") == ""  # and finishing frees it
