"""Unit tests for the versioned price table + cost estimation."""

from __future__ import annotations

import pytest

from pyyol import pricing


@pytest.mark.parametrize(
    "raw,expected",
    [
        ("gpt-4o", "gpt-4o"),
        ("gpt-4o-2024-08-06", "gpt-4o"),
        ("gpt-4o-mini", "gpt-4o-mini"),  # more specific rule wins over "4o"
        ("GPT-4O-MINI", "gpt-4o-mini"),  # case-insensitive
        ("gpt-4.1-mini", "gpt-4.1-mini"),
        ("o1-mini", "o1-mini"),
        ("us.anthropic.claude-opus-4-1-20250805", "claude-opus"),
        ("claude-3-5-sonnet-20241022", "claude-sonnet"),
        ("claude-haiku-4-5", "claude-haiku"),
        ("gemini-2.5-flash", "gemini-flash"),
        ("gemini-1.5-pro", "gemini-pro"),
        ("meta-llama/Llama-3.3-70B", "llama"),
        ("deepseek-chat", "deepseek"),
    ],
)
def test_canonical_mapping(raw, expected):
    assert pricing._canonical(raw) == expected


def test_unknown_model_uses_fallback_not_zero():
    assert pricing._canonical("totally-made-up-model") is None
    assert pricing.is_known("totally-made-up-model") is False
    # Fallback is a real mid-tier rate, so unknown models are never silently free.
    cost = pricing.estimate_cost("totally-made-up-model", 1_000_000, 1_000_000)
    assert cost == pytest.approx((0.50 + 1.50))  # $ per 1M in + 1M out


def test_opus_and_sonnet_are_distinct():
    # The old heuristic priced every non-haiku Claude the same; they must differ now.
    opus = pricing.estimate_cost("claude-opus-4", 1_000_000, 1_000_000)
    sonnet = pricing.estimate_cost("claude-sonnet-4", 1_000_000, 1_000_000)
    haiku = pricing.estimate_cost("claude-haiku-4-5", 1_000_000, 1_000_000)
    assert opus > sonnet > haiku


def test_cost_math_input_output_split():
    # gpt-4o: $2.50/1M in, $10.00/1M out.
    cost = pricing.estimate_cost("gpt-4o", prompt_tokens=1000, completion_tokens=500)
    expected = (1000 * 2.50 + 500 * 10.00) / 1_000_000
    assert cost == pytest.approx(expected)


def test_cached_tokens_billed_at_cached_rate_as_subset_of_prompt():
    # 1000 prompt tokens, 400 of them cached. gpt-4o cached rate is $1.25/1M.
    cost = pricing.estimate_cost("gpt-4o", prompt_tokens=1000, completion_tokens=0, cached_tokens=400)
    expected = (600 * 2.50 + 400 * 1.25) / 1_000_000
    assert cost == pytest.approx(expected)


def test_cached_tokens_clamped_to_prompt():
    # Cached can't exceed prompt; extra is ignored rather than double-counted.
    cost = pricing.estimate_cost("gpt-4o", prompt_tokens=100, completion_tokens=0, cached_tokens=500)
    expected = (100 * 1.25) / 1_000_000
    assert cost == pytest.approx(expected)


def test_open_weight_models_are_free():
    assert pricing.estimate_cost("llama-3.3-70b", 1_000_000, 1_000_000) == 0.0


def test_zero_tokens_zero_cost():
    assert pricing.estimate_cost("gpt-4o", 0, 0) == 0.0


def test_pricing_version_is_stamped():
    assert isinstance(pricing.PRICING_VERSION, str) and pricing.PRICING_VERSION
