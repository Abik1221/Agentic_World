// Versioned LLM price table + cost estimation (mirrors the Python SDK's pricing.py).
//
// Single, dated source of truth for turning token counts into a USD cost estimate.
// Prices are public list prices in USD per 1,000,000 tokens as of PRICING_VERSION;
// they are estimates for the sandbox/unverified tier (the ranked/verified tier's
// authoritative cost comes from the Pyyol Gateway). When a provider changes prices,
// bump PRICING_VERSION and update the table — never edit silently, so a cost can
// always be traced to the table that produced it.

import { isSelfHosted } from "./providers.js";

// Bump whenever any rate below changes. Stamped onto every estimate.
export const PRICING_VERSION = "2026-08-06";

export interface Rate {
  /** USD per 1M input tokens. */
  input: number;
  /** USD per 1M output tokens. */
  output: number;
  /** USD per 1M cached (prompt-cache read) input tokens; defaults to `input`. */
  cachedInput?: number;
}

// Canonical model id -> Rate. Lowercase, provider-agnostic.
const TABLE: Record<string, Rate> = {
  // OpenAI
  "gpt-4o": { input: 2.5, output: 10.0, cachedInput: 1.25 },
  "gpt-4o-mini": { input: 0.15, output: 0.6, cachedInput: 0.075 },
  "gpt-4.1": { input: 2.0, output: 8.0, cachedInput: 0.5 },
  "gpt-4.1-mini": { input: 0.4, output: 1.6, cachedInput: 0.1 },
  "gpt-4.1-nano": { input: 0.1, output: 0.4, cachedInput: 0.025 },
  o1: { input: 15.0, output: 60.0, cachedInput: 7.5 },
  "o1-mini": { input: 1.1, output: 4.4, cachedInput: 0.55 },
  o3: { input: 2.0, output: 8.0, cachedInput: 0.5 },
  "o3-mini": { input: 1.1, output: 4.4, cachedInput: 0.55 },
  "o4-mini": { input: 1.1, output: 4.4, cachedInput: 0.275 },
  "gpt-3.5-turbo": { input: 0.5, output: 1.5 },
  // Anthropic (distinct Opus / Sonnet / Haiku)
  "claude-opus": { input: 15.0, output: 75.0, cachedInput: 1.5 },
  "claude-sonnet": { input: 3.0, output: 15.0, cachedInput: 0.3 },
  "claude-haiku": { input: 0.8, output: 4.0, cachedInput: 0.08 },
  // Google (Gemini)
  "gemini-flash": { input: 0.15, output: 0.6, cachedInput: 0.0375 },
  "gemini-pro": { input: 1.25, output: 5.0, cachedInput: 0.3125 },
  // Open-weight served by a HOSTED provider — there IS a per-token bill.
  //
  // "Open weight" does not mean "free". Groq bills per token like anyone else, and
  // recording $0 for it meant a Groq-backed agent reported no cost at all on a platform
  // that advertises verified LLM cost tracking. (The Python SDK already scoped these by
  // provider; this table did not, so the two disagreed about the same call.)
  "groq-llama-8b": { input: 0.05, output: 0.08 },
  "groq-llama-70b": { input: 0.59, output: 0.79 },
  // Open-weight / self-hosted (no per-token bill)
  llama: { input: 0.0, output: 0.0 },
  mistral: { input: 0.0, output: 0.0 },
  qwen: { input: 0.0, output: 0.0 },
  deepseek: { input: 0.27, output: 1.1 },
};

// Provider-scoped rules, checked BEFORE the name rules. An open-weight model is $0
// when you run it yourself and very much not $0 when a hosted provider serves it — and
// the model id cannot tell you which, since "llama-3.3-70b" is the same string either
// way. Only an explicitly provider-attributed call gets a hosted rate.
const PROVIDER_RULES: Record<string, ReadonlyArray<readonly [string, string]>> = {
  groq: [
    ["llama-3.1-8b", "groq-llama-8b"],
    ["llama-3.1-70b", "groq-llama-70b"],
    ["llama-3.3-70b", "groq-llama-70b"],
    ["llama-4", "groq-llama-70b"],
  ],
};

// Last-resort rate for an unmapped model (never silently $0 unless open-weight).
const FALLBACK: Rate = { input: 0.5, output: 1.5 };

// Ordered [substring, canonical] rules; first match wins, most specific first.
const RULES: ReadonlyArray<readonly [string, string]> = [
  ["gpt-4o-mini", "gpt-4o-mini"],
  ["gpt-4o", "gpt-4o"],
  ["4o-mini", "gpt-4o-mini"],
  ["4o", "gpt-4o"],
  ["gpt-4.1-nano", "gpt-4.1-nano"],
  ["gpt-4.1-mini", "gpt-4.1-mini"],
  ["gpt-4.1", "gpt-4.1"],
  ["4.1-nano", "gpt-4.1-nano"],
  ["4.1-mini", "gpt-4.1-mini"],
  ["4.1", "gpt-4.1"],
  ["o1-mini", "o1-mini"],
  ["o1", "o1"],
  ["o3-mini", "o3-mini"],
  ["o3", "o3"],
  ["o4-mini", "o4-mini"],
  ["gpt-3.5", "gpt-3.5-turbo"],
  ["3.5-turbo", "gpt-3.5-turbo"],
  ["opus", "claude-opus"],
  ["sonnet", "claude-sonnet"],
  ["haiku", "claude-haiku"],
  ["gemini-1.5-flash", "gemini-flash"],
  ["gemini-2.0-flash", "gemini-flash"],
  ["gemini-2.5-flash", "gemini-flash"],
  ["flash", "gemini-flash"],
  ["gemini-1.5-pro", "gemini-pro"],
  ["gemini-2.5-pro", "gemini-pro"],
  ["gemini", "gemini-pro"],
  ["llama", "llama"],
  ["mistral", "mistral"],
  ["mixtral", "mistral"],
  ["qwen", "qwen"],
  ["deepseek", "deepseek"],
];

/** Map a raw model string to a canonical table key, or null if unknown.
 *  `provider` scopes the lookup: provider-specific rules win, because they are the
 *  only ones that know a hosted bill exists for a model that would otherwise be free. */
export function canonical(model: string, provider = ""): string | null {
  const m = (model ?? "").trim().toLowerCase();
  if (!m) return null;
  for (const [needle, key] of PROVIDER_RULES[provider.trim().toLowerCase()] ?? []) {
    if (m.includes(needle)) return key;
  }
  if (m in TABLE) return m;
  for (const [needle, key] of RULES) if (m.includes(needle)) return key;
  return null;
}

// Rate for a model the developer serves themselves. Not a guess and not a fallback —
// there is no per-token bill, so any non-zero number here would be fiction.
const FREE: Rate = { input: 0, output: 0, cachedInput: 0 };

/** The Rate for a model (falls back to a mid-tier rate for unknown models).
 *  `provider` scopes the lookup so a self-hosted model stays at $0. */
export function rateFor(model: string, provider = ""): Rate {
  // Self-hosted first, ahead of every name-based rule. The model id cannot tell you
  // who served it — "llama-3.3-70b" is the same string on Groq's bill and on your own
  // GPU — so without this an Ollama user is charged Groq's rates for electricity they
  // already paid for, and the unknown-model fallback would invent a bill outright.
  if (isSelfHosted(provider.trim().toLowerCase())) return FREE;
  const key = canonical(model, provider);
  return key !== null ? TABLE[key] : FALLBACK;
}

/** True if the model maps to an explicit table entry (not the fallback). */
export function isKnown(model: string): boolean {
  return canonical(model) !== null;
}

// Cache-WRITE multipliers, applied to a model's input rate.
//
// Writing a prompt into a provider's cache is a separately-billed event from reading it
// back, and the two go in OPPOSITE directions: Anthropic surcharges a write to 1.25x
// input and discounts a read to 0.1x, while OpenAI does not bill writes at all. Recording
// only reads therefore does not merely lose a number — it prices the expensive half of
// caching at zero, and does so for the agents that cache hardest.
//
// Expressed as a multiplier rather than a per-model rate because that is how providers
// publish it: one ratio per model family. A multiplier also cannot drift out of step with
// a model's input rate the way a duplicated absolute number can.
const CACHE_WRITE_MULTIPLIER: ReadonlyArray<readonly [string, number]> = [
  ["claude-", 1.25], // Anthropic bills a cache write at 1.25x input
  ["gpt-", 0.0], // OpenAI prompt caching is automatic; writes are not billed
  ["o1", 0.0],
  ["o3", 0.0],
  ["o4", 0.0],
  ["gemini-", 0.0], // implicit context caching is free
];

// Multiplier for a family with no published cache-write behaviour: a write costs what an
// ordinary input token costs. Not 0.0, which would make an unrecognised model's caching
// silently free — the flattering direction.
const DEFAULT_CACHE_WRITE_MULTIPLIER = 1.0;

/** USD per 1M tokens for writing a prompt into the provider's cache. */
export function cacheWriteRate(model: string, provider = ""): number {
  const rate = rateFor(model, provider);
  const key = canonical(model, provider) ?? "";
  for (const [prefix, mult] of CACHE_WRITE_MULTIPLIER) {
    if (key.startsWith(prefix)) return rate.input * mult;
  }
  return rate.input * DEFAULT_CACHE_WRITE_MULTIPLIER;
}

export interface CostArgs {
  promptTokens?: number;
  completionTokens?: number;
  /** Prompt-cache READ tokens (a subset of promptTokens). */
  cachedTokens?: number;
  /** Prompt-cache WRITE/creation tokens (also a subset of promptTokens). */
  cachedWriteTokens?: number;
  reasoningTokens?: number;
  /** WHO served the call. Decides whether there is a bill at all: the same model id
   *  is billed on a hosted provider and free on the developer's own hardware. */
  provider?: string;
}

/**
 * USD cost estimate for one model call.
 *
 * `promptTokens` is the TOTAL billable input, and `cachedTokens` (reads) and
 * `cachedWriteTokens` (creations) are SUBSETS of it — so the three partition the input
 * into full-rate, read-rate and write-rate portions. Normalizing onto that convention is
 * the caller's job (extractUsage does it): providers disagree about whether cache tokens
 * sit inside their reported input count, and pricing must not have to know which.
 *
 * `reasoningTokens` are output tokens already counted in `completionTokens` (kept for
 * reporting).
 */
export function estimateCost(model: string, a: CostArgs = {}): number {
  const rate = rateFor(model, a.provider ?? "");
  const prompt = Math.max(0, a.promptTokens ?? 0);
  const completion = Math.max(0, a.completionTokens ?? 0);
  // Reads are taken out first, then writes from what remains, so the two subsets can
  // never overlap and bill the same token twice.
  const read = Math.max(0, Math.min(a.cachedTokens ?? 0, prompt));
  const write = Math.max(0, Math.min(a.cachedWriteTokens ?? 0, prompt - read));
  const fullInput = prompt - read - write;
  const readRate = rate.cachedInput ?? rate.input;
  const cost =
    (fullInput * rate.input +
      read * readRate +
      write * cacheWriteRate(model, a.provider ?? "") +
      completion * rate.output) /
    1_000_000;
  return Math.round(cost * 1e8) / 1e8;
}
