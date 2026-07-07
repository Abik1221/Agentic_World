"""Tests for the pyyol Python SDK. Run: pytest (from sdk/python)."""
import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import pytest

from pyyol import Agent, VerificationError, compute_signature, simulate_goofspiel, verify_request
from pyyol.signing import ReplayGuard


# A fixed vector shared with the Go platform and the JS SDK — all three MUST agree.
XLANG_VECTOR = (
    ("shared-secret", "2026-07-06T12:00:00Z", "req_abc", "POST", "/turn", b'{"x":1}'),
    "d0da90bddbc2cef9aec939e18c6b1c8e7d2f706cd21da8b1b3d7eb34a6275976",
)


def test_signature_cross_language_vector():
    (secret, ts, nonce, method, path, body), expected = XLANG_VECTOR
    assert compute_signature(secret, ts, nonce, method, path, body) == expected


def _agent():
    a = Agent(secret="test-secret", supported_games=["goofspiel"], name="lowball")
    a.on_turn("goofspiel")(lambda v: {"round": v.round, "card": min(v.legal_actions)})
    return a


def test_simulate_goofspiel_runs_full_lifecycle():
    a = _agent()
    seen = {"init": 0, "event": 0, "end": 0}
    a.on_initialize(lambda r: seen.__setitem__("init", seen["init"] + 1))
    a.on_event(lambda n: seen.__setitem__("event", seen["event"] + 1))
    a.on_game_end(lambda r: seen.__setitem__("end", seen["end"] + 1))

    res = simulate_goofspiel(a, hand_size=13, seed=3)
    assert res["rounds"] == 13
    assert res["scores"]["agent"] + res["scores"]["baseline"] == sum(range(1, 14))
    assert seen == {"init": 1, "event": 13, "end": 1}


def test_health_is_unsigned():
    status, body = _agent().handle("GET", "/health", {}, b"")
    assert status == 200 and body["status"] == "healthy"


def test_tampered_signature_rejected():
    a = _agent()
    status, body = a.handle(
        "POST", "/turn",
        {"X-Arena-Signature": "v1=deadbeef", "X-Arena-Timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "X-Arena-Request-Id": "x"},
        b'{"game":"goofspiel"}',
    )
    assert status == 401 and body["reason"] == "bad_signature"


def test_replay_rejected():
    ts = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    body = b'{"game":"goofspiel"}'
    sig = compute_signature("test-secret", ts, "n1", "POST", "/turn", body)
    hdr = {"X-Arena-Signature": f"v1={sig}", "X-Arena-Timestamp": ts, "X-Arena-Request-Id": "n1"}
    rg = ReplayGuard()
    verify_request("test-secret", hdr, "POST", "/turn", body, replay_guard=rg)
    with pytest.raises(VerificationError) as ei:
        verify_request("test-secret", hdr, "POST", "/turn", body, replay_guard=rg)
    assert ei.value.reason == "replayed_nonce"


def test_illegal_move_fails_loudly():
    a = Agent(secret="", supported_games=["goofspiel"])
    a.on_turn("goofspiel")(lambda v: {"card": 999})  # never in hand
    from pyyol import SimulationError

    with pytest.raises(SimulationError):
        simulate_goofspiel(a, hand_size=5)
