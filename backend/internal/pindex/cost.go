package pindex

// CostEfficiencyScore maps an agent's VERIFIED cost-per-win (USD, measured by the
// Pyyol Gateway) to a 0..scale sub-score: cheaper-per-win scores higher. At or below
// cheapUSD → full marks; at or above expensiveUSD → 0; linear between. No wins yet or
// no verified cost → 0 (unproven, not "free").
//
// This is the scoring PRIMITIVE for a verified cost-efficiency P-Index dimension. It
// uses ONLY gateway-verified cost, so — unlike self-reported tokens, which the
// Intelligence dimension deliberately excludes — it is un-gameable. Wiring it into the
// composite is a deliberate, config-versioned step to be validated on a live DB first
// (mirroring how the Intelligence dimension was rolled out inactive), so this function
// stands alone until that toggle.
func CostEfficiencyScore(verifiedCostUSD float64, wins int, scale, cheapUSD, expensiveUSD float64) float64 {
	if wins <= 0 || verifiedCostUSD <= 0 || scale <= 0 {
		return 0
	}
	costPerWin := verifiedCostUSD / float64(wins)
	if expensiveUSD <= cheapUSD {
		// Degenerate band: full marks at/below cheap, else nothing.
		if costPerWin <= cheapUSD {
			return scale
		}
		return 0
	}
	frac := 1 - (costPerWin-cheapUSD)/(expensiveUSD-cheapUSD)
	return clamp(frac, 0, 1) * scale
}
