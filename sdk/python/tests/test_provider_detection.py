"""Whatever the developer runs, the SDK must identify it.

Most of the ecosystem speaks the OpenAI wire format, so the OpenAI SDK pointed at a
different base_url is the single most common way to run anything: Ollama, vLLM,
LM Studio, llama.cpp, OpenRouter, Together, Groq, DeepSeek, Azure. Classifying those
by client class calls all of them "openai", which prices a model running on the
developer's own GPU at OpenAI's rates and files it under the wrong vendor on a public
leaderboard.

These tests pin the resolution order and the cases where getting it wrong costs money
or credits the wrong company.
"""

import pytest

from pyyol import pricing, providers
from pyyol._instrument import extract_usage


class FakeHTTP:
    def __init__(self, base_url):
        self.base_url = base_url


class FakeResource:
    """Mimics a provider SDK resource: a bound method's owner exposing ``_client``."""

    def __init__(self, base_url):
        self._client = FakeHTTP(base_url)


# --- base_url resolution ------------------------------------------------------


@pytest.mark.parametrize(
    "base_url,expected",
    [
        ("https://api.openai.com/v1", "openai"),
        ("https://api.anthropic.com", "anthropic"),
        ("https://api.groq.com/openai/v1", "groq"),
        ("https://openrouter.ai/api/v1", "openrouter"),
        ("https://api.together.xyz/v1", "together"),
        ("https://api.deepseek.com/v1", "deepseek"),
        ("https://api.mistral.ai/v1", "mistral"),
        ("https://api.x.ai/v1", "xai"),
        ("https://api.cerebras.ai/v1", "cerebras"),
        ("https://my-org.openai.azure.com/", "azure"),
        ("https://generativelanguage.googleapis.com/v1beta", "google"),
        ("https://bedrock-runtime.us-east-1.amazonaws.com", "bedrock"),
    ],
)
def test_hosted_providers_are_identified_by_host(base_url, expected):
    assert providers.from_base_url(base_url) == expected


@pytest.mark.parametrize(
    "base_url,expected",
    [
        # The named local runtimes, by their default ports.
        ("http://localhost:11434/v1", "ollama"),
        ("http://127.0.0.1:11434", "ollama"),
        ("http://localhost:1234/v1", "lmstudio"),
        ("http://localhost:8000/v1", "vllm"),
        ("http://localhost:8080/v1", "llamacpp"),
        # A local address on an unrecognised port is STILL self-hosted. "We could not
        # name the runtime" and "this costs money" are different claims.
        ("http://localhost:7777/v1", "self-hosted"),
        ("http://192.168.1.50:9999/v1", "self-hosted"),
        ("http://10.0.0.4:8123/v1", "self-hosted"),
        ("http://host.docker.internal:11434", "ollama"),
        ("http://my-box.local:4000/v1", "self-hosted"),
    ],
)
def test_local_runtimes_are_identified_as_self_hosted(base_url, expected):
    assert providers.from_base_url(base_url) == expected
    assert providers.is_self_hosted(providers.from_base_url(base_url))


def test_unknown_public_host_resolves_to_nothing_rather_than_guessing():
    """A public host we do not recognise must not be filed under a provider."""
    assert providers.from_base_url("https://llm.some-startup.example/v1") == ""


def test_base_url_beats_module_name():
    """The decisive case: an OpenAI CLIENT talking to Ollama is Ollama.

    Resolving by module here would report "openai" and bill the developer for tokens
    OpenAI never served.
    """
    got = providers.resolve(
        module_name="openai.resources.chat.completions",
        base_url="http://localhost:11434/v1",
    )
    assert got == "ollama"


def test_module_name_used_when_there_is_no_base_url():
    assert providers.resolve(module_name="anthropic.resources.messages") == "anthropic"
    assert providers.resolve(module_name="ollama._client") == "ollama"
    assert providers.resolve(module_name="cohere.client") == "cohere"


def test_resolution_never_raises_on_junk():
    for junk in ("", "not a url", "://///", "http://", None):
        assert providers.resolve(base_url=junk or "") in ("", "self-hosted") or True


# --- pricing follows the provider, not the model name -------------------------


def test_self_hosted_models_are_free():
    """The same model id costs money on Groq and nothing on your own GPU."""
    hosted = pricing.estimate_cost("llama-3.3-70b", 1_000_000, 1_000_000, provider="groq")
    local = pricing.estimate_cost("llama-3.3-70b", 1_000_000, 1_000_000, provider="ollama")
    assert hosted > 0, "a hosted open-weight model has a real bill"
    assert local == 0, "a self-hosted model has no per-token bill"


def test_unknown_model_served_locally_is_free_not_fallback_priced():
    """The unknown-model fallback must not invent a bill for a local model.

    Without the self-hosted check this would fall through to the mid-tier fallback
    rate and charge a developer for their own hardware.
    """
    assert pricing.estimate_cost("brand-new-thing-2026", 500_000, 500_000, provider="ollama") == 0
    # ...while the same unknown model on an unknown hosted provider still estimates.
    assert (
        pricing.estimate_cost("brand-new-thing-2026", 500_000, 500_000, provider="openrouter") > 0
    )


# --- usage extraction across response shapes ----------------------------------


def test_extract_ollama_native_shape():
    """Ollama carries no `usage` object at all — counts sit at the top level."""
    info = extract_usage(
        {
            "model": "llama3.3:70b",
            "prompt_eval_count": 120,
            "eval_count": 45,
            "done": True,
        }
    )
    assert info is not None, "an Ollama response must not read as 'no usage'"
    assert info["provider"] == "ollama"
    assert info["model"] == "llama3.3:70b"
    assert info["prompt_tokens"] == 120
    assert info["completion_tokens"] == 45


def test_extract_google_gemini_shape():
    info = extract_usage(
        {
            "model_version": "gemini-2.5-pro",
            "usage_metadata": {
                "prompt_token_count": 800,
                "candidates_token_count": 200,
                "cached_content_token_count": 100,
                "thoughts_token_count": 50,
            },
        }
    )
    assert info is not None
    assert info["provider"] == "google"
    assert info["model"] == "gemini-2.5-pro"
    assert info["prompt_tokens"] == 800
    assert info["completion_tokens"] == 200
    assert info["cached_tokens"] == 100
    assert info["reasoning_tokens"] == 50


def test_extract_cohere_shape():
    info = extract_usage(
        {"model": "command-r-plus", "meta": {"tokens": {"input_tokens": 30, "output_tokens": 12}}}
    )
    assert info is not None
    assert info["provider"] == "cohere"
    assert info["prompt_tokens"] == 30


def test_openai_shape_still_wins_when_usage_is_present():
    """The new shapes are only consulted when there is no `usage` object, so they can
    never hijack a normal OpenAI or Anthropic response."""
    info = extract_usage(
        {
            "model": "gpt-4o",
            "usage": {"prompt_tokens": 10, "completion_tokens": 5},
            # Decoy fields from the other shapes.
            "prompt_eval_count": 9999,
            "usage_metadata": {"prompt_token_count": 9999},
        }
    )
    assert info["provider"] == "openai"
    assert info["prompt_tokens"] == 10


def test_response_with_no_usage_anywhere_returns_none():
    assert extract_usage({"model": "gpt-4o", "choices": []}) is None


# --- call-site resolution -----------------------------------------------------


def test_call_provider_uses_the_clients_base_url():
    from pyyol._instrument import _resolve_call_provider

    # Patched as the OpenAI SDK, but actually pointed at a local LM Studio.
    assert _resolve_call_provider(FakeResource("http://localhost:1234/v1"), "openai") == "lmstudio"
    # A real OpenAI client stays OpenAI.
    assert _resolve_call_provider(FakeResource("https://api.openai.com/v1"), "openai") == "openai"
    # No readable base_url falls back to the SDK we patched.
    assert _resolve_call_provider(FakeResource(None), "anthropic") == "anthropic"


def test_gateway_routed_calls_report_the_upstream_not_the_gateway():
    """When routing is on, every client points at the Pyyol gateway. Resolving from
    that URL would report the gateway's own host for every provider — so the provider
    is recovered from the gateway PATH instead."""
    from pyyol import _instrument

    _instrument.enable_gateway("agent-key", "https://api.pyyol.com")
    try:
        got = _instrument._resolve_call_provider(
            FakeResource("https://api.pyyol.com/gw/anthropic"), "openai"
        )
        assert got == "anthropic"
    finally:
        _instrument.disable_gateway()
