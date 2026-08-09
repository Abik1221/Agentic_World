"""Automatic, zero-config LLM usage capture.

Call ``pyyol.instrument()`` once at startup and the SDK transparently wraps the
OpenAI and Anthropic clients: every non-streaming completion has its **real** model,
token counts, and cost recorded — no `log_model_call` boilerplate, and the numbers
come from the provider's own response, not a manifest the developer typed. During a
turn the usage is accumulated and auto-attached to the outgoing move
(`runtime._handle_turn`), so it reaches the arena benchmark even with Lens disabled.

    import pyyol
    pyyol.instrument()          # once, at import/startup

    @agent.on_turn("goofspiel")
    def decide(view):
        # ordinary client call — usage is captured automatically
        resp = client.chat.completions.create(model="gpt-4o", messages=[...])
        return GoofspielMove(card=..., round=view.round)

Design:
  * Never imports a provider — patches only what's already installed.
  * Every capture path is wrapped so instrumentation can never raise into the
    developer's call.
  * Idempotent; ``uninstrument()`` restores originals (used by tests).
  * Limitation (Phase 1): streaming responses carry no usage on the returned
    iterator; pass ``stream_options={"include_usage": True}`` and record manually,
    or use non-streaming calls for automatic capture.
"""

from __future__ import annotations

import functools
import importlib
import inspect
import time
from typing import Any

from . import pricing, providers, scaffold
from .telemetry import current_span, current_usage

# (class, method_name, original_callable) for uninstrument().
_PATCHED: list[tuple[Any, str, Any]] = []

# --- Verified-tier gateway routing (Phase 4c) ---------------------------------
# When routing is enabled, the instrument wrapper injects the Pyyol identity headers
# (X-Pyyol-Key/Match/Turn) into every LLM call so the Pyyol Gateway can attribute the
# server-observed usage to the right agent/match/turn. route() points a client's
# base_url at the gateway. Together they make ranked LLM traffic flow through the
# gateway with a single opt-in line, and the gateway measures the REAL model/cost.

_gateway: dict[str, str] = {}  # {"key": agent_key, "base": gateway_base_url}


def enable_gateway(agent_key: str, base_url: str) -> None:
    """Enable gateway routing: subsequent instrumented calls carry the Pyyol identity
    headers. Called by the runtime in ranked mode; safe to call directly in tests."""
    _gateway["key"] = agent_key or ""
    _gateway["base"] = (base_url or "").rstrip("/")


def disable_gateway() -> None:
    _gateway.clear()


# Which WIRE FORMAT a provider speaks. This decides the gateway path, and it is the only
# per-provider knowledge routing needs.
#
# WHY NOT A PROVIDER->PATH TABLE. There was one, listing openai, anthropic and groq. Every
# other provider — Gemini, Mistral, DeepSeek, Cohere, xAI, Together, OpenRouter, Fireworks,
# and every self-hosted vLLM or Ollama server — resolved to "" and was therefore NOT ROUTED,
# silently. Those agents produced no proofs, could never earn Verified, and in ranked play can
# have decisions treated as unproven. Nothing told the developer. A table that has to name
# every provider is always one release behind the ecosystem, so the verified tier was
# structurally OpenAI-and-Anthropic-only.
#
# What actually varies is one bit: does the provider's own SDK append a version segment to the
# base URL, or not?
#
#   OpenAI-wire clients call {base}/chat/completions        -> the base must end in /v1
#   Anthropic clients call   {base}/v1/messages             -> the base must NOT
#   Google clients call      {base}/v1beta/models/...       -> the base must NOT
#
# So three cases, and OpenAI-wire is the DEFAULT because the overwhelming majority of the
# ecosystem speaks it. A provider nobody has heard of routes correctly on the day it ships.
_ANTHROPIC_WIRE = frozenset({"anthropic"})
_GOOGLE_WIRE = frozenset({"google", "vertex"})


def _wire_suffix(provider: str) -> str:
    """The version segment the gateway base needs for this provider's wire format."""
    p = (provider or "").lower()
    if p in _ANTHROPIC_WIRE or p in _GOOGLE_WIRE:
        return ""
    return "/v1"


def gateway_base_url(provider: str) -> str:
    """The gateway base_url a `provider` client should point at, or "" if routing is off or
    the provider cannot be routed.

    Returns "" for a LOCAL/self-hosted provider. That is not an oversight: the gateway runs on
    Pyyol's side and cannot reach a model server on the developer's own machine, so pointing a
    client at it would break every call. Such play is unverified — and also free, so there is no
    cost attribution to lose either.
    """
    base = _gateway.get("base", "")
    if not base or not provider:
        return ""
    if providers.is_self_hosted(provider):
        return ""
    return f"{base}/gw/{provider}{_wire_suffix(provider)}"


def gateway_headers() -> dict[str, str]:
    """The X-Pyyol-* identity headers for the current turn (empty if routing off)."""
    if not _gateway.get("key"):
        return {}
    h = {"X-Pyyol-Key": _gateway["key"]}
    acc = current_usage()
    if acc is not None:
        if getattr(acc, "match_id", ""):
            h["X-Pyyol-Match"] = acc.match_id
        h["X-Pyyol-Turn"] = str(getattr(acc, "turn", 0))
        # Only this header makes the attribution provable. Match and turn are chosen
        # by us and could name any decision; the proof is minted by the platform for
        # one specific turn, so a call carrying it could not have been fabricated.
        # Absent on older platforms — the call is then simply unproven, not rejected.
        proof = getattr(acc, "turn_proof", "")
        if proof:
            h["X-Pyyol-Proof"] = proof
    return h


def route(client: Any, provider: str | None = None) -> Any:
    """Point a provider client at the Pyyol Gateway (sets its base_url). Explicit,
    robust opt-in — operates on the instance the developer hands us, so it doesn't
    depend on provider-internal layout. Returns the same client for chaining. A no-op
    when routing is disabled or the provider can't be determined."""
    prov = provider or _detect_provider(client)
    if not prov:
        # LOUD, not silent.
        #
        # This used to return the client untouched, so an agent using an
        # unrecognised client looked instrumented, reported zero tokens, and could
        # never earn Verified — and in ranked, decisions that carry no proof can have
        # the match voided. The developer had no way to discover any of that until it
        # cost them. A warning is the difference between a five-minute fix and a
        # silently unverifiable agent.
        _warn(
            "pyyol.route(): could not identify the provider behind "
            f"{type(client).__module__}.{type(client).__name__}, so this client is "
            "NOT routed through the Pyyol Gateway. Its usage will not be verified. "
            'Pass provider= explicitly ("openai", "anthropic", "groq") if you '
            "know which wire format it speaks."
        )
        return client

    url = gateway_base_url(prov)
    if not url:
        # Two different reasons land here, and conflating them is how the old code hid a real
        # problem behind a benign one.
        if _gateway.get("base") and providers.is_self_hosted(prov):
            # A local model server. The gateway cannot reach the developer's own machine, so
            # NOT routing is correct — but say so, because "verified" will be absent and the
            # developer should know that was a consequence of their choice rather than a bug.
            _warn(
                f"pyyol.route(): {prov} runs on your own machine, so it cannot be routed "
                "through the Pyyol Gateway (we cannot reach your host). These calls are free "
                "and will be recorded as self-reported rather than verified."
            )
            return client
        # Routing simply not enabled (no gateway configured) — the normal state in
        # local and sandbox play, where usage is self-reported and that is fine.
        # DELIBERATELY SILENT: warning here would fire on every run for every
        # developer, and a warning that always fires is one people learn to ignore —
        # including the one below that actually means something.
        return client

    try:
        client.base_url = url
    except Exception as e:  # noqa: BLE001
        _warn(
            f"pyyol.route(): could not set base_url on this {prov} client ({e!r}), so "
            "it is NOT routed and its usage will not be verified."
        )
    return client


def _warn(msg: str) -> None:
    """Surface a routing problem on stderr AND through warnings.

    stderr because agents run in a terminal where a warnings-module message is easy
    to filter away or never see; warnings so a test suite can assert on it.
    """
    import sys
    import warnings

    warnings.warn(msg, RuntimeWarning, stacklevel=3)
    print(f"pyyol: {msg}", file=sys.stderr)


def _detect_provider(client: Any) -> str:
    """Identify the provider behind a client, or "" when we cannot tell.

    Resolution prefers the client's configured base_url over its module name, because
    most of the ecosystem speaks the OpenAI wire format: Ollama, vLLM, LM Studio,
    OpenRouter, Together, Groq, DeepSeek and Azure are all routinely driven through the
    OpenAI SDK with nothing changed but the URL. Classifying those by module would call
    every one of them "openai" — pricing a locally-served Llama at OpenAI's rates and
    filing it under the wrong vendor on the public board.

    See `providers.resolve`. Returns "" only when neither the URL nor the module says
    anything, which is the case `route()` warns about.
    """
    base = ""
    try:
        raw = getattr(client, "base_url", None)
        base = str(raw) if raw else ""
    except Exception:  # noqa: BLE001
        base = ""
    return providers.resolve(module_name=type(client).__module__, base_url=base)


def _resolve_call_provider(resource: Any, patched_as: str) -> str:
    """The provider for one instrumented call.

    `patched_as` is the SDK we wrapped (what wire format this is). The client's
    base_url is consulted first and wins, so an OpenAI client pointed at
    http://localhost:11434/v1 records as `ollama` — self-hosted and free — instead of
    billing the developer for tokens OpenAI never served.

    A base_url that points at the PYYOL GATEWAY is ignored for attribution: it tells us
    the call was proxied, not who served it, and the gateway records the real provider
    itself from the upstream response.
    """
    base = _client_base_url(resource)
    gw = _gateway.get("base", "")
    if base and gw and base.startswith(gw):
        # Routed through us. Recover the upstream from the gateway PATH by matching it
        # back against the routing table, rather than by parsing segments — the table
        # is the thing that defined the path, so the two cannot drift apart.
        # Recover the upstream from the gateway PATH: /gw/<slug>[/v1]. Parsed structurally now
        # that there is no table to match against — which also means a provider added tomorrow
        # is attributed correctly without touching this.
        tail = base[len(gw) :].strip("/")
        parts = tail.split("/")
        if len(parts) >= 2 and parts[0] == "gw" and parts[1]:
            return parts[1]
        return patched_as
    return providers.resolve(base_url=base, fallback=patched_as)


def _get(obj: Any, name: str, default: Any = None) -> Any:
    if isinstance(obj, dict):
        return obj.get(name, default)
    return getattr(obj, name, default)


# --- Usage normalization, by MEANING rather than by vendor ---------------------------
#
# WHY THE VENDOR TABLE HAD TO GO. This had one extractor per provider: OpenAI, Anthropic, the
# OpenAI Responses API, Ollama, Google, Cohere. Everything else returned None and was recorded as
# ZERO tokens and zero cost — silently, because a zero is a plausible-looking integer. New
# providers ship constantly and every self-hosted server (vLLM, LM Studio, llama.cpp, SGLang,
# TGI) has its own dialect, so the table was always one release behind.
#
# What is stable is not the field NAMES but the CONCEPTS: input, output, cache read, cache write,
# reasoning. Matching on those means `prompt_cache_hit_tokens`, `cache_read_input_tokens`,
# `cached_tokens` and `cachedContentTokenCount` all land in one bucket without any being listed.
#
# This mirrors internal/llmgw/usagenorm.go deliberately. The gateway is the authoritative
# observer for the verified tier; if the SDK normalized differently, "verified" and
# "self-reported" would be two different numbers for one call — and the boards rank on cost, so
# the divergence would land as a silent bias in a public ranking rather than a visible bug.
# sdk/conformance/usage_pricing.json pins both against the same expectations.

_CONCEPT_INPUT_PROMPT = "input_prompt"   # whole-prompt family: cache is a SUBSET
_CONCEPT_INPUT_FRESH = "input_fresh"     # fresh-input family: cache is ADDITIVE
_CONCEPT_OUTPUT = "output"
_CONCEPT_CACHE_READ = "cache_read"
_CONCEPT_CACHE_WRITE = "cache_write"
_CONCEPT_REASONING = "reasoning"
_CONCEPT_TOTAL = "total"

_MODEL_KEYS = frozenset({"model", "modelversion", "modelid", "modelname", "modelslug"})


def _classify_usage_key(key: str) -> str | None:
    """What a response field MEANS, independent of what it is called.

    Ordered most-specific-first: "cache_read_input_tokens" contains both "cache" and "input" and
    must read as a cache field, not an input count. Getting that order wrong would make
    Anthropic's cache read look like its input total.
    """
    k = key.lower().replace("-", "_")

    if "cach" in k:
        if "creat" in k or "writ" in k:
            return _CONCEPT_CACHE_WRITE
        if "miss" in k:
            # NOT a cache concept. A miss is ordinary uncached input, already inside the prompt
            # total that accompanies it (DeepSeek documents prompt == hit + miss), so counting it
            # would double-bill.
            return None
        if "read" in k or "hit" in k or "cached" in k:
            return _CONCEPT_CACHE_READ
        # A bare cache count with no direction: read is the cheaper and therefore conservative
        # reading — overstating a discount is worse than understating it.
        return _CONCEPT_CACHE_READ
    if "reasoning" in k or "thought" in k:
        return _CONCEPT_REASONING
    if "total" in k:
        return _CONCEPT_TOTAL
    # Output before input: these are unambiguous, and doing them first keeps the input rules from
    # having to exclude them.
    if "completion" in k or "output" in k or "candidates" in k or k == "eval_count":
        return _CONCEPT_OUTPUT
    if "prompt" in k:
        return _CONCEPT_INPUT_PROMPT
    if "input" in k:
        return _CONCEPT_INPUT_FRESH
    return None


def _is_model_key(key: str) -> bool:
    k = key.lower()
    for ch in ("_", "-", "."):
        k = k.replace(ch, "")
    return k in _MODEL_KEYS


def _usage_fields(node: Any) -> dict[str, Any] | None:
    """The named fields of a mapping or a provider SDK object, or None."""
    if isinstance(node, dict):
        return node
    if isinstance(node, (str, bytes, bytearray, int, float, bool)) or node is None:
        return None
    out: dict[str, Any] = {}
    for klass in reversed(getattr(type(node), "__mro__", ())):
        for key, value in vars(klass).items():
            if key.startswith("_") or callable(value) or isinstance(value, property):
                continue
            out[key] = value
    for key in getattr(type(node), "__slots__", ()) or ():
        if not key.startswith("_"):
            try:
                out[key] = getattr(node, key)
            except Exception:  # noqa: BLE001 - a raising descriptor must not break a call
                pass
    data = getattr(node, "__dict__", None)
    if isinstance(data, dict):
        for key, value in data.items():
            if not key.startswith("_"):
                out[key] = value
    return out or None


def _harvest_usage(node: Any, acc: dict[str, Any], depth: int = 0) -> None:
    """Walk any response and accumulate usage by concept.

    Takes the MAXIMUM per concept rather than the last value seen: responses repeat counts, and a
    later zero for a field the provider is not reporting would erase a real count already found.

    Depth-bounded so a self-referential SDK object cannot spin forever.
    """
    if depth > 12:
        return
    if isinstance(node, (list, tuple)):
        for item in node:
            _harvest_usage(item, acc, depth + 1)
        return
    fields = _usage_fields(node)
    if fields is None:
        return
    for key in sorted(fields):
        value = fields[key]
        if _is_model_key(key) and isinstance(value, str) and value and not acc.get("model"):
            acc["model"] = value
            continue
        if isinstance(value, bool):
            continue
        if isinstance(value, (int, float)):
            concept = _classify_usage_key(key)
            if concept and value > 0:
                if value > acc.get(concept, 0):
                    acc[concept] = int(value)
                    acc["found"] = True
            continue
        _harvest_usage(value, acc, depth + 1)


# Container keys that hold accounting. Harvesting these FIRST is what stops an unrelated number
# elsewhere in the response from being read as a token count — a real risk once the walk is
# general, and one a "take the largest match" rule gets wrong every time.
_USAGE_CONTAINER_HINTS = ("usage", "tokens", "accounting", "billing")


def _harvest_containers(node: Any, acc: dict[str, Any], depth: int = 0) -> None:
    """Harvest only inside containers whose NAME says they hold accounting.

    Two-pass exists because of a decoy: a response carrying both a real ``usage`` object and a
    stray ``prompt_eval_count`` elsewhere must be read from ``usage``. Scanning the whole document
    and keeping the largest value would prefer whichever number happened to be bigger, which is
    not a rule — it is a coin flip on the cost of the call.
    """
    if depth > 12:
        return
    if isinstance(node, (list, tuple)):
        for item in node:
            _harvest_containers(item, acc, depth + 1)
        return
    fields = _usage_fields(node)
    if fields is None:
        return

    # The CANONICAL envelope wins outright where it exists. `usage` is what OpenAI, Anthropic,
    # Bedrock, Mistral and every OpenAI-wire server fill in; the alternatives below are what a
    # provider uses INSTEAD of it, never alongside. A response carrying both (a proxy echoing
    # dialects, or a stale field) must be read from `usage` — harvesting every container and
    # keeping the largest number would let a decoy set the cost of the call.
    primary = None
    for key, value in fields.items():
        if key.lower().replace("-", "").replace("_", "") == "usage" and not isinstance(
            value, (str, bytes, int, float, bool)
        ):
            primary = value
            break

    for key in sorted(fields):
        value = fields[key]
        if _is_model_key(key) and isinstance(value, str) and value and not acc.get("model"):
            acc["model"] = value
            continue
        if primary is not None:
            continue  # only the canonical envelope, harvested below
        lowered = key.lower()
        if any(h in lowered for h in _USAGE_CONTAINER_HINTS) and not isinstance(
            value, (str, bytes, int, float, bool)
        ):
            _harvest_usage(value, acc, depth + 1)
            continue
        _harvest_containers(value, acc, depth + 1)

    if primary is not None:
        _harvest_usage(primary, acc, depth + 1)
        # Keep descending for a NESTED envelope: Anthropic's streaming shape puts usage inside a
        # `message` object, so a frame's outer level may hold none of the counts.
        for key in sorted(fields):
            if key.lower().replace("-", "").replace("_", "") != "usage":
                _harvest_containers(fields[key], acc, depth + 1)


def _vendor_from_markers(resp: Any) -> str:
    """A vendor label inferred from DISTINCTIVE container names, not from token field names.

    Kept because the label is user-visible and feeds pricing: an open-weight model is free when
    self-hosted and billed when a hosted provider serves it, with the same model id either way.
    Only markers that genuinely identify one vendor are listed, and this is a LAST-RESORT hint —
    the caller's own resolution from the client's base_url wins wherever it has one, because most
    of the ecosystem speaks the OpenAI format without being OpenAI.
    """
    fields = _usage_fields(resp) or {}
    keys = {k.lower().replace("-", "").replace("_", "") for k in fields}
    # A plain `usage` envelope is the OpenAI/Anthropic shape and OUTRANKS every marker below. A
    # response can legitimately carry both (a proxy that echoes several dialects, or a decoy), and
    # in that case the primary envelope is the one the provider actually filled in — so the wire
    # label is decided by the token field names inside it rather than by a marker's presence.
    if "usage" in keys:
        return ""
    if "usagemetadata" in keys:
        return providers.GOOGLE
    if "prompteval count".replace(" ", "") in keys or "evalcount" in keys:
        return providers.OLLAMA
    meta = fields.get("meta")
    if meta is not None and (_usage_fields(meta) or {}).get("tokens") is not None:
        return "cohere"
    return ""


def extract_usage(resp: Any) -> dict[str, Any] | None:
    """Pull normalized usage from ANY provider response, or None if it carries none.

    Normalizes onto ONE convention: ``prompt_tokens`` is the total billable input, with cache
    reads and writes as SUBSETS of it. Providers genuinely disagree here and the disagreement is
    silent, so the rule follows the WORD USED rather than a vendor list:

      * a PROMPT-family key names the whole prompt, so cache is already inside it
        (OpenAI's ``prompt_tokens``, Google's ``promptTokenCount``, DeepSeek's ``prompt_tokens``)
      * an INPUT-family key names fresh input, so cache is billed on top
        (Anthropic's ``input_tokens``, Bedrock's ``inputTokens``)

    A reported total cross-checks the additive case, so a provider using "input" for a
    cache-inclusive total is corrected by its own arithmetic instead of being over-counted.
    """
    acc: dict[str, Any] = {}
    _harvest_containers(resp, acc)
    if not acc.get("found"):
        # No named accounting container. Some providers (Ollama natively) put their counts at the
        # TOP LEVEL with no envelope, so fall back to the whole document rather than reporting a
        # local model as free — a zero there is never contradicted by an invoice, which is exactly
        # why it went unnoticed for so long.
        _harvest_usage(resp, acc)
    if not acc.get("found"):
        return None

    cache_read = acc.get(_CONCEPT_CACHE_READ, 0)
    cache_write = acc.get(_CONCEPT_CACHE_WRITE, 0)
    reasoning = acc.get(_CONCEPT_REASONING, 0)
    completion = acc.get(_CONCEPT_OUTPUT, 0)
    total = acc.get(_CONCEPT_TOTAL, 0)
    in_prompt = acc.get(_CONCEPT_INPUT_PROMPT, 0)
    in_fresh = acc.get(_CONCEPT_INPUT_FRESH, 0)

    if in_prompt > 0:
        prompt = in_prompt
    elif in_fresh > 0:
        prompt = in_fresh + cache_read + cache_write
        if total > 0 and prompt > total - completion and total - completion >= in_fresh:
            prompt = total - completion
    else:
        # No input count, but cache counts present: the cache IS the input we know about.
        prompt = cache_read + cache_write

    # A cache count larger than the prompt total cannot be a subset of it. Raise the total rather
    # than let pricing clamp the excess away as if it had never been billed.
    prompt = max(prompt, cache_read + cache_write)

    # The wire FORMAT this looked like, as a last-resort provider label. Not a vendor claim: the
    # caller's own resolution (which reads the client's base_url) wins wherever it has one,
    # because most of the ecosystem speaks the OpenAI format without being OpenAI.
    wire = _vendor_from_markers(resp)
    if not wire:
        if in_prompt > 0:
            wire = "openai"
        elif in_fresh > 0:
            wire = "anthropic"

    return {
        "model": acc.get("model", "") or "",
        "provider": wire,
        "prompt_tokens": int(prompt),
        "completion_tokens": int(completion),
        "cached_tokens": int(cache_read),
        "cached_write_tokens": int(cache_write),
        "reasoning_tokens": int(reasoning),
    }


def record_response(
    resp: Any, *, provider: str = "", latency_ms: int = 0
) -> dict[str, Any] | None:
    """Record usage from a provider response: compute cost, add to the turn
    accumulator, and emit a Lens ``model_call`` span. Returns the extracted usage (or
    None). Also the public manual hook for clients this module doesn't auto-wrap.
    """
    info = extract_usage(resp)
    if info is None:
        return None
    prov = provider or info["provider"]
    cost = pricing.estimate_cost(
        info["model"],
        info["prompt_tokens"],
        info["completion_tokens"],
        cached_tokens=info["cached_tokens"],
        cached_write_tokens=info["cached_write_tokens"],
        reasoning_tokens=info["reasoning_tokens"],
        # WHO served it, not just what was served. An open-weight model is free when
        # you run it yourself and billed when a hosted provider serves it, and the
        # model id is identical either way — so without this a Groq-backed agent
        # reported $0 on a platform that advertises verified cost tracking.
        provider=prov,
    )
    acc = current_usage()
    if acc is not None:
        acc.add(
            model=info["model"],
            provider=prov,
            prompt_tokens=info["prompt_tokens"],
            completion_tokens=info["completion_tokens"],
            reasoning_tokens=info["reasoning_tokens"],
            cached_tokens=info["cached_tokens"],
            cached_write_tokens=info["cached_write_tokens"],
            estimated_cost=cost,
            latency_ms=latency_ms,
        )
    current_span().log_model_call(
        provider=prov,
        model=info["model"],
        prompt_tokens=info["prompt_tokens"],
        completion_tokens=info["completion_tokens"],
        total_tokens=info["prompt_tokens"] + info["completion_tokens"],
        estimated_cost=cost,
        latency_ms=latency_ms,
    )
    return info


def _client_base_url(resource: Any) -> str:
    """Best-effort read of the base_url the provider client will actually call. The
    patched method is bound to a resource (e.g. Completions) whose ``_client`` holds the
    configured base_url. Returns "" if it can't be determined."""
    try:
        client = getattr(resource, "_client", None)
        base = getattr(client, "base_url", None)
        return str(base).rstrip("/") if base else ""
    except Exception:  # noqa: BLE001
        return ""


def _targets_gateway(resource: Any) -> bool:
    """True only when this call's client is pointed at the Pyyol Gateway. Guards the
    identity-header injection so the X-Pyyol-Key credential is NEVER sent to a
    third-party provider (e.g. a client the dev forgot to route()) — only to the
    gateway that issued it."""
    gw = _gateway.get("base", "")
    if not gw:
        return False
    base = _client_base_url(resource)
    return bool(base) and base.startswith(gw)


def _inject_gateway_headers(resource: Any, kwargs: dict[str, Any]) -> None:
    """Merge the Pyyol identity headers into the call's extra_headers (both OpenAI and
    Anthropic accept extra_headers). Dev-supplied headers win. No-op when routing off OR
    when the call does not target the gateway — the credential never leaves for a
    third-party host."""
    if not _targets_gateway(resource):
        return
    headers = gateway_headers()
    if not headers:
        return
    try:
        existing = kwargs.get("extra_headers") or {}
        merged = {**headers, **dict(existing)}  # dev-supplied overrides
        kwargs["extra_headers"] = merged
    except Exception:  # noqa: BLE001 - never break the dev's call
        pass


def _safe_record(resp: Any, provider: str, start: float, resource: Any = None) -> None:
    try:
        # Resolve from the CLIENT's base_url when we have one — the wire format we
        # patched is not the same thing as who served the call.
        prov = _resolve_call_provider(resource, provider) if resource is not None else provider
        record_response(resp, provider=prov, latency_ms=int((time.perf_counter() - start) * 1000))
    except Exception:  # noqa: BLE001 - instrumentation must never break the dev's call
        pass


def _safe_scaffold(kwargs: dict[str, Any], endpoint: str) -> None:
    """Fingerprint the scaffold from the outgoing request, before the model is called.

    Done on the REQUEST rather than the response because the scaffold is the thing the
    developer wrote — system prompt, tools, sampling — and none of that comes back. Wrapped
    like every other instrumentation hook: a fingerprinting problem must never be why a
    developer's model call fails.
    """
    try:
        acc = current_usage()
        if acc is None:
            return
        fp = scaffold.from_request(kwargs, endpoint=endpoint)
        acc.observe_scaffold(fp, "" if fp else scaffold.issue(kwargs, endpoint=endpoint))
    except Exception:  # noqa: BLE001 - instrumentation must never break the dev's call
        pass


def _patch_method(
    module_path: str, class_name: str, method: str, provider: str, endpoint: str = ""
) -> bool:
    """Wrap ``module.Class.method`` so its return value is recorded. Handles both sync
    and async originals. Idempotent and fully guarded."""
    try:
        mod = importlib.import_module(module_path)
    except Exception:  # noqa: BLE001 - provider not installed / different layout
        return False
    cls = getattr(mod, class_name, None)
    if cls is None:
        return False
    orig = getattr(cls, method, None)
    if orig is None or getattr(orig, "_pyyol_instrumented", False):
        return False

    if inspect.iscoroutinefunction(orig):

        @functools.wraps(orig)
        async def wrapper(*args: Any, **kwargs: Any) -> Any:
            resource = args[0] if args else None
            _inject_gateway_headers(resource, kwargs)
            _safe_scaffold(kwargs, endpoint)
            start = time.perf_counter()
            resp = await orig(*args, **kwargs)
            _safe_record(resp, provider, start, resource)
            return resp

    else:

        @functools.wraps(orig)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            resource = args[0] if args else None
            _inject_gateway_headers(resource, kwargs)
            _safe_scaffold(kwargs, endpoint)
            start = time.perf_counter()
            resp = orig(*args, **kwargs)
            _safe_record(resp, provider, start, resource)
            return resp

    wrapper._pyyol_instrumented = True  # type: ignore[attr-defined]
    try:
        setattr(cls, method, wrapper)
    except Exception:  # noqa: BLE001
        return False
    _PATCHED.append((cls, method, orig))
    return True


def _patch_bound_module_func(module_path: str, name: str, provider: str) -> bool:
    """Wrap a module-level function that was BOUND at import time.

    Some SDKs expose conveniences by capturing a default client's bound methods at
    import (``ollama.chat = _client.chat``). Patching the class afterwards does not
    touch those references, so an agent calling ``ollama.chat(...)`` — the form every
    tutorial uses — would go completely unmeasured while the class patch reported
    success.
    """
    try:
        mod = importlib.import_module(module_path)
    except Exception:  # noqa: BLE001
        return False
    orig = getattr(mod, name, None)
    if orig is None or not callable(orig) or getattr(orig, "_pyyol_instrumented", False):
        return False

    if inspect.iscoroutinefunction(orig):

        @functools.wraps(orig)
        async def wrapper(*args: Any, **kwargs: Any) -> Any:
            start = time.perf_counter()
            resp = await orig(*args, **kwargs)
            _safe_record(resp, provider, start, getattr(orig, "__self__", None))
            return resp

    else:

        @functools.wraps(orig)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            start = time.perf_counter()
            resp = orig(*args, **kwargs)
            _safe_record(resp, provider, start, getattr(orig, "__self__", None))
            return resp

    wrapper._pyyol_instrumented = True  # type: ignore[attr-defined]
    try:
        setattr(mod, name, wrapper)
    except Exception:  # noqa: BLE001
        return False
    _PATCHED.append((mod, name, orig))
    return True


def _patch_openai() -> bool:
    patched = False
    for module_path, class_name in (
        ("openai.resources.chat.completions", "Completions"),
        ("openai.resources.chat.completions", "AsyncCompletions"),
        ("openai.resources.responses", "Responses"),
        ("openai.resources.responses", "AsyncResponses"),
    ):
        endpoint = (
            "openai.responses" if "responses" in module_path else "openai.chat.completions"
        )
        patched |= _patch_method(module_path, class_name, "create", "openai", endpoint)
    return patched


def _patch_anthropic() -> bool:
    patched = False
    for class_name in ("Messages", "AsyncMessages"):
        patched |= _patch_method(
            "anthropic.resources.messages", class_name, "create", "anthropic", "anthropic.messages"
        )
    return patched


def _patch_ollama() -> bool:
    """Ollama's native client, both call styles.

    Local models are the case people most often assume "just works" and least often
    check: no bill arrives to contradict a zero, so an unmeasured Ollama agent looks
    exactly like a cheap one.
    """
    patched = False
    for class_name in ("Client", "AsyncClient"):
        for method in ("chat", "generate"):
            patched |= _patch_method(
                "ollama._client", class_name, method, providers.OLLAMA, f"ollama.{method}"
            )
    # The module-level conveniences are bound to a default client at import.
    for name in ("chat", "generate"):
        patched |= _patch_bound_module_func("ollama", name, providers.OLLAMA)
    return patched


def _patch_google() -> bool:
    """Both Google SDKs: the current google-genai and the legacy google-generativeai."""
    patched = False
    for class_name in ("Models", "AsyncModels"):
        patched |= _patch_method(
            "google.genai.models",
            class_name,
            "generate_content",
            providers.GOOGLE,
            "google.generate_content",
        )
    patched |= _patch_method(
        "google.generativeai.generative_models",
        "GenerativeModel",
        "generate_content",
        providers.GOOGLE,
        "google.generate_content",
    )
    return patched


def _patch_cohere() -> bool:
    patched = False
    for class_name in ("Client", "AsyncClient", "ClientV2", "AsyncClientV2"):
        patched |= _patch_method("cohere.client", class_name, "chat", "cohere", "cohere.chat")
    return patched


# Every backend the SDK can auto-capture. Note this list is about NATIVE clients only:
# any OpenAI-compatible endpoint (vLLM, LM Studio, llama.cpp, OpenRouter, Together,
# Groq, DeepSeek, Azure, xAI, Perplexity, Cerebras…) is already covered by the OpenAI
# patch, and `providers.resolve` reads its base_url to attribute it correctly.
_PATCHERS = {
    "openai": _patch_openai,
    "anthropic": _patch_anthropic,
    "ollama": _patch_ollama,
    "google": _patch_google,
    "cohere": _patch_cohere,
}


def instrument(providers: list[str] | None = None) -> list[str]:
    """Auto-capture LLM usage from installed providers. Pass e.g. ``["openai"]`` to
    limit which are patched; default patches every supported backend that is
    installed. Returns the list actually instrumented. Safe to call more than once."""
    want = set(providers) if providers is not None else set(_PATCHERS)
    done: list[str] = []
    for name, patch in _PATCHERS.items():
        if name in want and patch():
            done.append(name)
    return done


def uninstrument() -> None:
    """Restore all patched methods/functions (primarily for tests)."""
    while _PATCHED:
        target, name, orig = _PATCHED.pop()
        try:
            setattr(target, name, orig)
        except Exception:  # noqa: BLE001
            pass
