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

from . import pricing
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

    Detection is by module name, so a client is recognised by what it IS rather than
    what it is configured to talk to. Note the Groq case has TWO shapes: the native
    `groq` package, and the far more common one of pointing the OpenAI SDK at Groq's
    OpenAI-compatible endpoint. The second correctly reports "openai" — it IS an
    OpenAI client — and the gateway routes by path, so that keeps working.
    """
    mod = type(client).__module__.lower()
    if "groq" in mod:
        return "groq"
    if "openai" in mod:
        return "openai"
    if "anthropic" in mod:
        return "anthropic"
    return ""


def _get(obj: Any, name: str, default: Any = None) -> Any:
    if isinstance(obj, dict):
        return obj.get(name, default)
    return getattr(obj, name, default)


def extract_usage(resp: Any) -> Optional[Dict[str, Any]]:
    """Pull normalized usage from a provider response, or None if it has none.

    Handles OpenAI Chat Completions (``prompt_tokens``/``completion_tokens`` with
    ``prompt_tokens_details.cached_tokens`` + ``completion_tokens_details.reasoning_tokens``),
    Anthropic Messages (``input_tokens``/``output_tokens`` + ``cache_read_input_tokens``),
    and the OpenAI Responses API (``input_tokens``/``output_tokens``). Duck-typed so a
    dict or an SDK object both work.
    """
    u = _get(resp, "usage")
    if u is None:
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


def _safe_record(resp: Any, provider: str, start: float) -> None:
    try:
        record_response(
            resp, provider=provider, latency_ms=int((time.perf_counter() - start) * 1000)
        )
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
            _inject_gateway_headers(args[0] if args else None, kwargs)
            start = time.perf_counter()
            resp = await orig(*args, **kwargs)
            _safe_record(resp, provider, start)
            return resp

    else:

        @functools.wraps(orig)
        def wrapper(*args: Any, **kwargs: Any) -> Any:
            _inject_gateway_headers(args[0] if args else None, kwargs)
            start = time.perf_counter()
            resp = orig(*args, **kwargs)
            _safe_record(resp, provider, start)
            return resp

    wrapper._pyyol_instrumented = True  # type: ignore[attr-defined]
    try:
        setattr(cls, method, wrapper)
    except Exception:  # noqa: BLE001
        return False
    _PATCHED.append((cls, method, orig))
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


def instrument(providers: Optional[List[str]] = None) -> List[str]:
    """Auto-capture LLM usage from installed providers. Pass e.g. ``["openai"]`` to
    limit which are patched; default patches all supported providers that are
    installed. Returns the list actually instrumented. Safe to call more than once."""
    want = set(providers) if providers is not None else {"openai", "anthropic"}
    done: List[str] = []
    if "openai" in want and _patch_openai():
        done.append("openai")
    if "anthropic" in want and _patch_anthropic():
        done.append("anthropic")
    return done


def uninstrument() -> None:
    """Restore all patched methods (primarily for tests)."""
    while _PATCHED:
        cls, method, orig = _PATCHED.pop()
        try:
            setattr(cls, method, orig)
        except Exception:  # noqa: BLE001
            pass
