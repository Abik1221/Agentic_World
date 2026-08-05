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
from typing import Any, Dict, List, Optional, Tuple

from . import pricing, providers
from .telemetry import current_span, current_usage

# (class, method_name, original_callable) for uninstrument().
_PATCHED: List[Tuple[Any, str, Any]] = []

# --- Verified-tier gateway routing (Phase 4c) ---------------------------------
# When routing is enabled, the instrument wrapper injects the Pyyol identity headers
# (X-Pyyol-Key/Match/Turn) into every LLM call so the Pyyol Gateway can attribute the
# server-observed usage to the right agent/match/turn. route() points a client's
# base_url at the gateway. Together they make ranked LLM traffic flow through the
# gateway with a single opt-in line, and the gateway measures the REAL model/cost.

_gateway: Dict[str, str] = {}  # {"key": agent_key, "base": gateway_base_url}


def enable_gateway(agent_key: str, base_url: str) -> None:
    """Enable gateway routing: subsequent instrumented calls carry the Pyyol identity
    headers. Called by the runtime in ranked mode; safe to call directly in tests."""
    _gateway["key"] = agent_key or ""
    _gateway["base"] = (base_url or "").rstrip("/")


def disable_gateway() -> None:
    _gateway.clear()


# Per-provider path on the gateway (see internal/llmgateway routing).
# Groq speaks the OpenAI wire format, so it uses the same /v1 suffix — but it needs
# its own gateway path: a Groq model name sent to the OpenAI upstream is just an
# unknown model.
_PROVIDER_PATH = {
    "openai": "/gw/openai/v1",
    "anthropic": "/gw/anthropic",
    "groq": "/gw/groq/v1",
}


def gateway_base_url(provider: str) -> str:
    """The gateway base_url a `provider` client should point at, or "" if routing is
    off / the provider is unknown."""
    base = _gateway.get("base", "")
    path = _PROVIDER_PATH.get(provider, "")
    return base + path if base and path else ""


def gateway_headers() -> Dict[str, str]:
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


def route(client: Any, provider: Optional[str] = None) -> Any:
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
        tail = base[len(gw) :]
        for provider, path in _PROVIDER_PATH.items():
            if tail.startswith(path):
                return provider
        return patched_as
    return providers.resolve(base_url=base, fallback=patched_as)


def _get(obj: Any, name: str, default: Any = None) -> Any:
    if isinstance(obj, dict):
        return obj.get(name, default)
    return getattr(obj, name, default)


def _extract_ollama(resp: Any) -> Optional[Dict[str, Any]]:
    """Ollama's native response shape, which has no ``usage`` object at all.

    Counts live at the top level as ``prompt_eval_count`` / ``eval_count``. Without
    this an agent running Ollama through the native client reported zero tokens
    forever — it looked instrumented and measured nothing.
    """
    prompt = _get(resp, "prompt_eval_count")
    completion = _get(resp, "eval_count")
    if prompt is None and completion is None:
        return None
    return {
        "model": _get(resp, "model", "") or "",
        "provider": providers.OLLAMA,
        "prompt_tokens": int(prompt or 0),
        "completion_tokens": int(completion or 0),
        "cached_tokens": 0,
        "reasoning_tokens": 0,
    }


def _extract_google(resp: Any) -> Optional[Dict[str, Any]]:
    """Google Gemini (google-genai / google-generativeai): counts hang off
    ``usage_metadata`` with their own field names."""
    um = _get(resp, "usage_metadata")
    if um is None:
        return None
    prompt = _get(um, "prompt_token_count", 0) or 0
    completion = _get(um, "candidates_token_count", 0) or 0
    if not prompt and not completion:
        return None
    return {
        # google-genai exposes the resolved model on the response; older shapes do not,
        # in which case the caller's model kwarg is the only source and we leave it to
        # the manual path rather than inventing one.
        "model": _get(resp, "model_version", "") or _get(resp, "model", "") or "",
        "provider": providers.GOOGLE,
        "prompt_tokens": int(prompt),
        "completion_tokens": int(completion),
        "cached_tokens": int(_get(um, "cached_content_token_count", 0) or 0),
        "reasoning_tokens": int(_get(um, "thoughts_token_count", 0) or 0),
    }


def _extract_cohere(resp: Any) -> Optional[Dict[str, Any]]:
    """Cohere nests counts under ``meta.tokens``."""
    meta = _get(resp, "meta")
    if meta is None:
        return None
    tokens = _get(meta, "tokens")
    if tokens is None:
        return None
    prompt = _get(tokens, "input_tokens", 0) or 0
    completion = _get(tokens, "output_tokens", 0) or 0
    if not prompt and not completion:
        return None
    return {
        "model": _get(resp, "model", "") or "",
        "provider": "cohere",
        "prompt_tokens": int(prompt),
        "completion_tokens": int(completion),
        "cached_tokens": 0,
        "reasoning_tokens": 0,
    }


def extract_usage(resp: Any) -> Optional[Dict[str, Any]]:
    """Pull normalized usage from a provider response, or None if it has none.

    Handles OpenAI Chat Completions (``prompt_tokens``/``completion_tokens`` with
    ``prompt_tokens_details.cached_tokens`` + ``completion_tokens_details.reasoning_tokens``),
    Anthropic Messages (``input_tokens``/``output_tokens`` + ``cache_read_input_tokens``),
    the OpenAI Responses API (``input_tokens``/``output_tokens``), Ollama
    (``prompt_eval_count``/``eval_count``, no usage object), Google Gemini
    (``usage_metadata``) and Cohere (``meta.tokens``). Duck-typed so a dict or an SDK
    object both work.
    """
    u = _get(resp, "usage")
    if u is None:
        # Shapes that carry no `usage` at all. Checked in order of how distinctive
        # their marker fields are, so none can claim another's response.
        for extractor in (_extract_ollama, _extract_google, _extract_cohere):
            info = extractor(resp)
            if info is not None:
                return info
        return None

    model = _get(resp, "model", "") or ""

    prompt = _get(u, "prompt_tokens")
    completion = _get(u, "completion_tokens")
    style_openai_chat = prompt is not None or completion is not None
    if prompt is None:
        prompt = _get(u, "input_tokens", 0)
    if completion is None:
        completion = _get(u, "output_tokens", 0)

    cached = 0
    reasoning = 0
    ptd = _get(u, "prompt_tokens_details")
    if ptd is not None:
        cached = _get(ptd, "cached_tokens", 0) or 0
    ctd = _get(u, "completion_tokens_details")
    if ctd is not None:
        reasoning = _get(ctd, "reasoning_tokens", 0) or 0
    if not cached:
        # Anthropic prompt-cache read tokens.
        cached = _get(u, "cache_read_input_tokens", 0) or 0

    # Infer provider from the response shape when the patch site didn't say.
    if style_openai_chat:
        provider = "openai"
    elif _get(u, "input_tokens") is not None:
        provider = "anthropic"
    else:
        provider = ""

    return {
        "model": model,
        "provider": provider,
        "prompt_tokens": int(prompt or 0),
        "completion_tokens": int(completion or 0),
        "cached_tokens": int(cached or 0),
        "reasoning_tokens": int(reasoning or 0),
    }


def record_response(
    resp: Any, *, provider: str = "", latency_ms: int = 0
) -> Optional[Dict[str, Any]]:
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
            estimated_cost=cost,
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


def _inject_gateway_headers(resource: Any, kwargs: Dict[str, Any]) -> None:
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


def _patch_method(module_path: str, class_name: str, method: str, provider: str) -> bool:
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
            start = time.perf_counter()
            resp = await orig(*args, **kwargs)
            _safe_record(resp, provider, start, resource)
            return resp

    else:

        @functools.wraps(orig)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            resource = args[0] if args else None
            _inject_gateway_headers(resource, kwargs)
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
        patched |= _patch_method(module_path, class_name, "create", "openai")
    return patched


def _patch_anthropic() -> bool:
    patched = False
    for class_name in ("Messages", "AsyncMessages"):
        patched |= _patch_method("anthropic.resources.messages", class_name, "create", "anthropic")
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
            patched |= _patch_method("ollama._client", class_name, method, providers.OLLAMA)
    # The module-level conveniences are bound to a default client at import.
    for name in ("chat", "generate"):
        patched |= _patch_bound_module_func("ollama", name, providers.OLLAMA)
    return patched


def _patch_google() -> bool:
    """Both Google SDKs: the current google-genai and the legacy google-generativeai."""
    patched = False
    for class_name in ("Models", "AsyncModels"):
        patched |= _patch_method(
            "google.genai.models", class_name, "generate_content", providers.GOOGLE
        )
    patched |= _patch_method(
        "google.generativeai.generative_models",
        "GenerativeModel",
        "generate_content",
        providers.GOOGLE,
    )
    return patched


def _patch_cohere() -> bool:
    patched = False
    for class_name in ("Client", "AsyncClient", "ClientV2", "AsyncClientV2"):
        patched |= _patch_method("cohere.client", class_name, "chat", "cohere")
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


def instrument(providers: Optional[List[str]] = None) -> List[str]:
    """Auto-capture LLM usage from installed providers. Pass e.g. ``["openai"]`` to
    limit which are patched; default patches every supported backend that is
    installed. Returns the list actually instrumented. Safe to call more than once."""
    want = set(providers) if providers is not None else set(_PATCHERS)
    done: List[str] = []
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
