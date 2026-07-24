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

const PROVIDER_PATH: Record<string, string> = { openai: "/gw/openai/v1", anthropic: "/gw/anthropic" };

/** The baseURL a provider client should point at, or "" if routing is off/unknown. */
export function gatewayBaseUrl(provider: string): string {
  const path = PROVIDER_PATH[provider];
  return gateway.base && path ? gateway.base + path : "";
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
  const name = (client?.constructor?.name ?? "").toLowerCase();
  if (name.includes("openai")) return "openai";
  if (name.includes("anthropic")) return "anthropic";
  // duck-type fallback
  if (client?.chat?.completions) return "openai";
  if (client?.messages) return "anthropic";
  return "";
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
  cachedTokens: number;
  reasoningTokens: number;
}

/** Pull normalized usage from a provider response, or null if it has none.
 *  Handles OpenAI Chat Completions, Anthropic Messages, and the OpenAI Responses
 *  API; duck-typed so a plain object or an SDK object both work. */
export function extractUsage(resp: Any): ExtractedUsage | null {
  const u = get(resp, "usage");
  if (u == null) return null;

  const model = get(resp, "model", "") || "";

  let prompt = get(u, "prompt_tokens");
  let completion = get(u, "completion_tokens");
  const styleOpenAiChat = prompt !== undefined || completion !== undefined;
  if (prompt === undefined) prompt = get(u, "input_tokens", 0);
  if (completion === undefined) completion = get(u, "output_tokens", 0);

  let cached = 0;
  let reasoning = 0;
  const ptd = get(u, "prompt_tokens_details");
  if (ptd != null) cached = get(ptd, "cached_tokens", 0) || 0;
  const ctd = get(u, "completion_tokens_details");
  if (ctd != null) reasoning = get(ctd, "reasoning_tokens", 0) || 0;
  if (!cached) cached = get(u, "cache_read_input_tokens", 0) || 0;

  let provider = "";
  if (styleOpenAiChat) provider = "openai";
  else if (get(u, "input_tokens") !== undefined) provider = "anthropic";

  return {
    model,
    provider,
    promptTokens: Math.trunc(prompt || 0),
    completionTokens: Math.trunc(completion || 0),
    cachedTokens: Math.trunc(cached || 0),
    reasoningTokens: Math.trunc(reasoning || 0),
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
    promptTokens: info.promptTokens,
    completionTokens: info.completionTokens,
    cachedTokens: info.cachedTokens,
    reasoningTokens: info.reasoningTokens,
  });
  currentUsage()?.add({
    model: info.model,
    provider,
    promptTokens: info.promptTokens,
    completionTokens: info.completionTokens,
    reasoningTokens: info.reasoningTokens,
    cachedTokens: info.cachedTokens,
    estimatedCost: cost,
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
export function patchPrototype(proto: Any, method: string, provider: string): boolean {
  if (proto == null) return false;
  const orig = proto[method];
  if (typeof orig !== "function" || orig._pyyolInstrumented) return false;
  const wrapped = async function (this: Any, ...args: Any[]): Promise<Any> {
    injectGatewayHeaders(this, args);
    const start = Date.now();
    const resp = await orig.apply(this, args);
    try {
      recordResponse(resp, { provider, latencyMs: Date.now() - start });
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
  if (chat?.Completions?.prototype) patched = patchPrototype(chat.Completions.prototype, "create", "openai") || patched;
  const responses = await tryImport("openai/resources/responses");
  if (responses?.Responses?.prototype)
    patched = patchPrototype(responses.Responses.prototype, "create", "openai") || patched;
  return patched;
}

async function patchAnthropic(): Promise<boolean> {
  let patched = false;
  const messages = await tryImport("@anthropic-ai/sdk/resources/messages");
  if (messages?.Messages?.prototype)
    patched = patchPrototype(messages.Messages.prototype, "create", "anthropic") || patched;
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
