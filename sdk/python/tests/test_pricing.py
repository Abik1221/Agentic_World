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
    assert cost == pytest.approx(0.50 + 1.50)  # $ per 1M in + 1M out


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
    cost = pricing.estimate_cost(
        "gpt-4o", prompt_tokens=1000, completion_tokens=0, cached_tokens=400
    )
    expected = (600 * 2.50 + 400 * 1.25) / 1_000_000
    assert cost == pytest.approx(expected)


def test_cached_tokens_clamped_to_prompt():
    # Cached can't exceed prompt; extra is ignored rather than double-counted.
    cost = pricing.estimate_cost(
        "gpt-4o", prompt_tokens=100, completion_tokens=0, cached_tokens=500
    )
    expected = (100 * 1.25) / 1_000_000
    assert cost == pytest.approx(expected)


def test_open_weight_models_are_free():
    assert pricing.estimate_cost("llama-3.3-70b", 1_000_000, 1_000_000) == 0.0


def test_zero_tokens_zero_cost():
    assert pricing.estimate_cost("gpt-4o", 0, 0) == 0.0


def test_pricing_version_is_stamped():
    assert isinstance(pricing.PRICING_VERSION, str) and pricing.PRICING_VERSION


def test_hosted_open_weight_is_priced_but_self_hosted_is_not():
    """ "Open weight" does not mean "free" — it means you MIGHT be running it yourself.

    Groq bills per token like anyone else, so a Groq-backed agent was reporting $0 on
    a platform that advertises verified cost tracking. But the model id is identical
    whether Groq serves it or you do, so pricing by name alone would have billed
    self-hosted users for compute they never bought. The provider is what separates
    the two cases.
    """
    from pyyol import estimate_cost

    model = "llama-3.3-70b-versatile"

    assert estimate_cost(model, 1000, 200) == 0.0, "self-hosted open weight must stay free"
    hosted = estimate_cost(model, 1000, 200, provider="groq")
    assert hosted > 0, "a Groq-served model must have a non-zero cost"

    # And the small model must be cheaper than the large one, or the table is wrong.
    small = estimate_cost("llama-3.1-8b-instant", 1000, 200, provider="groq")
    assert 0 < small < hosted


def test_an_unknown_provider_does_not_invent_a_hosted_rate():
    from pyyol import estimate_cost

    assert estimate_cost("llama-3.3-70b-versatile", 1000, 200, provider="mystery") == 0.0


# --- Prompt-cache accounting ---------------------------------------------------
#
# The bug these pin: cache WRITES were never captured, and cache tokens were assumed to
# be a subset of the reported input count on every provider. Both errors pushed cost
# DOWN, and hardest for the agents that cache most aggressively — the opposite of what a
# cost-efficiency ranking needs.


def test_cache_write_is_billed_above_input_on_anthropic():
    """Anthropic surcharges a cache write to 1.25x input. Billing it at the READ rate
    (0.1x) or at zero understates the expensive half of caching by up to 12.5x."""
    r = pricing.rate_for("claude-opus-4")
    assert pricing.cache_write_rate("claude-opus-4") == pytest.approx(r.input * 1.25)
    assert pricing.cache_write_rate("claude-opus-4") > r.input
    assert r.cached_input is not None and pricing.cache_write_rate("claude-opus-4") > r.cached_input


def test_cache_write_is_free_on_openai():
    """OpenAI's prompt caching is automatic and writes are not billed. Applying
    Anthropic's 1.25x surcharge here would invent a charge that does not exist."""
    assert pricing.cache_write_rate("gpt-4o") == 0.0


def test_unknown_model_cache_write_is_not_silently_free():
    """An unmapped model must not get free caching — that is the flattering direction,
    and it would let an unrecognised model look cheaper than any known one."""
    assert pricing.cache_write_rate("some-unreleased-model-2027") > 0.0


def test_reads_and_writes_partition_the_input_and_never_double_bill():
    """Reads come out of the input first, then writes from the remainder, so no token is
    billed twice and the three portions sum to prompt_tokens."""
    rate = pricing.rate_for("claude-opus-4")
    prompt, read, write, completion = 2520, 1500, 600, 90
    got = pricing.estimate_cost(
        "claude-opus-4", prompt, completion,
        cached_tokens=read, cached_write_tokens=write,
    )
    full = prompt - read - write  # 420 at full input rate
    want = (
        full * rate.input
        + read * rate.cached_input
        + write * pricing.cache_write_rate("claude-opus-4")
        + completion * rate.output
    ) / 1_000_000.0
    assert got == pytest.approx(want)


def test_old_cache_accounting_understated_a_real_call_by_over_3x():
    """The regression itself, with the token counts observed from a live gateway call:
    Anthropic reported input 420, output 90, cache read 1500, cache write 600.

    The OLD path did two things wrong at once. It never read
    ``cache_creation_input_tokens``, so the 600 written tokens did not exist. And it
    treated cache reads as a subset of ``input_tokens``, so ``min(1500, 420)`` billed 420
    tokens at the cheap read rate and threw the other 1080 reads away entirely.

    Both errors point the same way, which is why this matters: the reported cost was 28%
    of the real one, and the understatement scales with how hard an agent caches.
    """
    # What the old code computed: prompt never grew, reads clamped down to it.
    old = pricing.estimate_cost("claude-opus-4", 420, 90, cached_tokens=420)
    # What the provider actually bills, on the normalized convention.
    new = pricing.estimate_cost(
        "claude-opus-4", 2520, 90, cached_tokens=1500, cached_write_tokens=600
    )
    assert old == pytest.approx((420 * 1.50 + 90 * 75.00) / 1_000_000.0)
    assert new == pytest.approx(
        (420 * 15.00 + 1500 * 1.50 + 600 * 18.75 + 90 * 75.00) / 1_000_000.0
    )
    assert new > 3.5 * old


def test_pricing_a_write_at_the_read_rate_is_not_close_enough():
    """The plausible half-fix — capture writes but bill them like reads — is off by the
    full 12.5x spread between Anthropic's 1.25x write and 0.1x read."""
    correct = pricing.estimate_cost(
        "claude-opus-4", 2520, 90, cached_tokens=1500, cached_write_tokens=600
    )
    as_if_read = pricing.estimate_cost(
        "claude-opus-4", 2520, 90, cached_tokens=2100
    )  # 1500 reads + 600 writes all at the read rate
    assert correct - as_if_read == pytest.approx(600 * (18.75 - 1.50) / 1_000_000.0)


def test_self_hosted_caching_is_still_free():
    """A multiplier on a $0 input rate must stay $0 — a locally served model has no
    bill of any kind, cache or otherwise."""
    assert pricing.cache_write_rate("llama-3.3-70b", provider="ollama") == 0.0
    assert pricing.estimate_cost(
        "llama-3.3-70b", 2520, 90, cached_tokens=1500,
        cached_write_tokens=600, provider="ollama",
    ) == 0.0
