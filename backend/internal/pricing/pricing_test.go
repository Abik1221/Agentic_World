package pricing

import (
	"math"
	"testing"
)

func TestCanonical(t *testing.T) {
	cases := map[string]string{
		"gpt-4o":                                "gpt-4o",
		"gpt-4o-2024-08-06":                     "gpt-4o",
		"gpt-4o-mini":                           "gpt-4o-mini", // specific rule wins over "4o"
		"GPT-4O-MINI":                           "gpt-4o-mini", // case-insensitive
		"gpt-4.1-mini":                          "gpt-4.1-mini",
		"o1-mini":                               "o1-mini",
		"us.anthropic.claude-opus-4-1-20250805": "claude-opus",
		"claude-3-5-sonnet-20241022":            "claude-sonnet",
		"claude-haiku-4-5":                      "claude-haiku",
		"gemini-2.5-flash":                      "gemini-flash",
		"gemini-1.5-pro":                        "gemini-pro",
		"meta-llama/Llama-3.3-70B":              "llama",
		"deepseek-chat":                         "deepseek",
	}
	for raw, want := range cases {
		if got := Canonical(raw); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestUnknownModelUsesFallbackNotZero(t *testing.T) {
	if Canonical("totally-made-up") != "" {
		t.Fatal("expected unknown model to be uncanonical")
	}
	if IsKnown("totally-made-up") {
		t.Fatal("expected unknown model to be not-known")
	}
	got := EstimateCost("totally-made-up", 1_000_000, 1_000_000, 0, 0)
	if math.Abs(got-(0.50+1.50)) > 1e-9 {
		t.Errorf("fallback cost = %v, want %v", got, 2.0)
	}
}

func TestOpusSonnetHaikuDistinct(t *testing.T) {
	opus := EstimateCost("claude-opus-4", 1_000_000, 1_000_000, 0, 0)
	sonnet := EstimateCost("claude-sonnet-4", 1_000_000, 1_000_000, 0, 0)
	haiku := EstimateCost("claude-haiku-4-5", 1_000_000, 1_000_000, 0, 0)
	if !(opus > sonnet && sonnet > haiku) {
		t.Errorf("expected opus > sonnet > haiku, got %v %v %v", opus, sonnet, haiku)
	}
}

func TestCostMathInputOutputSplit(t *testing.T) {
	got := EstimateCost("gpt-4o", 1000, 500, 0, 0)
	want := (1000*2.50 + 500*10.00) / 1_000_000
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

func TestCachedTokensRateAndClamp(t *testing.T) {
	got := EstimateCost("gpt-4o", 1000, 0, 400, 0)
	want := (600*2.50 + 400*1.25) / 1_000_000
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("cached cost = %v, want %v", got, want)
	}
	// cached clamped to prompt
	clamped := EstimateCost("gpt-4o", 100, 0, 500, 0)
	if math.Abs(clamped-(100*1.25)/1_000_000) > 1e-9 {
		t.Errorf("clamped cost = %v", clamped)
	}
}

func TestOpenWeightFreeAndZero(t *testing.T) {
	if EstimateCost("llama-3.3-70b", 1_000_000, 1_000_000, 0, 0) != 0 {
		t.Error("open-weight model should be free")
	}
	if EstimateCost("gpt-4o", 0, 0, 0, 0) != 0 {
		t.Error("zero tokens should be zero cost")
	}
}

func TestVersionStamped(t *testing.T) {
	if Version == "" {
		t.Error("Version must be set")
	}
}
