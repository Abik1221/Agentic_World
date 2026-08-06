"""Scaffold fingerprints: shared fixtures, asserted identically by the Python and JS SDKs.

A fingerprint is a hash, so any disagreement between the two SDKs is total: the same agent
switching SDKs would look like a brand-new scaffold, its history would split into two
epochs, and it would silently drop out of every paired model comparison. Nothing about that
failure looks like a bug — the agent just quietly stops appearing on the model board.

So the fixtures pin exact values, and the relational cases (same_fingerprint_as /
differs_from) pin the PROPERTIES that make pairing sound, which is what actually has to
hold even if a future version legitimately rehashes everything.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Dict, List

import pytest

from pyyol import scaffold

_FIXTURES = Path(__file__).resolve().parents[2] / "conformance" / "scaffold.json"


def _load() -> Dict[str, Any]:
    assert _FIXTURES.is_file(), f"scaffold fixtures not found at {_FIXTURES}"
    return json.loads(_FIXTURES.read_text())


_DOC = _load()
_CASES: List[Dict[str, Any]] = _DOC["cases"]
_BY_NAME = {c["name"]: c for c in _CASES}


def _fp(case: Dict[str, Any]) -> str:
    return scaffold.from_request(case["request"], endpoint=case["endpoint"])


def _canonical(case: Dict[str, Any]) -> str:
    return scaffold.canonical(scaffold.extract(case["request"], endpoint=case["endpoint"]))


def test_fixture_file_is_not_empty():
    """A suite that silently finds zero cases would report success while testing nothing."""
    assert len(_CASES) >= 12


def test_scaffold_version_matches_the_fixtures():
    assert scaffold.SCAFFOLD_VERSION == _DOC["scaffold_version"]


@pytest.mark.parametrize("case", _CASES, ids=[c["name"] for c in _CASES])
def test_expected_values(case: Dict[str, Any]):
    exp = case.get("expect")
    if not exp:
        pytest.skip("relational case, checked below")
    if "fingerprint" in exp:
        assert _fp(case) == exp["fingerprint"], case["why"]
    if "canonical" in exp:
        assert _canonical(case) == exp["canonical"], case["why"]
    if "canonical_contains" in exp:
        assert exp["canonical_contains"] in _canonical(case), (
            f"{case['why']}\ncanonical was: {_canonical(case)!r}"
        )


@pytest.mark.parametrize(
    "case",
    [c for c in _CASES if "same_fingerprint_as" in c],
    ids=[c["name"] for c in _CASES if "same_fingerprint_as" in c],
)
def test_cases_that_must_share_a_fingerprint(case: Dict[str, Any]):
    other = _BY_NAME[case["same_fingerprint_as"]]
    mine, theirs = _fp(case), _fp(other)
    assert mine, "an empty fingerprint cannot satisfy a sameness claim"
    assert mine == theirs, case["why"]


@pytest.mark.parametrize(
    "case",
    [c for c in _CASES if "differs_from" in c],
    ids=[c["name"] for c in _CASES if "differs_from" in c],
)
def test_cases_that_must_differ(case: Dict[str, Any]):
    other = _BY_NAME[case["differs_from"]]
    assert _fp(case) != _fp(other), case["why"]


def test_the_model_is_never_part_of_the_fingerprint():
    """The single property the whole design rests on, asserted directly rather than only
    through fixtures: swapping the model must not move the fingerprint, or no model change
    is ever pairable and the model board cannot exist."""
    base = {
        "system": "You play Goofspiel.",
        "messages": [{"role": "user", "content": "bid"}],
        "temperature": 0.3,
    }
    a = scaffold.from_request({**base, "model": "claude-opus-4"}, endpoint="x")
    b = scaffold.from_request({**base, "model": "gpt-5.2"}, endpoint="x")
    c = scaffold.from_request({**base, "model": "llama-3.3-70b"}, endpoint="x")
    assert a and a == b == c


def test_credentials_never_enter_the_hash():
    """An api_key must not be hashed even indirectly. Beyond the obvious, a key rotation
    would otherwise split an agent's history for a reason that has nothing to do with its
    harness."""
    base = {"system": "S", "messages": [{"role": "user", "content": "u"}]}
    plain = scaffold.from_request(base, endpoint="x")
    with_key = scaffold.from_request({**base, "api_key": "sk-secret", "base_url": "http://x"}, endpoint="x")
    assert plain == with_key
    assert "sk-secret" not in scaffold.canonical(scaffold.extract({**base, "api_key": "sk-secret"}))


def test_tracker_reports_instability_rather_than_hiding_it():
    """Game state in the system prompt makes the fingerprint churn. That agent cannot be
    paired, and the tracker has to say so instead of keeping the first value seen."""
    t = scaffold.ScaffoldTracker()
    t.observe("sc_aaaaaaaaaaaaaaaa")
    t.observe("sc_aaaaaaaaaaaaaaaa")
    assert t.first == "sc_aaaaaaaaaaaaaaaa" and not t.unstable

    t.observe("sc_bbbbbbbbbbbbbbbb")
    assert t.unstable
    # The first value is still reported: it is the most likely intended scaffold, and
    # discarding it would lose information that the instability flag already qualifies.
    assert t.first == "sc_aaaaaaaaaaaaaaaa"


def test_unknown_fingerprints_do_not_count_as_a_change():
    """A failure to fingerprint is not evidence that the harness changed. Counting it as
    one would mark honest agents unstable and quietly shrink the comparable population."""
    t = scaffold.ScaffoldTracker()
    t.observe("sc_aaaaaaaaaaaaaaaa")
    t.observe("")
    assert not t.unstable and t.first == "sc_aaaaaaaaaaaaaaaa"


def test_pairing_eligibility_refuses_unknowns():
    """Treating 'we could not tell' as 'the same as the others' is how a confounded
    comparison gets published as a clean one."""
    assert scaffold.eligible_for_pairing(["sc_a", "sc_a"])
    assert not scaffold.eligible_for_pairing(["sc_a", "sc_b"])
    assert not scaffold.eligible_for_pairing(["sc_a", ""])
    assert not scaffold.eligible_for_pairing([None])
    assert not scaffold.eligible_for_pairing([])
