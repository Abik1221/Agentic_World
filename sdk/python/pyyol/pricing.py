"""Versioned LLM price table + cost estimation.

The audit flagged the old cost path as a *stale heuristic with no version and no
Opus/Sonnet split*. This module is the single, dated source of truth the SDK uses to
turn token counts into a USD cost estimate. It is deliberately data-only (stdlib,
no deps) so both the auto-instrumentation and manual `log_model_call` share one table.

Prices are **public list prices in USD per 1,000,000 tokens** as of
``PRICING_VERSION``. They are estimates for the *sandbox / unverified* tier; the
authoritative figure for the ranked/verified tier will come from the Pyyol Gateway
(the real provider bill). When a provider changes prices, bump ``PRICING_VERSION``
and update the table below — never edit silently, so a cost can always be traced to
the table that produced it.
"""

from __future__ import annotations

from typing import NamedTuple

# Bump this whenever any rate below changes. Stamped onto every estimate so a cost
# is always reproducible from the exact table that produced it.
PRICING_VERSION = "2026-08-06"


class Rate(NamedTuple):
    """USD per 1,000,000 tokens."""

    input: float
    output: float
    # Cost of a cached (prompt-cache read) input token; defaults to input when unset.
    cached_input: float | None = None


# Cache-WRITE multipliers, applied to a model's input rate.
#
# Writing a prompt into a provider's cache is a separate, separately-billed event from
# reading it back, and the two go in OPPOSITE directions: Anthropic surcharges a write
# at 1.25x input and discounts a read to 0.1x, while OpenAI does not bill writes at all.
# Recording only reads therefore does not merely lose a number — it prices the
# expensive half of caching at zero, and it does so for the agents that cache hardest.
#
# Expressed as a multiplier rather than a per-model rate because that is how providers
# actually publish it: one ratio for the whole model family. A multiplier also cannot
# drift out of step with a model's input rate the way a duplicated absolute number can.
#
# Keyed by canonical model family prefix, so the multiplier is derived from the same
# lookup that produced the rate and needs no extra provider argument at the call site.
_CACHE_WRITE_MULTIPLIER: tuple[tuple[str, float], ...] = (
    ("claude-", 1.25),  # Anthropic bills a cache write at 1.25x input
    ("gpt-", 0.0),  # OpenAI prompt caching is automatic and writes are not billed
    ("o1", 0.0),
    ("o3", 0.0),
    ("o4", 0.0),
    ("gemini-", 0.0),  # implicit context caching is free (explicit caching bills storage)
)

# Multiplier for a model family with no published cache-write behaviour. 1.0 — a write
# costs what an ordinary input token costs. Neither 0.0 (which would silently make an
# unknown model's caching free, the flattering direction) nor 1.25 (which would invent
# a surcharge no provider announced).
_DEFAULT_CACHE_WRITE_MULTIPLIER = 1.0


# Canonical model id -> Rate. Keep names lowercase and provider-agnostic; raw model
# strings are normalized onto these keys by `_canonical()`.
_TABLE: dict[str, Rate] = {
    # --- OpenAI ---
    "gpt-4o": Rate(2.50, 10.00, 1.25),
    "gpt-4o-mini": Rate(0.15, 0.60, 0.075),
    "gpt-4.1": Rate(2.00, 8.00, 0.50),
    "gpt-4.1-mini": Rate(0.40, 1.60, 0.10),
    "gpt-4.1-nano": Rate(0.10, 0.40, 0.025),
    "o1": Rate(15.00, 60.00, 7.50),
    "o1-mini": Rate(1.10, 4.40, 0.55),
    "o3": Rate(2.00, 8.00, 0.50),
    "o3-mini": Rate(1.10, 4.40, 0.55),
    "o4-mini": Rate(1.10, 4.40, 0.275),
    "gpt-3.5-turbo": Rate(0.50, 1.50),
    # --- Anthropic (Claude) --- distinct Opus / Sonnet / Haiku, the split the old
    # heuristic lacked.
    "claude-opus": Rate(15.00, 75.00, 1.50),
    "claude-sonnet": Rate(3.00, 15.00, 0.30),
    "claude-haiku": Rate(0.80, 4.00, 0.08),
    # --- Google (Gemini) ---
    "gemini-flash": Rate(0.15, 0.60, 0.0375),
    "gemini-pro": Rate(1.25, 5.00, 0.3125),
    # --- Open-weight served by a HOSTED provider (there IS a per-token bill) ---
    #
    # "open weight" does not mean "free". Groq bills per token like anyone else, and
    # recording $0 for it meant a Groq-backed agent reported no cost at all — on a
    # platform that advertises verified LLM cost tracking. These are Groq's published
    # rates per 1M tokens; they are ESTIMATES for the unverified tier, and the gateway
    # remains authoritative for real spend.
    "groq-llama-8b": Rate(0.05, 0.08),
    "groq-llama-70b": Rate(0.59, 0.79),
    # --- Open-weight / self-hosted (no per-token bill; recorded as $0) ---
    "llama": Rate(0.0, 0.0),
    "mistral": Rate(0.0, 0.0),
    "qwen": Rate(0.0, 0.0),
    "deepseek": Rate(0.27, 1.10),
}

# Last-resort rate when a model can't be mapped (mirrors a mid-tier model so an
# unknown model is never silently $0 unless it's explicitly open-weight).
_FALLBACK = Rate(0.50, 1.50)

# Ordered (substring, canonical) rules. First match wins, so put more specific
# substrings before more general ones (e.g. "4o-mini" before "4o").
_RULES: tuple[tuple[str, str], ...] = (
    ("gpt-4o-mini", "gpt-4o-mini"),
    ("gpt-4o", "gpt-4o"),
    ("4o-mini", "gpt-4o-mini"),
    ("4o", "gpt-4o"),
    ("gpt-4.1-nano", "gpt-4.1-nano"),
    ("gpt-4.1-mini", "gpt-4.1-mini"),
    ("gpt-4.1", "gpt-4.1"),
    ("4.1-nano", "gpt-4.1-nano"),
    ("4.1-mini", "gpt-4.1-mini"),
    ("4.1", "gpt-4.1"),
    ("o1-mini", "o1-mini"),
    ("o1", "o1"),
    ("o3-mini", "o3-mini"),
    ("o3", "o3"),
    ("o4-mini", "o4-mini"),
    ("gpt-3.5", "gpt-3.5-turbo"),
    ("3.5-turbo", "gpt-3.5-turbo"),
    ("opus", "claude-opus"),
    ("sonnet", "claude-sonnet"),
    ("haiku", "claude-haiku"),
    ("gemini-1.5-flash", "gemini-flash"),
    ("gemini-2.0-flash", "gemini-flash"),
    ("gemini-2.5-flash", "gemini-flash"),
    ("flash", "gemini-flash"),
    ("gemini-1.5-pro", "gemini-pro"),
    ("gemini-2.5-pro", "gemini-pro"),
    ("gemini", "gemini-pro"),
    ("llama", "llama"),
    ("mistral", "mistral"),
    ("mixtral", "mistral"),
    ("qwen", "qwen"),
    ("deepseek", "deepseek"),
)


# Provider-scoped rates. An open-weight model is $0 when you run it yourself and very
# much not $0 when a hosted provider serves it — and the MODEL ID cannot tell you
# which, since "llama-3.3-70b" is the same string either way. Pricing it by name alone
# would have billed self-hosted users for compute they never bought; the test suite
# caught exactly that. So the provider scopes the lookup, and only an explicitly
# provider-attributed call gets a hosted rate.
_PROVIDER_RULES: dict[str, list] = {
    "groq": [
        ("llama-3.1-8b", "groq-llama-8b"),
        ("llama-3.1-70b", "groq-llama-70b"),
        ("llama-3.3-70b", "groq-llama-70b"),
        ("llama-4", "groq-llama-70b"),
    ],
}


def _canonical(model: str, provider: str = "") -> str | None:
    """Map a raw model string ("us.anthropic.claude-opus-4-1-20250805", "gpt-4o-2024-08-06")
    to a canonical table key, or None if unknown."""
    m = (model or "").strip().lower()
    if not m:
        return None
    # Provider-specific rules win: they are the only ones that know a hosted bill
    # exists for a model that would otherwise be free.
    for needle, key in _PROVIDER_RULES.get((provider or "").strip().lower(), []):
        if needle in m:
            return key
    if m in _TABLE:
        return m
    for needle, key in _RULES:
        if needle in m:
            return key
    return None


# Rate for a model the developer serves themselves. Not a guess and not a fallback —
# there is no per-token bill, so any non-zero number here would be fiction.
_FREE = Rate(0.0, 0.0, 0.0)


def rate_for(model: str, provider: str = "") -> Rate:
    """The Rate used for `model` (falls back to a mid-tier rate for unknown models).

    `provider` scopes the lookup so a hosted open-weight model is priced while the
    same model self-hosted stays at $0.
    """
    # Self-hosted first, ahead of every name-based rule. The model id cannot tell you
    # who served it — "llama-3.3-70b" is the same string on Groq's bill and on your own
    # GPU — so without this an Ollama user is charged Groq's rates for electricity they
    # already paid for, and the unknown-model fallback would invent a bill for a
    # locally-served model nobody has a rule for.
    from . import providers as _providers

    if _providers.is_self_hosted((provider or "").strip().lower()):
        return _FREE
    key = _canonical(model, provider)
    return _TABLE[key] if key is not None else _FALLBACK


def is_known(model: str, provider: str = "") -> bool:
    """True if the model maps to an explicit table entry (not the fallback)."""
    return _canonical(model, provider) is not None


def cache_write_rate(model: str, provider: str = "") -> float:
    """USD per 1,000,000 tokens for writing a prompt into the provider's cache."""
    rate = rate_for(model, provider)
    # A self-hosted model has no bill of any kind, and multiplying a $0 input rate keeps
    # that true without a special case.
    key = _canonical(model, provider) or ""
    for prefix, mult in _CACHE_WRITE_MULTIPLIER:
        if key.startswith(prefix):
            return rate.input * mult
    return rate.input * _DEFAULT_CACHE_WRITE_MULTIPLIER


def estimate_cost(
    model: str,
    prompt_tokens: int = 0,
    completion_tokens: int = 0,
    *,
    cached_tokens: int = 0,
    cached_write_tokens: int = 0,
    reasoning_tokens: int = 0,
    provider: str = "",
) -> float:
    """USD cost estimate for one model call.

    `prompt_tokens` is the TOTAL billable input, and `cached_tokens` (cache reads) and
    `cached_write_tokens` (cache creations) are SUBSETS of it — so the three partition
    the input into full-rate, read-rate and write-rate portions. Callers are responsible
    for normalizing onto that convention, which `_instrument.extract_usage` does: some
    providers report cache tokens inside their input count and some report them
    alongside it, and pricing must not have to know which.

    `reasoning_tokens` are billed at the output rate (they are output tokens the
    provider bills for) and are treated as a subset of `completion_tokens`.
    """
    rate = rate_for(model, provider)
    prompt = max(0, prompt_tokens)
    # Reads are taken out first, then writes from what remains, so the two subsets can
    # never overlap and bill the same token twice.
    read = max(0, min(cached_tokens, prompt))
    write = max(0, min(cached_write_tokens, prompt - read))
    full_input = prompt - read - write
    read_rate = rate.cached_input if rate.cached_input is not None else rate.input
    cost = (
        full_input * rate.input
        + read * read_rate
        + write * cache_write_rate(model, provider)
        + max(0, completion_tokens) * rate.output
    ) / 1_000_000.0
    return round(cost, 8)
