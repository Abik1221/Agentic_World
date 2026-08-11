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
//
// # Why it still stands alone: measured, 2026-08-11
//
// The remaining Phase 4 metrics — cache-hit ratio, cost per decision, cost intervals and
// quality-per-cost — were assessed against the live database and are NOT calibratable yet.
// The blocker is not implementation effort, it is that there is nothing to calibrate on:
//
//	verified gateway cost      88 (match, agent) rows · 670 calls · $53.64 total
//	                           72 non-house agents across 68 developers
//	decisions reporting cache  52  of 7,494,528
//	decisions with any cost    1,654 of 7,494,528
//
// cheapUSD and expensiveUSD are exactly the kind of constant CLAUDE.md forbids picking
// from a synthetic harness, and a cache-hit band fitted to 52 observations would be worse
// than that — it would be fitted to noise while LOOKING like a measurement. On a
// cost-efficiency ranking the failure mode is not a slightly wrong score: an agent that
// batches, caches or retries differently from the lab agents would be ranked against a
// band no real traffic ever informed.
//
// Quality-per-cost carries a second, harder dependency. Its quality term is the Skill
// dimension, and platform-wide only 964 decisions carry a score — because until the fix in
// internal/monopoly and internal/mafia, every decision was logged against the state it
// PRODUCED rather than the one it was chosen from, so the scorer refused almost everything.
// Those numbers are about to change shape entirely. Building quality-per-cost on today's
// skill signal would bake the broken pairing into a published ratio.
//
// The order that unblocks this: let corrected decisions accumulate, confirm the skill
// dimension has a real population, and only then set the cost bands from traffic that
// actually exists. Same discipline as RANKED_INTEGRITY_MIN_PCT, and for the same reason —
// measure first, weight second.
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
