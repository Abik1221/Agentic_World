"""The proof token must travel: platform turn view -> accumulator -> LLM call header.

If any link breaks, an honest agent looks 0% LLM-backed and ranked enforcement would
void real matches. These pin each hop.
"""

from pyyol import instrument as instr
from pyyol.telemetry import turn_usage


def test_proof_from_the_turn_view_reaches_the_gateway_headers(monkeypatch):
    monkeypatch.setitem(instr._gateway, "key", "sk_arena_test")

    with turn_usage(match_id="m_1", turn=7, turn_proof="proof-abc"):
        h = instr.gateway_headers()

    assert h["X-Pyyol-Match"] == "m_1"
    assert h["X-Pyyol-Turn"] == "7"
    assert h["X-Pyyol-Proof"] == "proof-abc", "the proof never reached the LLM call"


def test_no_proof_header_when_the_platform_sent_none(monkeypatch):
    """An older platform ships no turn_proof. The call is then unproven — which is
    correct — but must not send an empty header that looks like a failed proof."""
    monkeypatch.setitem(instr._gateway, "key", "sk_arena_test")

    with turn_usage(match_id="m_1", turn=1):
        h = instr.gateway_headers()

    assert "X-Pyyol-Proof" not in h
    assert h["X-Pyyol-Match"] == "m_1", "identity headers must still be sent"


def test_each_turn_carries_its_own_proof(monkeypatch):
    """A proof is bound to one decision, so it must not leak across turns — that is
    exactly the reuse the binding exists to prevent."""
    monkeypatch.setitem(instr._gateway, "key", "sk_arena_test")

    with turn_usage(match_id="m_1", turn=1, turn_proof="proof-r1"):
        first = instr.gateway_headers()["X-Pyyol-Proof"]
    with turn_usage(match_id="m_1", turn=2, turn_proof="proof-r2"):
        second = instr.gateway_headers()["X-Pyyol-Proof"]

    assert first == "proof-r1"
    assert second == "proof-r2"
    assert first != second


def test_routing_off_sends_no_identity_headers(monkeypatch):
    """No gateway key => not routing => the developer's provider call must not carry
    Pyyol identity headers at all."""
    monkeypatch.setitem(instr._gateway, "key", "")
    with turn_usage(match_id="m_1", turn=1, turn_proof="proof-abc"):
        assert instr.gateway_headers() == {}
