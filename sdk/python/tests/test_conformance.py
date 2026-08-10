"""Cross-language conformance: token normalization + cost, driven by shared fixtures.

The expectations live in ``sdk/conformance/usage_pricing.json`` and are read by this
suite, the JS SDK's ``conformance.test.ts`` and the Go backend's ``conformance_test.go``.

The point is drift. Three implementations of this logic exist, and nothing in any of them
forces them to agree; each language's own tests would keep passing while the three quietly
diverged. Since the boards rank on cost efficiency, a divergence would land as a silent
bias in a public ranking rather than a visible bug — an agent scored cheaper or dearer for
choosing a different SDK. Sharing one set of expectations makes that a failing test.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from pyyol import pricing
from pyyol._instrument import extract_usage

_FIXTURES = Path(__file__).resolve().parents[2] / "conformance" / "usage_pricing.json"


def _load() -> dict[str, Any]:
    # A missing fixture file must fail loudly rather than silently skip: an empty
    # conformance suite that reports "passed" is the exact failure this guards against.
    assert _FIXTURES.is_file(), f"conformance fixtures not found at {_FIXTURES}"
    return json.loads(_FIXTURES.read_text())


_DOC = _load()
_CASES: list[dict[str, Any]] = _DOC["cases"]


def test_fixture_file_is_not_empty():
    """A suite that silently finds zero cases would report success while testing nothing."""
    assert len(_CASES) >= 7


def test_pricing_version_matches_the_fixtures():
    """The fixture costs were computed from one dated table. If the SDK's table moves and
    the fixtures do not, every cost below is being checked against stale rates."""
    assert pricing.PRICING_VERSION == _DOC["pricing_version"]


@pytest.mark.parametrize("case", _CASES, ids=[c["name"] for c in _CASES])
def test_usage_extraction_matches_shared_fixture(case: dict[str, Any]):
    info = extract_usage(case["response"])
    assert info is not None, f"{case['name']}: extractor returned nothing — {case['why']}"
    want = case["expect"]
    got = {
        "prompt_tokens": info["prompt_tokens"],
        "completion_tokens": info["completion_tokens"],
        "cached_read_tokens": info["cached_tokens"],
        "cached_write_tokens": info["cached_write_tokens"],
        "reasoning_tokens": info["reasoning_tokens"],
    }
    expected = {k: want[k] for k in got}
    assert got == expected, f"{case['name']}: {case['why']}"


@pytest.mark.parametrize("case", _CASES, ids=[c["name"] for c in _CASES])
def test_the_subset_invariant_holds(case: dict[str, Any]):
    """read + write <= prompt, in every case. Violating it means pricing silently clamps
    the excess to zero, which reads as a cheaper call rather than as an error."""
    info = extract_usage(case["response"])
    assert info is not None
    assert info["cached_tokens"] + info["cached_write_tokens"] <= info["prompt_tokens"], (
        f"{case['name']}: cache tokens exceed the prompt total, so pricing would discard "
        f"the excess as if it had never been billed"
    )


@pytest.mark.parametrize("case", _CASES, ids=[c["name"] for c in _CASES])
def test_cost_matches_shared_fixture(case: dict[str, Any]):
    info = extract_usage(case["response"])
    assert info is not None
    got = pricing.estimate_cost(
        case["model"],
        info["prompt_tokens"],
        info["completion_tokens"],
        cached_tokens=info["cached_tokens"],
        cached_write_tokens=info["cached_write_tokens"],
        reasoning_tokens=info["reasoning_tokens"],
        provider=case["provider"],
    )
    want = case["expect"]["cost_usd"]
    assert got == pytest.approx(want, abs=1e-9), (
        f"{case['name']}: cost {got} != {want} ({case['expect']['cost_breakdown']})"
    )
