"""Which provider is actually serving this call?

Identifying the provider by the client CLASS is not enough, and getting it wrong is
not cosmetic: the provider decides whether a call is priced or free, and it is half of
the model attribution the public benchmark ranks on.

The problem is that most of the ecosystem speaks the OpenAI wire format. Ollama,
vLLM, LM Studio, llama.cpp, OpenRouter, Together, Groq, DeepSeek, Fireworks, xAI,
Perplexity and Azure are all commonly used *through the OpenAI SDK* with nothing
changed but ``base_url``. Classifying by module name calls every one of them "openai",
which would price a locally-hosted Llama at OpenAI's rates and file it under the wrong
vendor on the board.

So resolution goes, in order:

1. **base_url host** — where the bytes are actually going. Authoritative, because it
   is the one thing that cannot be true of two different providers at once.
2. **module name** — for native SDKs (anthropic, ollama, cohere, …) and for clients
   that expose no readable base_url.
3. **response shape** — last resort, and only ever distinguishes wire formats.

Anything on a loopback or private-network address is treated as SELF-HOSTED even when
the runtime is unrecognised: a model served from 192.168.x.x has no per-token bill,
and saying "unknown" there would price it as if it did.
"""

from __future__ import annotations

import ipaddress
from typing import Optional, Tuple
from urllib.parse import urlparse

# Canonical provider keys. These are the strings the backend taxonomy classifies, so
# they must stay in step with `internal/rating/taxonomy.go`'s providerTable.
OPENAI = "openai"
ANTHROPIC = "anthropic"
GOOGLE = "google"
GROQ = "groq"
OLLAMA = "ollama"
SELF_HOSTED = "self-hosted"

# host substring -> provider key. Matched against the URL's hostname, longest first so
# a more specific host cannot be shadowed by a shorter one.
_HOST_RULES: Tuple[Tuple[str, str], ...] = (
    ("api.openai.com", OPENAI),
    ("openai.azure.com", "azure"),
    ("api.anthropic.com", ANTHROPIC),
    ("bedrock-runtime", "bedrock"),
    ("bedrock", "bedrock"),
    ("generativelanguage.googleapis.com", GOOGLE),
    ("aiplatform.googleapis.com", "vertex"),
    ("api.groq.com", GROQ),
    ("openrouter.ai", "openrouter"),
    ("api.together.xyz", "together"),
    ("together.ai", "together"),
    ("api.fireworks.ai", "fireworks"),
    ("api.deepinfra.com", "deepinfra"),
    ("api.mistral.ai", "mistral"),
    ("api.deepseek.com", "deepseek"),
    ("api.cohere.ai", "cohere"),
    ("api.cohere.com", "cohere"),
    ("api.x.ai", "xai"),
    ("api.perplexity.ai", "perplexity"),
    ("api.cerebras.ai", "cerebras"),
    ("api.sambanova.ai", "sambanova"),
    ("api.studio.nebius", "nebius"),
    ("api.hyperbolic.xyz", "hyperbolic"),
    ("api.moonshot", "moonshot"),
    ("dashscope.aliyuncs.com", "alibaba"),
)

# Default ports of the common local runtimes. A local address that matches one of
# these names the runtime; one that does not is still self-hosted, just unnamed.
_LOCAL_PORTS = {
    11434: OLLAMA,
    1234: "lmstudio",  # LM Studio's default server port
    8000: "vllm",  # vLLM's default
    8080: "llamacpp",  # llama.cpp server default
    5000: "localai",
    3000: SELF_HOSTED,
    9997: SELF_HOSTED,
}

# module-name substring -> provider key, for native SDKs. Order matters: "openai" is
# checked LAST because several packages embed it in their module path.
_MODULE_RULES: Tuple[Tuple[str, str], ...] = (
    ("ollama", OLLAMA),
    ("anthropic", ANTHROPIC),
    ("groq", GROQ),
    ("mistralai", "mistral"),
    ("cohere", "cohere"),
    ("google.genai", GOOGLE),
    ("google.generativeai", GOOGLE),
    ("google", GOOGLE),
    ("openai", OPENAI),
)


def _hostname(base_url: str) -> Tuple[str, Optional[int]]:
    """(hostname, port) from a base URL, or ("", None) if unparseable."""
    if not base_url:
        return "", None
    u = base_url if "://" in base_url else "http://" + base_url
    try:
        parsed = urlparse(u)
        return (parsed.hostname or "").lower(), parsed.port
    except Exception:  # noqa: BLE001 - never raise out of detection
        return "", None


def is_local_host(host: str) -> bool:
    """True for loopback, link-local and private-network addresses, and for the
    hostnames that conventionally mean 'this machine'.

    A model served from one of these has no per-token bill, which is why it is worth
    detecting even when we cannot name the runtime.
    """
    if not host:
        return False
    if host in ("localhost", "127.0.0.1", "::1", "0.0.0.0", "host.docker.internal"):
        return True
    if host.endswith(".local") or host.endswith(".internal"):
        return True
    try:
        ip = ipaddress.ip_address(host)
        return ip.is_loopback or ip.is_private or ip.is_link_local
    except ValueError:
        return False


def from_base_url(base_url: str) -> str:
    """Provider key implied by a base URL, or "" when the host says nothing.

    A local address always resolves to SOMETHING (the named runtime, or the generic
    self-hosted key) — never to "", because "we could not tell" and "it runs on your
    own hardware for free" must not be the same answer.
    """
    host, port = _hostname(base_url)
    if not host:
        return ""
    for needle, provider in _HOST_RULES:
        if needle in host:
            return provider
    if is_local_host(host):
        return _LOCAL_PORTS.get(port or 0, SELF_HOSTED)
    return ""


def from_module(module_name: str) -> str:
    """Provider key implied by a client's module path, or ""."""
    mod = (module_name or "").lower()
    for needle, provider in _MODULE_RULES:
        if needle in mod:
            return provider
    return ""


def resolve(*, module_name: str = "", base_url: str = "", fallback: str = "") -> str:
    """The provider serving this call.

    base_url wins over the module name, because the module only says which WIRE FORMAT
    the client speaks while the URL says who is on the other end — and for every
    OpenAI-compatible endpoint those are different answers.
    """
    return from_base_url(base_url) or from_module(module_name) or fallback


def is_self_hosted(provider: str) -> bool:
    """True when the provider runs on the developer's own hardware, so its tokens
    carry no per-token bill."""
    return provider in (OLLAMA, SELF_HOSTED, "vllm", "lmstudio", "llamacpp", "localai", "tgi")
