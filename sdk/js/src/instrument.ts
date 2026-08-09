// Automatic, zero-config LLM usage capture (mirrors the Python SDK's instrument.py).
//
// Call `instrument()` once at startup and the SDK transparently wraps the OpenAI
// and Anthropic clients: every non-streaming completion has its REAL model, token
// counts, and cost recorded from the provider's own response — no boilerplate. The
// runtime auto-attaches the captured usage to the outgoing move, so it reaches the
// arena benchmark even with Lens disabled.
//
//   import { instrument } from "pyyol";
//   await instrument();            // once, at startup
//
// Design: never imports a provider that isn't installed; every capture path is
// guarded so instrumentation can never throw into the developer's call; idempotent;
// uninstrument() restores originals (used by tests).
// Limitation (Phase 2): streaming responses carry no usage on the returned stream;
// pass `stream_options: { include_usage: true }` and record manually, or use
// non-streaming calls for automatic capture.

import { estimateCost } from "./pricing.js";
import { fromRequest, issue } from "./scaffold.js";
import * as providers from "./providers.js";
import { currentSpan, currentUsage } from "./telemetry.js";

// eslint-disable-next-line @typescript-eslint/no-explicit-any
type Any = any;

// [prototype, method, original] for uninstrument().
const PATCHED: Array<[Any, string, Any]> = [];

// --- Verified-tier gateway routing (Phase 4c) ---------------------------------
// When routing is enabled, the wrapper injects the Pyyol identity headers
// (X-Pyyol-Key/Match/Turn) into each LLM call's request options so the Pyyol Gateway
// can attribute the server-observed usage; route() points a client's baseURL at the
// gateway. Together, ranked LLM traffic flows through the gateway with one opt-in line.

const gateway: { key: string; base: string } = { key: "", base: "" };

/** Enable gateway routing (called by the runtime in ranked mode; safe in tests). */
export function enableGateway(agentKey: string, baseUrl: string): void {
  gateway.key = agentKey ?? "";
  gateway.base = (baseUrl ?? "").replace(/\/+$/, "");
}

export function disableGateway(): void {
  gateway.key = "";
  gateway.base = "";
}

// Which WIRE FORMAT a provider speaks. This decides the gateway path, and it is the only
// per-provider knowledge routing needs.
//
// WHY NOT A PROVIDER->PATH TABLE. There was one, listing openai and anthropic — not even groq.
// Every other provider resolved to "" and was therefore NOT ROUTED, silently: no proofs, no
// Verified badge, and in ranked play decisions that count as unproven. A table naming every
// provider is always one release behind the ecosystem, so the verified tier was structurally
// OpenAI-and-Anthropic-only.
//
// What actually varies is one bit: does the provider's own SDK append a version segment?
//
//   OpenAI-wire clients call {base}/chat/completions   -> the base must end in /v1
//   Anthropic clients call   {base}/v1/messages        -> the base must NOT
//   Google clients call      {base}/v1beta/models/...  -> the base must NOT
//
// Three cases, with OpenAI-wire the DEFAULT because most of the ecosystem speaks it. A provider
// nobody has heard of routes correctly on the day it ships.
const NO_VERSION_SUFFIX = new Set(["anthropic", "google", "vertex"]);

/**
 * The baseURL a provider client should point at, or "" when routing is off or the provider
 * cannot be routed.
 *
 * Returns "" for a LOCAL/self-hosted provider, deliberately: the gateway runs on Pyyol's side
 * and cannot reach a model server on the developer's own machine, so pointing a client at it
 * would break every call. That play is unverified — and also free, so no cost attribution is
 * lost either.
 */
export function gatewayBaseUrl(provider: string): string {
  if (!gateway.base || !provider) return "";
  if (providers.isSelfHosted(provider)) return "";
  const suffix = NO_VERSION_SUFFIX.has(provider.toLowerCase()) ? "" : "/v1";
  return `${gateway.base}/gw/${provider}${suffix}`;
}

/** The X-Pyyol-* identity headers for the current turn (empty if routing off). */
export function gatewayHeaders(): Record<string, string> {
  if (!gateway.key) return {};
  const h: Record<string, string> = { "X-Pyyol-Key": gateway.key };
  const acc = currentUsage();
  if (acc) {
    if (acc.matchId) h["X-Pyyol-Match"] = acc.matchId;
    h["X-Pyyol-Turn"] = String(acc.turn ?? 0);
  }
  return h;
}

function detectProvider(client: Any): string {
  // baseURL first: most of the ecosystem speaks the OpenAI wire format, so an OpenAI
  // client pointed at http://localhost:11434/v1 IS Ollama — and calling it "openai"
  // would price a model on the developer's own GPU at OpenAI's rates.
  const byUrl = providers.fromBaseUrl(String(client?.baseURL ?? ""));
  if (byUrl) return byUrl;
  const byName = providers.fromName(client?.constructor?.name ?? "");
  if (byName) return byName;
  // duck-type fallback
  if (client?.chat?.completions) return "openai";
  if (client?.messages) return "anthropic";
  return "";
}

/** The provider for one instrumented call.
 *
 *  `patchedAs` is the SDK we wrapped (which wire format this is). The client's baseURL
 *  is consulted first and wins. A baseURL pointing at the PYYOL GATEWAY is not used for
 *  attribution — it says the call was proxied, not who served it — so the upstream is
 *  recovered by PARSING the gateway path (/gw/<slug>[/v1]) rather than matching a table, so a
 *  provider added tomorrow is attributed correctly without touching this. */
export function resolveCallProvider(resource: Any, patchedAs: string): string {
  const base = clientBaseUrl(resource);
  if (base && gateway.base && base.startsWith(gateway.base)) {
    const parts = base.slice(gateway.base.length).replace(/^\/+|\/+$/g, "").split("/");
    if (parts.length >= 2 && parts[0] === "gw" && parts[1]) return parts[1];
    return patchedAs;
  }
  return providers.resolve({ baseUrl: base, fallback: patchedAs });
}

/** Point a provider client at the Pyyol Gateway (sets its baseURL). Explicit, robust
 *  opt-in that operates on the given instance. Returns the client. No-op when routing
 *  is off or the provider can't be determined. */
export function route<T>(client: T, provider?: string): T {
  const prov = provider ?? detectProvider(client);
  const url = prov ? gatewayBaseUrl(prov) : "";
  if (url) {
    try {
      (client as Any).baseURL = url;
    } catch {
      // ignore — never break the caller
    }
  }
  return client;
}

/** Best-effort read of the baseURL the provider client will actually call. The patched
 *  method is bound to a resource whose `_client` holds the configured baseURL. */
function clientBaseUrl(resource: Any): string {
  try {
    const base = resource?._client?.baseURL;
    return base ? String(base).replace(/\/+$/, "") : "";
  } catch {
    return "";
  }
}

/** True only when this call's client is pointed at the Pyyol Gateway. Guards header
 *  injection so the X-Pyyol-Key credential is NEVER sent to a third-party provider
 *  (e.g. a client the dev forgot to route()) — only to the gateway that issued it. */
function targetsGateway(resource: Any): boolean {
  if (!gateway.base) return false;
  const base = clientBaseUrl(resource);
  return !!base && base.startsWith(gateway.base);
}

// injectGatewayHeaders merges the Pyyol identity headers into an LLM call's request
// options. JS SDKs take per-request headers via a SECOND options arg
// (create(body, { headers })), so we ensure args[1].headers carries them. Dev-supplied
// headers win. No-op when routing is off OR when the call does not target the gateway.
function injectGatewayHeaders(resource: Any, args: Any[]): void {
  if (!targetsGateway(resource)) return;
  const headers = gatewayHeaders();
  if (!Object.keys(headers).length) return;
  try {
    const opts = (args[1] && typeof args[1] === "object" ? args[1] : {}) as Record<string, Any>;
    opts.headers = { ...headers, ...(opts.headers ?? {}) }; // dev-supplied overrides
    args[1] = opts;
  } catch {
    // never break the dev's call
  }
}

function get(obj: Any, name: string, dflt?: Any): Any {
  if (obj == null) return dflt;
  const v = obj[name];
  return v === undefined ? dflt : v;
}

export interface ExtractedUsage {
  model: string;
  provider: string;
  promptTokens: number;
  completionTokens: number;
  /** Prompt-cache READ tokens. */
  cachedTokens: number;
  /** Prompt-cache WRITE/creation tokens. Billed at 1.25x input on Anthropic, so an
   *  agent's most expensive tokens were previously recorded as zero. */
  cachedWriteTokens: number;
  reasoningTokens: number;
}

// --- Usage normalization, by MEANING rather than by vendor -----------------------------
//
// WHY THE VENDOR TABLE HAD TO GO. There was one extractor per provider: OpenAI, Anthropic, the
// Responses API, Ollama, Google, Cohere. Everything else returned null and was recorded as ZERO
// tokens and zero cost — silently, because a zero is a plausible-looking integer. New providers
// ship constantly and every self-hosted server (vLLM, LM Studio, llama.cpp, SGLang, TGI) has its
// own dialect, so the table was always one release behind.
//
// What is stable is not the field NAMES but the CONCEPTS: input, output, cache read, cache write,
// reasoning. Matching on those means `prompt_cache_hit_tokens`, `cache_read_input_tokens`,
// `cached_tokens` and `cachedContentTokenCount` all land in one bucket without any being listed.
//
// This mirrors internal/llmgw/usagenorm.go and the Python SDK deliberately. The gateway is the
// authoritative observer for the verified tier; if an SDK normalized differently, "verified" and
// "self-reported" would be two different numbers for one call — and the boards rank on cost, so
// the divergence would land as a silent bias in a public ranking rather than a visible bug.
// sdk/conformance/usage_pricing.json pins all three against the same expectations.

type Concept =
  | "inputPrompt" // whole-prompt family: cache is a SUBSET
  | "inputFresh" // fresh-input family: cache is ADDITIVE
  | "output"
  | "cacheRead"
  | "cacheWrite"
  | "reasoning"
  | "total";

const MODEL_KEYS = new Set(["model", "modelversion", "modelid", "modelname", "modelslug"]);

function normalizeKey(key: string): string {
  return key.toLowerCase().replace(/[_\-.]/g, "");
}

function isModelKey(key: string): boolean {
  return MODEL_KEYS.has(normalizeKey(key));
}

/**
 * What a response field MEANS, independent of what it is called.
 *
 * Ordered most-specific-first: "cache_read_input_tokens" contains both "cache" and "input" and
 * must read as a cache field, not an input count. Getting that order wrong would make Anthropic's
 * cache read look like its input total.
 */
function classifyUsageKey(key: string): Concept | null {
  const k = key.toLowerCase().replace(/-/g, "_");
  if (k.includes("cach")) {
    if (k.includes("creat") || k.includes("writ")) return "cacheWrite";
    // A MISS is ordinary uncached input, already inside the prompt total that accompanies it
    // (DeepSeek documents prompt == hit + miss), so counting it would double-bill.
    if (k.includes("miss")) return null;
    if (k.includes("read") || k.includes("hit") || k.includes("cached")) return "cacheRead";
    // A bare cache count with no direction: read is the cheaper and therefore conservative
    // reading — overstating a discount is worse than understating it.
    return "cacheRead";
  }
  if (k.includes("reasoning") || k.includes("thought")) return "reasoning";
  if (k.includes("total")) return "total";
  // Output before input: these are unambiguous, and doing them first keeps the input rules from
  // having to exclude them.
  if (k.includes("completion") || k.includes("output") || k.includes("candidates") || k === "eval_count") {
    return "output";
  }
  if (k.includes("prompt")) return "inputPrompt";
  if (k.includes("input")) return "inputFresh";
  return null;
}

type Acc = Partial<Record<Concept, number>> & { found?: boolean; model?: string };

function fieldsOf(node: unknown): Record<string, unknown> | null {
  if (node === null || node === undefined) return null;
  if (typeof node !== "object") return null;
  if (Array.isArray(node)) return null;
  return node as Record<string, unknown>;
}

/**
 * Accumulate every usage-shaped number under `node`, by concept.
 *
 * Takes the MAXIMUM per concept rather than the last value seen: responses repeat counts (a
 * streamed body carries them across frames), and a later zero for a field the provider is not
 * reporting in that frame would erase a real count already found.
 */
function harvestUsage(node: unknown, acc: Acc, depth = 0): void {
  if (depth > 12) return;
  if (Array.isArray(node)) {
    for (const item of node) harvestUsage(item, acc, depth + 1);
    return;
  }
  const fields = fieldsOf(node);
  if (!fields) return;
  for (const key of Object.keys(fields).sort()) {
    const value = fields[key];
    if (isModelKey(key) && typeof value === "string" && value && !acc.model) {
      acc.model = value;
      continue;
    }
    if (typeof value === "number" && Number.isFinite(value)) {
      const concept = classifyUsageKey(key);
      if (concept && value > 0 && value > (acc[concept] ?? 0)) {
        acc[concept] = Math.trunc(value);
        acc.found = true;
      }
      continue;
    }
    harvestUsage(value, acc, depth + 1);
  }
}

const USAGE_CONTAINER_HINTS = ["usage", "tokens", "accounting", "billing"];

/** Collect every non-scalar value whose key satisfies `match`, in deterministic order. */
function findByKey(node: unknown, match: (k: string) => boolean, depth = 0): unknown[] {
  if (depth > 12) return [];
  const out: unknown[] = [];
  if (Array.isArray(node)) {
    for (const item of node) out.push(...findByKey(item, match, depth + 1));
    return out;
  }
  const fields = fieldsOf(node);
  if (!fields) return out;
  for (const key of Object.keys(fields).sort()) {
    const v = fields[key];
    if (v !== null && typeof v === "object") {
      if (match(key)) {
        out.push(v); // the envelope is the unit; do not also descend into it
        continue;
      }
      out.push(...findByKey(v, match, depth + 1));
    }
  }
  return out;
}

function harvestModel(node: unknown, acc: Acc, depth = 0): void {
  if (depth > 12 || acc.model) return;
  if (Array.isArray(node)) {
    for (const item of node) harvestModel(item, acc, depth + 1);
    return;
  }
  const fields = fieldsOf(node);
  if (!fields) return;
  for (const key of Object.keys(fields).sort()) {
    if (isModelKey(key) && typeof fields[key] === "string" && fields[key]) {
      acc.model = fields[key] as string;
      return;
    }
  }
  for (const key of Object.keys(fields).sort()) harvestModel(fields[key], acc, depth + 1);
}

/**
 * Read the CANONICAL usage envelope where one exists, and only scan more widely when there is none.
 *
 * WHY PRIORITY RATHER THAN "take the largest match". Once the walk is general, an unrelated number
 * elsewhere in a response can be read as a token count — a proxy echoing several dialects, or a
 * provider that left a legacy field in place, produces a body carrying both a real `usage` object
 * and a stray `prompt_eval_count`. Keeping the largest value would let whichever number happened
 * to be bigger decide what the developer is charged.
 *
 * `usage` is what OpenAI, Anthropic, Bedrock, Mistral and every OpenAI-wire server fill in. The
 * alternatives are what a provider uses INSTEAD of it, never alongside — so it wins outright.
 */
function harvestPreferringEnvelope(resp: unknown, acc: Acc): void {
  harvestModel(resp, acc);

  // Tier 1: the canonical envelope, wherever it sits (Anthropic's streaming shape nests it inside
  // `message`, so this cannot be a top-level-only lookup).
  const envelopes = findByKey(resp, (k) => normalizeKey(k) === "usage");
  if (envelopes.length) {
    for (const e of envelopes) harvestUsage(e, acc);
    if (acc.found) return;
  }
  // Tier 2: a differently-named accounting container (Google's usageMetadata, Cohere's meta.tokens).
  const containers = findByKey(resp, (k) => {
    const lk = k.toLowerCase();
    return USAGE_CONTAINER_HINTS.some((h) => lk.includes(h));
  });
  if (containers.length) {
    for (const c of containers) harvestUsage(c, acc);
    if (acc.found) return;
  }
  // Tier 3: no envelope at all. Ollama natively puts its counts at the TOP LEVEL, and a local model
  // bills nothing — so a zero there is never contradicted by an invoice, which is exactly why it
  // went unnoticed. Scan everything rather than report it as free.
  harvestUsage(resp, acc);
}

/**
 * A vendor label inferred from DISTINCTIVE container names, not from token field names.
 *
 * Kept because the label is user-visible and feeds pricing: an open-weight model is free when
 * self-hosted and billed when a hosted provider serves it, with the same model id either way. Only
 * markers that genuinely identify one vendor are listed, and this is a LAST-RESORT hint — the
 * caller's own resolution from the client's baseURL wins wherever it has one.
 *
 * A plain `usage` envelope OUTRANKS every marker: a response can carry both, and the envelope the
 * provider actually filled in is the one that decides.
 */
function vendorFromMarkers(resp: unknown): string {
  const fields = fieldsOf(resp) ?? {};
  const keys = new Set(Object.keys(fields).map(normalizeKey));
  if (keys.has("usage")) return "";
  if (keys.has("usagemetadata")) return providers.GOOGLE;
  if (keys.has("promptevalcount") || keys.has("evalcount")) return providers.OLLAMA;
  const meta = fieldsOf(fields["meta"]);
  if (meta && meta["tokens"] !== undefined) return "cohere";
  return "";
}

/**
 * Pull normalized usage from ANY provider response, or null if it carries none.
 *
 * Normalizes onto ONE convention: promptTokens is the total billable input, with cache reads and
 * writes as SUBSETS of it. Providers genuinely disagree and the disagreement is silent, so the rule
 * follows the WORD USED rather than a vendor list:
 *
 *   - a PROMPT-family key names the whole prompt, so cache is already inside it
 *   - an INPUT-family key names fresh input, so cache is billed on top
 *
 * A reported total cross-checks the additive case, so a provider using "input" for a
 * cache-inclusive total is corrected by its own arithmetic instead of being over-counted.
 */
export function extractUsage(resp: Any): ExtractedUsage | null {
  const acc: Acc = {};
  harvestPreferringEnvelope(resp, acc);
  if (!acc.found) return null;

  const cacheRead = acc.cacheRead ?? 0;
  const cacheWrite = acc.cacheWrite ?? 0;
  const completion = acc.output ?? 0;
  const total = acc.total ?? 0;
  const inPrompt = acc.inputPrompt ?? 0;
  const inFresh = acc.inputFresh ?? 0;

  let prompt: number;
  if (inPrompt > 0) {
    prompt = inPrompt;
  } else if (inFresh > 0) {
    prompt = inFresh + cacheRead + cacheWrite;
    if (total > 0 && prompt > total - completion && total - completion >= inFresh) {
      prompt = total - completion;
    }
  } else {
    // No input count, but cache counts present: the cache IS the input we know about.
    prompt = cacheRead + cacheWrite;
  }
  // A cache count larger than the prompt total cannot be a subset of it. Raise the total rather
  // than let pricing clamp the excess away as if it had never been billed.
  prompt = Math.max(prompt, cacheRead + cacheWrite);

  let provider = vendorFromMarkers(resp);
  if (!provider) {
    if (inPrompt > 0) provider = "openai";
    else if (inFresh > 0) provider = "anthropic";
  }

  return {
    model: acc.model ?? "",
    provider,
    promptTokens: Math.trunc(prompt),
    completionTokens: Math.trunc(completion),
    cachedTokens: Math.trunc(cacheRead),
    cachedWriteTokens: Math.trunc(cacheWrite),
    reasoningTokens: Math.trunc(acc.reasoning ?? 0),
  };
}

/** Record usage from a provider response: compute cost, add to the turn
 *  accumulator, and emit a Lens model_call span. Returns the extracted usage (or
 *  null). Also the public manual hook for clients this module doesn't auto-wrap. */
export function recordResponse(resp: Any, o: { provider?: string; latencyMs?: number } = {}): ExtractedUsage | null {
  const info = extractUsage(resp);
  if (info === null) return null;
  const provider = o.provider || info.provider;
  const cost = estimateCost(info.model, {
    // WHO served it, not just what was served: an open-weight model is free on your
    // own hardware and billed when a hosted provider serves it, and the model id is
    // identical either way.
    provider,
    promptTokens: info.promptTokens,
    completionTokens: info.completionTokens,
    cachedTokens: info.cachedTokens,
    cachedWriteTokens: info.cachedWriteTokens,
    reasoningTokens: info.reasoningTokens,
  });
  currentUsage()?.add({
    model: info.model,
    provider,
    promptTokens: info.promptTokens,
    completionTokens: info.completionTokens,
    reasoningTokens: info.reasoningTokens,
    cachedTokens: info.cachedTokens,
    cachedWriteTokens: info.cachedWriteTokens,
    estimatedCost: cost,
    latencyMs: o.latencyMs ?? 0,
  });
  currentSpan().logModelCall({
    provider,
    model: info.model,
    promptTokens: info.promptTokens,
    completionTokens: info.completionTokens,
    totalTokens: info.promptTokens + info.completionTokens,
    estimatedCost: cost,
    latencyMs: o.latencyMs ?? 0,
  });
  return info;
}

/** @internal Wrap `proto[method]` so its resolved return value is recorded.
 *  Idempotent and fully guarded. Exported for tests. */
export function patchPrototype(
  proto: Any,
  method: string,
  provider: string,
  endpoint = "",
): boolean {
  if (proto == null) return false;
  const orig = proto[method];
  if (typeof orig !== "function" || orig._pyyolInstrumented) return false;
  const wrapped = async function (this: Any, ...args: Any[]): Promise<Any> {
    injectGatewayHeaders(this, args);
    // Fingerprint the scaffold from the OUTGOING request: the system prompt, tools and
    // sampling are what the developer wrote, and none of that comes back in the response.
    // Guarded like every other hook — a fingerprinting problem must never be why a
    // developer's model call fails.
    try {
      const req = (args[0] ?? {}) as Record<string, unknown>;
      const fp = fromRequest(req, endpoint);
      currentUsage()?.observeScaffold(fp, fp ? "" : issue(req, endpoint));
    } catch {
      // instrumentation must never break the dev's call
    }
    const start = Date.now();
    const resp = await orig.apply(this, args);
    try {
      // Resolve from the CLIENT's baseURL — the wire format we patched is not the
      // same thing as who actually served the call.
      recordResponse(resp, { provider: resolveCallProvider(this, provider), latencyMs: Date.now() - start });
    } catch {
      // instrumentation must never break the dev's call
    }
    return resp;
  };
  wrapped._pyyolInstrumented = true;
  proto[method] = wrapped;
  PATCHED.push([proto, method, orig]);
  return true;
}

async function tryImport(spec: string): Promise<Any | null> {
  try {
    return await import(spec);
  } catch {
    return null;
  }
}

async function patchOpenAI(): Promise<boolean> {
  let patched = false;
  const chat = await tryImport("openai/resources/chat/completions");
  if (chat?.Completions?.prototype)
    patched =
      patchPrototype(chat.Completions.prototype, "create", "openai", "openai.chat.completions") ||
      patched;
  const responses = await tryImport("openai/resources/responses");
  if (responses?.Responses?.prototype)
    patched =
      patchPrototype(responses.Responses.prototype, "create", "openai", "openai.responses") || patched;
  return patched;
}

async function patchAnthropic(): Promise<boolean> {
  let patched = false;
  const messages = await tryImport("@anthropic-ai/sdk/resources/messages");
  if (messages?.Messages?.prototype)
    patched =
      patchPrototype(messages.Messages.prototype, "create", "anthropic", "anthropic.messages") ||
      patched;
  return patched;
}

/** Auto-capture LLM usage from installed providers. Pass e.g. `["openai"]` to limit
 *  which are patched; default patches all supported providers that are installed.
 *  Returns the list actually instrumented. Safe to call more than once. */
export async function instrument(providers?: string[]): Promise<string[]> {
  const want = new Set(providers ?? ["openai", "anthropic"]);
  const done: string[] = [];
  if (want.has("openai") && (await patchOpenAI())) done.push("openai");
  if (want.has("anthropic") && (await patchAnthropic())) done.push("anthropic");
  return done;
}

/** Restore all patched methods (primarily for tests). */
export function uninstrument(): void {
  while (PATCHED.length) {
    const [proto, method, orig] = PATCHED.pop()!;
    try {
      proto[method] = orig;
    } catch {
      // ignore
    }
  }
}
