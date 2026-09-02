"""Coverage for signature edge cases, ReplayGuard eviction, and multi-game
parse_view — the paths the cross-language + lifecycle tests don't exercise.
Run: pytest (from sdk/python)."""

import sys
import time
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

import pytest

from pyyol import VerificationError, compute_signature, verify_request
from pyyol.models import MAFIA, parse_view
from pyyol.signing import (
    REQUEST_ID_HEADER,
    SIGNATURE_HEADER,
    TIMESTAMP_HEADER,
    ReplayGuard,
)

SECRET = "test-secret"
BODY = b'{"game":"goofspiel"}'


def _rfc3339(epoch: float) -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime(epoch))


def _headers(ts: str, nonce: str = "n1") -> dict:
    sig = compute_signature(SECRET, ts, nonce, "POST", "/turn", BODY)
    return {
        SIGNATURE_HEADER: f"v1={sig}",
        TIMESTAMP_HEADER: ts,
        REQUEST_ID_HEADER: nonce,
    }


def test_stale_timestamp_rejected():
    now = 1_800_000_000.0
    ts = _rfc3339(now - 3600)  # 1h old; skew window is 300s
    with pytest.raises(VerificationError) as ei:
        verify_request(SECRET, _headers(ts), "POST", "/turn", BODY, now=now)
    assert ei.value.reason == "stale_timestamp"


def test_timestamp_within_skew_passes():
    now = 1_800_000_000.0
    ts = _rfc3339(now - 10)
    verify_request(SECRET, _headers(ts), "POST", "/turn", BODY, now=now)  # no raise


def test_missing_signature_rejected():
    with pytest.raises(VerificationError) as ei:
        verify_request(SECRET, {}, "POST", "/turn", BODY)
    assert ei.value.reason == "missing_signature"


def test_unsupported_version_rejected():
    now = 1_800_000_000.0
    ts = _rfc3339(now)
    h = _headers(ts)
    h[SIGNATURE_HEADER] = "v2=abc"
    with pytest.raises(VerificationError) as ei:
        verify_request(SECRET, h, "POST", "/turn", BODY, now=now)
    assert ei.value.reason == "unsupported_version"


def test_replayguard_evicts_past_ttl():
    rg = ReplayGuard(ttl=10)
    now = 1_000.0
    assert rg.check_and_store("a", now) is True
    assert rg.check_and_store("a", now) is False  # replay within TTL
    assert rg.check_and_store("a", now + 11) is True  # evicted after TTL -> fresh


def test_replayguard_evicts_oldest_at_size_cap():
    rg = ReplayGuard(ttl=1_000_000, max_size=2)  # no time eviction
    now = 1_000.0
    assert rg.check_and_store("a", now) is True
    assert rg.check_and_store("b", now) is True
    assert rg.check_and_store("c", now) is True  # evicts oldest ("a") to make room
    assert rg.check_and_store("b", now) is False  # still remembered
    assert rg.check_and_store("a", now) is True  # was evicted -> fresh again


def test_parse_view_mafia_alive_keys_coerced():
    v = parse_view(
        {
            "game": "mafia",
            "match_id": "x1",
            "your_seat": 3,
            "your_role": "mafia",
            "day": 2,
            "phase": "night",
            "alive": {"0": True, "3": True, "5": False},
            "allies": [7],
            "legal": ["kill:5", "noop"],
        }
    )
    assert v.game == MAFIA
    assert v.your_seat == 3
    assert v.your_role == "mafia"
    assert v.alive[0] is True
    assert v.alive[5] is False
    assert v.allies == [7]
    assert v.legal == ["kill:5", "noop"]
