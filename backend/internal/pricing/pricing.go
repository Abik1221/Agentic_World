// Package pricing is the arena's versioned LLM price table + cost estimation.
//
// It is the Go mirror of the SDK's pricing table (sdk/python/pyyol/pricing.py and
// sdk/js/src/pricing.ts) — one dated source of truth for turning token counts into
// a USD cost estimate. The audit flagged the old cost path as a stale heuristic with
// no version and no Opus/Sonnet split; this replaces it. Prices are public list
// prices in USD per 1,000,000 tokens as of Version. They are estimates for the
// sandbox/unverified tier; the ranked/verified tier's authoritative cost will come
// from the Pyyol Gateway (the real provider bill). When a provider changes prices,
// bump Version and update the table — never edit silently, so a cost can always be
// traced to the table that produced it.
package pricing

import (
	"math"
	"strings"
)

// Version is stamped onto every estimate. Bump whenever any rate below changes.
// Keep in sync with the SDK PRICING_VERSION.
const Version = "2026-08-06"

// cacheWriteMultipliers scale a model's input rate to price a prompt-cache WRITE.
//
// A write and a read are separately billed events that move in opposite directions:
// Anthropic surcharges a write to 1.25x input while discounting a read to 0.1x, and
// OpenAI does not bill writes at all. Pricing writes at the read rate — or, as this
// package did, not pricing them at all — understates the expensive half of caching, and
// understates it most for the agents that cache hardest.
//
// This matters more here than in the SDK: the gateway now OBSERVES real
// cache_creation_input_tokens on the verified tier, so without this the platform records
// the tokens and then bills them at zero.
//
// Multipliers rather than absolute rates because that is how providers publish them —
// one ratio per model family — and because a multiplier cannot drift out of step with
// the input rate the way a duplicated number can. Keyed by canonical-key prefix, so it
// rides the same lookup that produced the rate.
var cacheWriteMultipliers = []struct {
	prefix string
	mult   float64
}{
	{"claude-", 1.25}, // Anthropic bills a cache write at 1.25x input
	{"gpt-", 0.0},     // OpenAI prompt caching is automatic; writes are not billed
	{"o1", 0.0},
	{"o3", 0.0},
	{"o4", 0.0},
	{"gemini-", 0.0}, // implicit context caching is free
}

// defaultCacheWriteMultiplier applies to a family with no published cache-write
// behaviour: a write costs what an ordinary input token costs. Not 0.0, which would make
// an unrecognised model's caching silently free — the flattering direction.
const defaultCacheWriteMultiplier = 1.0

// CacheWriteRate is USD per 1,000,000 tokens for writing a prompt into the cache.
func CacheWriteRate(model string) float64 {
	r := rateFor(model)
	key := Canonical(model)
	for _, m := range cacheWriteMultipliers {
		if strings.HasPrefix(key, m.prefix) {
			return r.input * m.mult
		}
	}
	return r.input * defaultCacheWriteMultiplier
}

// rate is USD per 1,000,000 tokens.
type rate struct {
	input       float64
	output      float64
	cachedInput float64 // 0 => same as input
}

// canonical model id -> rate.
var table = map[string]rate{
	// OpenAI
	"gpt-4o":        {2.50, 10.00, 1.25},
	"gpt-4o-mini":   {0.15, 0.60, 0.075},
	"gpt-4.1":       {2.00, 8.00, 0.50},
	"gpt-4.1-mini":  {0.40, 1.60, 0.10},
	"gpt-4.1-nano":  {0.10, 0.40, 0.025},
	"o1":            {15.00, 60.00, 7.50},
	"o1-mini":       {1.10, 4.40, 0.55},
	"o3":            {2.00, 8.00, 0.50},
	"o3-mini":       {1.10, 4.40, 0.55},
	"o4-mini":       {1.10, 4.40, 0.275},
	"gpt-3.5-turbo": {0.50, 1.50, 0},
	// Anthropic (distinct Opus / Sonnet / Haiku)
	"claude-opus":   {15.00, 75.00, 1.50},
	"claude-sonnet": {3.00, 15.00, 0.30},
	"claude-haiku":  {0.80, 4.00, 0.08},
	// Google (Gemini)
	"gemini-flash": {0.15, 0.60, 0.0375},
	"gemini-pro":   {1.25, 5.00, 0.3125},
	// HOSTED open-weight. "Open weight" does not mean "free": Groq bills per token like
	// anyone else, and pricing llama by NAME alone recorded $0 for every Groq-backed agent —
	// on a platform whose whole claim is verified LLM cost. Published Groq rates per 1M.
	"groq-llama-8b":  {0.05, 0.08, 0},
	"groq-llama-70b": {0.59, 0.79, 0},
	// Open-weight / self-hosted (no per-token bill)
	"llama":    {0, 0, 0},
	"mistral":  {0, 0, 0},
	"qwen":     {0, 0, 0},
	"deepseek": {0.27, 1.10, 0},
}

// fallback: mid-tier rate for an unmapped model (never silently $0 unless the model
// is explicitly open-weight above).
var fallback = rate{0.50, 1.50, 0}

// rule maps a substring to a canonical key; ordered, first match wins, most
// specific substrings first.
type rule struct{ needle, key string }

var rules = []rule{
	{"gpt-4o-mini", "gpt-4o-mini"},
	{"gpt-4o", "gpt-4o"},
	{"4o-mini", "gpt-4o-mini"},
	{"4o", "gpt-4o"},
	{"gpt-4.1-nano", "gpt-4.1-nano"},
	{"gpt-4.1-mini", "gpt-4.1-mini"},
	{"gpt-4.1", "gpt-4.1"},
	{"4.1-nano", "gpt-4.1-nano"},
	{"4.1-mini", "gpt-4.1-mini"},
	{"4.1", "gpt-4.1"},
	{"o1-mini", "o1-mini"},
	{"o1", "o1"},
	{"o3-mini", "o3-mini"},
	{"o3", "o3"},
	{"o4-mini", "o4-mini"},
	{"gpt-3.5", "gpt-3.5-turbo"},
	{"3.5-turbo", "gpt-3.5-turbo"},
	{"opus", "claude-opus"},
	{"sonnet", "claude-sonnet"},
	{"haiku", "claude-haiku"},
	{"gemini-1.5-flash", "gemini-flash"},
	{"gemini-2.0-flash", "gemini-flash"},
	{"gemini-2.5-flash", "gemini-flash"},
	{"flash", "gemini-flash"},
	{"gemini-1.5-pro", "gemini-pro"},
	{"gemini-2.5-pro", "gemini-pro"},
	{"gemini", "gemini-pro"},
	{"llama", "llama"},
	{"mistral", "mistral"},
	{"mixtral", "mistral"},
	{"qwen", "qwen"},
	{"deepseek", "deepseek"},
}

// providerRules scope a lookup to who SERVED the model.
//
// An open-weight model is $0 when you run it yourself and very much not $0 when a hosted
// provider serves it — and the model id cannot tell you which, because "llama-3.3-70b" is the
// same string either way. Pricing by name alone would bill self-hosted users for compute they
// never bought, so only an explicitly provider-attributed call gets a hosted rate.
//
// The Python SDK has had this since its own test suite caught the same thing; the Go side was
// never updated, and a shared conformance fixture built from a REAL Groq response is what
// finally surfaced the divergence.
var providerRules = map[string][]rule{
	"groq": {
		{"llama-3.1-8b", "groq-llama-8b"},
		{"llama-3.1-70b", "groq-llama-70b"},
		{"llama-3.3-70b", "groq-llama-70b"},
		{"llama-4", "groq-llama-70b"},
	},
}

// CanonicalFor is Canonical, scoped by the provider that served the model.
func CanonicalFor(model, provider string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	// Provider rules win: they are the only ones that know a hosted bill exists for a model
	// that would otherwise be free.
	for _, a := range providerRules[strings.ToLower(strings.TrimSpace(provider))] {
		if strings.Contains(m, a.needle) {
			return a.key
		}
	}
	return Canonical(model)
}

// EstimateCostFor is EstimateCost, scoped by the provider that served the model. Prefer it
// wherever the provider is known — the gateway always knows it, and it is the only caller
// whose numbers are the platform's authoritative record of spend.
//
// Resolving to the canonical KEY and handing that to EstimateCost keeps ONE implementation of
// the arithmetic (the read/write subset ordering is subtle and must not exist twice).
func EstimateCostFor(provider, model string, promptTokens, completionTokens, cachedTokens, cachedWriteTokens, reasoningTokens int) float64 {
	key := CanonicalFor(model, provider)
	if key == "" {
		key = model
	}
	return EstimateCost(key, promptTokens, completionTokens, cachedTokens, cachedWriteTokens, reasoningTokens)
}

// Canonical maps a raw model string to a canonical table key, or "" if unknown.
func Canonical(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return ""
	}
	if _, ok := table[m]; ok {
		return m
	}
	for _, r := range rules {
		if strings.Contains(m, r.needle) {
			return r.key
		}
	}
	return ""
}

// IsKnown reports whether the model maps to an explicit table entry (not fallback).
func IsKnown(model string) bool { return Canonical(model) != "" }

func rateFor(model string) rate {
	if key := Canonical(model); key != "" {
		return table[key]
	}
	return fallback
}

// EstimateCost returns the USD cost estimate for one model call.
//
// promptTokens is the TOTAL billable input; cachedTokens (reads) and cachedWriteTokens
// (creations) are SUBSETS of it, so the three partition the input into full-rate,
// read-rate and write-rate portions. Normalizing onto that convention is the caller's
// job — providers disagree about whether cache tokens sit inside their reported input
// count, and pricing must not have to know which.
//
// reasoningTokens are output tokens already included in completionTokens (kept for
// reporting, not double-billed).
func EstimateCost(model string, promptTokens, completionTokens, cachedTokens, cachedWriteTokens, reasoningTokens int) float64 {
	r := rateFor(model)
	prompt := maxInt(0, promptTokens)
	completion := maxInt(0, completionTokens)
	// Reads come out first, then writes from what remains, so the two subsets cannot
	// overlap and bill the same token twice.
	cached := clampInt(cachedTokens, 0, prompt)
	cachedWrite := clampInt(cachedWriteTokens, 0, prompt-cached)
	fullInput := prompt - cached - cachedWrite
	cachedRate := r.cachedInput
	if cachedRate == 0 {
		cachedRate = r.input
	}
	cost := (float64(fullInput)*r.input +
		float64(cached)*cachedRate +
		float64(cachedWrite)*CacheWriteRate(model) +
		float64(completion)*r.output) / 1_000_000.0
	// Round to 8 decimals so the value is stable/reproducible.
	return math.Round(cost*1e8) / 1e8
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
