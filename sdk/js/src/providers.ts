// Which provider is actually serving this call?
//
// Identifying the provider by the client CLASS is not enough, and getting it wrong is
// not cosmetic: the provider decides whether a call is priced or free, and it is half
// of the model attribution the public benchmark ranks on.
//
// The problem is that most of the ecosystem speaks the OpenAI wire format. Ollama,
// vLLM, LM Studio, llama.cpp, OpenRouter, Together, Groq, DeepSeek, Azure and others
// are all routinely driven through the OpenAI SDK with nothing changed but `baseURL`.
// Classifying those by constructor name calls every one of them "openai" — pricing a
// locally-served Llama at OpenAI's rates and filing it under the wrong vendor.
//
// Resolution order: baseURL host → module/constructor name → caller's fallback.
// Anything on a loopback or private address is SELF-HOSTED even when the runtime is
// unrecognised: it has no per-token bill, and "unknown" would price it as if it did.
//
// Keep the provider keys here in step with `internal/rating/taxonomy.go` and the
// Python SDK's `providers.py` — the backend classifies exactly these strings.

export const OPENAI = "openai";
export const ANTHROPIC = "anthropic";
export const GOOGLE = "google";
export const GROQ = "groq";
export const OLLAMA = "ollama";
export const SELF_HOSTED = "self-hosted";

/** host substring -> provider key. Most specific first. */
const HOST_RULES: ReadonlyArray<readonly [string, string]> = [
  ["api.openai.com", OPENAI],
  ["openai.azure.com", "azure"],
  ["api.anthropic.com", ANTHROPIC],
  ["bedrock-runtime", "bedrock"],
  ["bedrock", "bedrock"],
  ["generativelanguage.googleapis.com", GOOGLE],
  ["aiplatform.googleapis.com", "vertex"],
  ["api.groq.com", GROQ],
  ["openrouter.ai", "openrouter"],
  ["api.together.xyz", "together"],
  ["together.ai", "together"],
  ["api.fireworks.ai", "fireworks"],
  ["api.deepinfra.com", "deepinfra"],
  ["api.mistral.ai", "mistral"],
  ["api.deepseek.com", "deepseek"],
  ["api.cohere.ai", "cohere"],
  ["api.cohere.com", "cohere"],
  ["api.x.ai", "xai"],
  ["api.perplexity.ai", "perplexity"],
  ["api.cerebras.ai", "cerebras"],
  ["api.sambanova.ai", "sambanova"],
  ["api.studio.nebius", "nebius"],
  ["api.hyperbolic.xyz", "hyperbolic"],
  ["api.moonshot", "moonshot"],
  ["dashscope.aliyuncs.com", "alibaba"],
];

/** Default ports of the common local runtimes. */
const LOCAL_PORTS: Record<string, string> = {
  "11434": OLLAMA,
  "1234": "lmstudio",
  "8000": "vllm",
  "8080": "llamacpp",
  "5000": "localai",
  "3000": SELF_HOSTED,
  "9997": SELF_HOSTED,
};

/** constructor/module-name substring -> provider. "openai" LAST: several packages
 *  embed it in their own names. */
const NAME_RULES: ReadonlyArray<readonly [string, string]> = [
  ["ollama", OLLAMA],
  ["anthropic", ANTHROPIC],
  ["groq", GROQ],
  ["mistral", "mistral"],
  ["cohere", "cohere"],
  ["googlegenai", GOOGLE],
  ["generativeai", GOOGLE],
  ["google", GOOGLE],
  ["openai", OPENAI],
];

const SELF_HOSTED_KEYS = new Set([OLLAMA, SELF_HOSTED, "vllm", "lmstudio", "llamacpp", "localai", "tgi"]);

function parse(baseUrl: string): { host: string; port: string } {
  if (!baseUrl) return { host: "", port: "" };
  const withScheme = baseUrl.includes("://") ? baseUrl : `http://${baseUrl}`;
  try {
    const u = new URL(withScheme);
    return { host: u.hostname.toLowerCase(), port: u.port };
  } catch {
    return { host: "", port: "" };
  }
}

/** True for loopback, link-local and private-network addresses, and for the hostnames
 *  that conventionally mean "this machine". A model served from one of these has no
 *  per-token bill, which is why it is worth detecting even when unnamed. */
export function isLocalHost(host: string): boolean {
  if (!host) return false;
  if (["localhost", "127.0.0.1", "::1", "0.0.0.0", "host.docker.internal"].includes(host)) return true;
  if (host.endsWith(".local") || host.endsWith(".internal")) return true;
  // IPv4 private ranges: 10/8, 192.168/16, 172.16–31/12, 127/8, 169.254/16.
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})$/.exec(host);
  if (!m) return false;
  const [a, b] = [Number(m[1]), Number(m[2])];
  return a === 10 || a === 127 || (a === 192 && b === 168) || (a === 172 && b >= 16 && b <= 31) || (a === 169 && b === 254);
}

/** Provider key implied by a base URL, or "" when the host says nothing. A local
 *  address always resolves to SOMETHING — never "" — because "we could not tell" and
 *  "it runs on your own hardware for free" must not be the same answer. */
export function fromBaseUrl(baseUrl: string): string {
  const { host, port } = parse(baseUrl);
  if (!host) return "";
  for (const [needle, provider] of HOST_RULES) {
    if (host.includes(needle)) return provider;
  }
  if (isLocalHost(host)) return LOCAL_PORTS[port] ?? SELF_HOSTED;
  return "";
}

/** Provider key implied by a client's constructor or module name, or "". */
export function fromName(name: string): string {
  const n = (name || "").toLowerCase().replace(/[^a-z]/g, "");
  for (const [needle, provider] of NAME_RULES) {
    if (n.includes(needle)) return provider;
  }
  return "";
}

/** The provider serving this call. baseURL wins over the name: the name only says
 *  which WIRE FORMAT the client speaks, while the URL says who is on the other end —
 *  and for every OpenAI-compatible endpoint those are different answers. */
export function resolve(o: { name?: string; baseUrl?: string; fallback?: string }): string {
  return fromBaseUrl(o.baseUrl ?? "") || fromName(o.name ?? "") || o.fallback || "";
}

/** True when the provider runs on the developer's own hardware, so its tokens carry
 *  no per-token bill. */
export function isSelfHosted(provider: string): boolean {
  return SELF_HOSTED_KEYS.has(provider);
}
