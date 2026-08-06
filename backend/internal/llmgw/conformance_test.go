package llmgw

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/agent-arena/arena/internal/pricing"
)

// Cross-language conformance: token normalization + cost, driven by shared fixtures.
//
// The expectations live in sdk/conformance/usage_pricing.json and are read by this test,
// the Python SDK's test_conformance.py and the JS SDK's conformance.test.ts.
//
// The point is drift. Three implementations of this logic exist — this gateway, the Python
// SDK and the JS SDK — and nothing in any of them forces them to agree; each language's own
// tests would keep passing while the three quietly diverged. Since the boards rank on cost
// efficiency, a divergence would land as a silent bias in a public ranking rather than a
// visible bug: an agent scored cheaper or dearer for choosing a different SDK, or the
// verified tier disagreeing with the self-reported one about the same call.
//
// This side matters most. The gateway is the authoritative observer for the verified tier,
// so if it normalizes differently from the SDKs, "verified" and "self-reported" cost become
// two different numbers for one call — and the verified one is the one we would defend.

type conformanceCase struct {
	Name     string          `json:"name"`
	Why      string          `json:"why"`
	Model    string          `json:"model"`
	Provider string          `json:"provider"`
	Response json.RawMessage `json:"response"`
	Expect   struct {
		PromptTokens      int     `json:"prompt_tokens"`
		CompletionTokens  int     `json:"completion_tokens"`
		CachedReadTokens  int     `json:"cached_read_tokens"`
		CachedWriteTokens int     `json:"cached_write_tokens"`
		ReasoningTokens   int     `json:"reasoning_tokens"`
		CostUSD           float64 `json:"cost_usd"`
		CostBreakdown     string  `json:"cost_breakdown"`
	} `json:"expect"`
}

type conformanceDoc struct {
	PricingVersion string            `json:"pricing_version"`
	Cases          []conformanceCase `json:"cases"`
}

func loadConformance(t *testing.T) conformanceDoc {
	t.Helper()
	path := filepath.Join("..", "..", "..", "sdk", "conformance", "usage_pricing.json")
	b, err := os.ReadFile(path)
	if err != nil {
		// Fails loudly rather than skipping. A conformance suite that quietly finds no
		// fixtures and reports "passed" is the exact failure being guarded against: the
		// three implementations would drift with nothing objecting.
		//
		// The fixtures live in the sibling sdk/ directory because they are shared with the
		// Python and JS SDKs — that is the point. A checkout or CI job that has backend/
		// without sdk/ will land here; the fix is to fetch the whole repo, not to relax this
		// into a skip.
		t.Fatalf("read shared conformance fixtures at %s: %v\n"+
			"These fixtures are shared with the Python and JS SDKs and live in the sibling "+
			"sdk/ directory. Run tests from a full checkout of the repository.", path, err)
	}
	var doc conformanceDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse conformance fixtures: %v", err)
	}
	// A suite that silently finds zero cases would report success while testing nothing.
	if len(doc.Cases) < 7 {
		t.Fatalf("only %d conformance cases loaded, expected at least 7", len(doc.Cases))
	}
	return doc
}

func TestConformancePricingVersionMatchesFixtures(t *testing.T) {
	// The fixture costs were computed from one dated table. If the backend's table moves and
	// the fixtures do not, every cost below is checked against stale rates.
	doc := loadConformance(t)
	if pricing.Version != doc.PricingVersion {
		t.Fatalf("pricing.Version = %q but fixtures were computed for %q — bump both together",
			pricing.Version, doc.PricingVersion)
	}
}

func TestConformanceUsageAndCost(t *testing.T) {
	doc := loadConformance(t)
	for _, c := range doc.Cases {
		t.Run(c.Name, func(t *testing.T) {
			var got Call
			applyUsage(&got, c.Response)

			if got.PromptTokens != c.Expect.PromptTokens ||
				got.CompletionTokens != c.Expect.CompletionTokens ||
				got.CachedReadTokens != c.Expect.CachedReadTokens ||
				got.CachedWriteTokens != c.Expect.CachedWriteTokens ||
				got.ReasoningTokens != c.Expect.ReasoningTokens {
				t.Fatalf("usage mismatch\n got prompt=%d completion=%d read=%d write=%d reasoning=%d\nwant prompt=%d completion=%d read=%d write=%d reasoning=%d\nwhy: %s",
					got.PromptTokens, got.CompletionTokens, got.CachedReadTokens,
					got.CachedWriteTokens, got.ReasoningTokens,
					c.Expect.PromptTokens, c.Expect.CompletionTokens, c.Expect.CachedReadTokens,
					c.Expect.CachedWriteTokens, c.Expect.ReasoningTokens, c.Why)
			}

			// read + write <= prompt, in every case. Violating it means pricing silently
			// clamps the excess to zero, which reads as a cheaper call rather than an error.
			if got.CachedReadTokens+got.CachedWriteTokens > got.PromptTokens {
				t.Fatalf("cache tokens (%d+%d) exceed prompt total %d — pricing would discard "+
					"the excess as if it had never been billed",
					got.CachedReadTokens, got.CachedWriteTokens, got.PromptTokens)
			}

			cost := pricing.EstimateCost(c.Model, got.PromptTokens, got.CompletionTokens,
				got.CachedReadTokens, got.CachedWriteTokens, got.ReasoningTokens)
			if math.Abs(cost-c.Expect.CostUSD) > 1e-9 {
				t.Fatalf("cost = %v, want %v (%s)", cost, c.Expect.CostUSD, c.Expect.CostBreakdown)
			}
		})
	}
}
