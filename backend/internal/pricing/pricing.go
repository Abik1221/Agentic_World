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
const Version = "2026-07-24"

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

// EstimateCost returns the USD cost estimate for one model call. cachedTokens are a
// subset of promptTokens billed at the cached-input rate; reasoningTokens are output
// tokens already included in completionTokens (kept for reporting, not double-billed).
func EstimateCost(model string, promptTokens, completionTokens, cachedTokens, reasoningTokens int) float64 {
	r := rateFor(model)
	prompt := maxInt(0, promptTokens)
	completion := maxInt(0, completionTokens)
	cached := clampInt(cachedTokens, 0, prompt)
	fullInput := prompt - cached
	cachedRate := r.cachedInput
	if cachedRate == 0 {
		cachedRate = r.input
	}
	cost := (float64(fullInput)*r.input + float64(cached)*cachedRate + float64(completion)*r.output) / 1_000_000.0
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
