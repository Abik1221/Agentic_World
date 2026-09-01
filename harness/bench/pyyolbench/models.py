"""The model registry: which models the benchmark ranks, and what they cost.

# Why prices live here and not only on the invoice

The benchmark's second claim — after "which model plays best" — is "what did that cost".
A cost number computed from OpenRouter's response is authoritative but arrives too late to
STOP a run, so the budget guard needs a local price table to project spend before a call is
made. These figures are therefore a forecasting aid, never the published number: the
published cost comes from the gateway's own accounting of what the provider reported.

Prices are USD per 1e6 tokens, read from OpenRouter's /api/v1/models on 2026-08-16. They
drift. `scripts/refresh_prices.py` re-reads them and rewrites this table; a run whose
projection disagrees with the invoice by more than a few percent should refresh first.

# Why the cache columns matter more than the headline price

Every model here bills cached prompt reads at roughly a tenth of fresh input. The benchmark
sends one large, byte-identical system prompt per game on every single decision, so after
the first call of a match that prefix should be a cache READ. On a 13-round Goofspiel match
that is the difference between paying for the rules 13 times and paying for them once.
`cache_write` is what the first call costs to establish it — on Anthropic and OpenAI that is
1.25x fresh input, which is why caching only pays off across a match rather than within a
call, and why a run of 1-round matches would be strictly worse off.
"""

from __future__ import annotations

from dataclasses import dataclass


@dataclass(frozen=True)
class Model:
    """One ranked model, priced per 1e6 tokens."""

    #: The OpenRouter model id. This is the string the gateway forwards and the provider
    #: echoes back, so it is also the string the model board attributes a seat to.
    slug: str
    #: Short label for filenames, container names and report columns.
    key: str
    #: Vendor family. Decides the prompt-cache DIALECT only — never the prompt text, and
    #: never a sampling parameter, both of which must stay identical for paired comparison.
    family: str
    price_in: float
    price_out: float
    price_cache_read: float
    price_cache_write: float

    def cost_usd(
        self,
        prompt_tokens: int,
        completion_tokens: int,
        cached_read: int = 0,
        cached_write: int = 0,
    ) -> float:
        """Dollar cost of one call.

        `cached_read` is a SUBSET of `prompt_tokens` — OpenRouter reports it the way the
        underlying provider does, and every provider in this table counts a cached read
        inside the prompt total rather than beside it. Subtracting is therefore correct and
        double-counting is the bug to avoid; the same distinction the gateway's usage
        normaliser draws between prompt-family and input-family keys.
        """
        fresh = max(0, prompt_tokens - cached_read - cached_write)
        return (
            fresh * self.price_in
            + cached_read * self.price_cache_read
            + cached_write * self.price_cache_write
            + completion_tokens * self.price_out
        ) / 1e6


# The five the benchmark ranks. Every one advertises `tools`, `reasoning` and
# `structured_outputs` on OpenRouter — verified against /api/v1/models before selection,
# because a model that cannot emit a structured tool call cannot be completion-bound, and an
# unbound seat is excluded from the board after you have already paid for its matches.
RANKED: dict[str, Model] = {
    "opus-5": Model("anthropic/claude-opus-5", "opus-5", "anthropic", 5.0, 25.0, 0.50, 6.25),
    "opus-4.8": Model("anthropic/claude-opus-4.8", "opus-4.8", "anthropic", 5.0, 25.0, 0.50, 6.25),
    "gpt-5.6-sol": Model("openai/gpt-5.6-sol", "gpt-5.6-sol", "openai", 5.0, 30.0, 0.50, 6.25),
    "gemini-3.1-pro": Model(
        "google/gemini-3.1-pro-preview", "gemini-3.1-pro", "google", 2.0, 12.0, 0.20, 0.375
    ),
    "deepseek-v4-pro": Model(
        "deepseek/deepseek-v4-pro", "deepseek-v4-pro", "deepseek", 1.168, 2.336, 0.0986, 0.0
    ),
}

# Cheap stand-ins with the same wire format and the same tool support, for proving the
# pipeline end to end before spending the real budget. A pilot on these costs cents and
# exercises every line of code a ranked run does — which is the only way to discover a
# binding failure without paying frontier prices to discover it.
PILOT: dict[str, Model] = {
    "deepseek-v4-flash": Model(
        "deepseek/deepseek-v4-flash", "deepseek-v4-flash", "deepseek", 0.063, 0.126, 0.0126, 0.0
    ),
    "gemini-3.7-flash": Model(
        "google/gemini-3.7-flash", "gemini-3.7-flash", "google", 0.375, 1.875, 0.0375, 0.0208
    ),
    "gpt-5.6-terra": Model("openai/gpt-5.6-terra", "gpt-5.6-terra", "openai", 1.0, 6.0, 0.10, 1.25),
}

ALL: dict[str, Model] = {**RANKED, **PILOT}


def resolve(name: str) -> Model:
    """Look a model up by short key or by full OpenRouter slug.

    Accepts both because the short key is what a container name and a report column want,
    while the slug is what an operator copies out of OpenRouter's dashboard. Raising on an
    unknown name rather than defaulting is deliberate: a typo that silently fell back to
    some default model would produce a run whose rows name a model that never played, and
    the board would publish it.
    """
    if name in ALL:
        return ALL[name]
    for m in ALL.values():
        if m.slug == name:
            return m
        # Tolerate a bare vendor-less id ("claude-opus-5"), which is what someone reading
        # our own docs is most likely to type.
        if m.slug.split("/", 1)[-1] == name:
            return m
    raise KeyError(
        f"unknown model {name!r}; known: " + ", ".join(sorted(ALL)) + " (or a full OpenRouter slug)"
    )
