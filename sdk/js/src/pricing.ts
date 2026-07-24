// Versioned LLM price table + cost estimation (mirrors the Python SDK's pricing.py).
//
// Single, dated source of truth for turning token counts into a USD cost estimate.
// Prices are public list prices in USD per 1,000,000 tokens as of PRICING_VERSION;
// they are estimates for the sandbox/unverified tier (the ranked/verified tier's
// authoritative cost comes from the Pyyol Gateway). When a provider changes prices,
// bump PRICING_VERSION and update the table — never edit silently, so a cost can
// always be traced to the table that produced it.

// Bump whenever any rate below changes. Stamped onto every estimate.
export const PRICING_VERSION = "2026-07-24";

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
  // Open-weight / self-hosted (no per-token bill)
  llama: { input: 0.0, output: 0.0 },
  mistral: { input: 0.0, output: 0.0 },
  qwen: { input: 0.0, output: 0.0 },
  deepseek: { input: 0.27, output: 1.1 },
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

/** Map a raw model string to a canonical table key, or null if unknown. */
export function canonical(model: string): string | null {
  const m = (model ?? "").trim().toLowerCase();
  if (!m) return null;
  if (m in TABLE) return m;
  for (const [needle, key] of RULES) if (m.includes(needle)) return key;
  return null;
}

/** The Rate for a model (falls back to a mid-tier rate for unknown models). */
export function rateFor(model: string): Rate {
  const key = canonical(model);
  return key !== null ? TABLE[key] : FALLBACK;
}

/** True if the model maps to an explicit table entry (not the fallback). */
export function isKnown(model: string): boolean {
  return canonical(model) !== null;
}

export interface CostArgs {
  promptTokens?: number;
  completionTokens?: number;
  cachedTokens?: number;
  reasoningTokens?: number;
}

/**
 * USD cost estimate for one model call. `cachedTokens` are a subset of
 * `promptTokens` billed at the cached-input rate; `reasoningTokens` are output
 * tokens already counted in `completionTokens` (kept for reporting).
 */
export function estimateCost(model: string, a: CostArgs = {}): number {
  const rate = rateFor(model);
  const prompt = Math.max(0, a.promptTokens ?? 0);
  const completion = Math.max(0, a.completionTokens ?? 0);
  const cached = Math.max(0, Math.min(a.cachedTokens ?? 0, prompt));
  const fullInput = prompt - cached;
  const cachedRate = rate.cachedInput ?? rate.input;
  const cost = (fullInput * rate.input + cached * cachedRate + completion * rate.output) / 1_000_000;
  return Math.round(cost * 1e8) / 1e8;
}
