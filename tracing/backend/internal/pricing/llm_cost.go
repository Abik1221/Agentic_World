// Package pricing mirrors pyyol-api/src/common/observability/estimate-llm-cost.ts for backfills.
package pricing

import (
	"math"
	"strings"
)

// EstimateLLMCostUSD returns a rough USD estimate from token counts when billed cost was not stored.
func EstimateLLMCostUSD(provider, model string, promptTokens, completionTokens int64) float64 {
	pt := max64(0, promptTokens)
	ct := max64(0, completionTokens)
	if pt == 0 && ct == 0 {
		return 0
	}
	m := strings.ToLower(model)
	p := strings.ToLower(provider)

	inPerM := 0.25
	outPerM := 0.75

	if p == "openai" || strings.Contains(m, "gpt") {
		switch {
		case strings.Contains(m, "gpt-4o-mini"):
			inPerM, outPerM = 0.15, 0.6
		case strings.Contains(m, "gpt-4o"):
			inPerM, outPerM = 2.5, 10
		case strings.Contains(m, "gpt-3.5"):
			inPerM, outPerM = 0.5, 1.5
		}
	} else if p == "anthropic" || strings.Contains(m, "claude") {
		if strings.Contains(m, "haiku") {
			inPerM, outPerM = 0.25, 1.25
		} else {
			inPerM, outPerM = 3, 15
		}
	} else if p == "google" || strings.Contains(m, "gemini") {
		if strings.Contains(m, "flash") {
			inPerM, outPerM = 0.075, 0.3
		} else {
			inPerM, outPerM = 1.25, 5
		}
	}

	usd := (float64(pt)/1_000_000)*inPerM + (float64(ct)/1_000_000)*outPerM
	return math.Round(usd*1_000_000) / 1_000_000
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
