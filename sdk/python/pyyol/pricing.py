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

from typing import Dict, NamedTuple, Optional, Tuple

# Bump this whenever any rate below changes. Stamped onto every estimate so a cost
# is always reproducible from the exact table that produced it.
PRICING_VERSION = "2026-07-31"


class Rate(NamedTuple):
    """USD per 1,000,000 tokens."""

    input: float
    output: float
    # Cost of a cached (prompt-cache read) input token; defaults to input when unset.
    cached_input: Optional[float] = None


# Canonical model id -> Rate. Keep names lowercase and provider-agnostic; raw model
# strings are normalized onto these keys by `_canonical()`.
_TABLE: Dict[str, Rate] = {
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
_RULES: Tuple[Tuple[str, str], ...] = (
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
_PROVIDER_RULES: Dict[str, list] = {
    "groq": [
        ("llama-3.1-8b", "groq-llama-8b"),
        ("llama-3.1-70b", "groq-llama-70b"),
        ("llama-3.3-70b", "groq-llama-70b"),
        ("llama-4", "groq-llama-70b"),
    ],
}


def _canonical(model: str, provider: str = "") -> Optional[str]:
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


def estimate_cost(
    model: str,
    prompt_tokens: int = 0,
    completion_tokens: int = 0,
    *,
    cached_tokens: int = 0,
    reasoning_tokens: int = 0,
    provider: str = "",
) -> float:
    """USD cost estimate for one model call.

    `cached_tokens` are billed at the cached-input rate and are treated as a SUBSET of
    `prompt_tokens` (so only `prompt_tokens - cached_tokens` are billed at full input
    rate). `reasoning_tokens` are billed at the output rate (they are output tokens the
    provider bills for) and are treated as a subset of `completion_tokens`.
    """
    rate = rate_for(model, provider)
    cached = max(0, min(cached_tokens, prompt_tokens))
    full_input = max(0, prompt_tokens - cached)
    cached_rate = rate.cached_input if rate.cached_input is not None else rate.input
    cost = (
        full_input * rate.input + cached * cached_rate + max(0, completion_tokens) * rate.output
    ) / 1_000_000.0
    return round(cost, 8)
