package rating

import "math"

// Verified coverage: how much of a row's play was actually proven, not whether any of it was.
//
// # The inversion this exists to stop
//
// The board's attribution tier was resolved as MIN(attr_rank) — the BEST tier ever seen
// across a group's matches. One gateway-verified call out of ten thousand decisions labelled
// the whole model row "verified", and nothing on the row let a reader tell that apart from a
// row where every decision was proven.
//
// That is not merely imprecise, it is exploitable, and the exploit rewards doing less of the
// thing we want. Cost per win is computed from VERIFIED cost, so an agent that routes 1% of
// its calls through the gateway and makes the rest elsewhere reports 1% of its real spend
// against 100% of its wins — and lands at the top of a cost-efficiency ranking precisely
// BECAUSE it declined to be measured. Publishing the numerator without the denominator turns
// the verified tier from a guarantee into a discount.
//
// So coverage is the number, and the tier is derived from it rather than asserted alongside
// it. A reader who distrusts our threshold can ignore the label and read the fraction.
//
// # Why decisions and not calls
//
// An agent may legitimately make several model calls for one decision — a best-of-N sample, a
// tool loop, a retry. Counting CALLS would let volume manufacture coverage: forty calls for
// one bound decision would read as forty units of proof. Coverage counts DISTINCT DECISIONS
// proven, against decisions actually made, so the denominator is the thing being ranked.

// Coverage thresholds. A row must prove most of its play to be called verified, and prove
// some of it to be called partially verified.
//
// The numbers are deliberately not 100%: a single dropped bookkeeping write, a provider
// timeout on one turn, or an agent that answers one turn from cache would otherwise demote a
// scrupulous developer to the same tier as one who never routed at all. They are also
// deliberately high — the point of the tier is that the number behind it is close to whole.
//
// Both are published alongside the coverage fraction, so the threshold is a summary and never
// the only thing a reader can see.
const (
	// VerifiedCoverageThreshold is the share of decisions that must be proven for a row to
	// claim the verified tier.
	VerifiedCoverageThreshold = 0.90
	// PartialCoverageThreshold is the share below which proven decisions are too sparse to
	// describe the row at all: a handful of bound turns says something about those turns and
	// nothing about the thousands around them.
	PartialCoverageThreshold = 0.10
)

// AttrPartial sits between verified and observed: some decisions were provably LLM-backed but
// not enough of them to characterise the row.
//
// A separate tier rather than rounding down to "observed", because the two are different
// facts and a developer mid-migration deserves to see progress rather than a flat denial.
// Rounding UP to verified is what created the inversion in the first place.
const AttrPartial = "partial"

// CoverageStat is verified coverage for one board row.
type CoverageStat struct {
	// Decisions actually made (the denominator). Zero means the row has no decision log,
	// which is not the same as having made no decisions — see Known.
	Decisions int `json:"decisions"`
	// BoundDecisions is DISTINCT decisions proven LLM-backed by a turn proof.
	BoundDecisions int `json:"bound_decisions"`
	// Coverage is BoundDecisions/Decisions, clamped to [0,1]. Published as the raw fraction
	// so no threshold is load-bearing or hidden.
	Coverage float64 `json:"coverage"`
	// Known is false when there is no denominator to divide by. Distinct from Coverage == 0:
	// "we cannot say" and "nothing was proven" are different claims and only one of them
	// accuses the developer of anything.
	Known bool `json:"coverage_known"`
}

// NewCoverage builds a CoverageStat from raw counts.
//
// BoundDecisions is clamped to Decisions. An agent can legitimately bind a round the decision
// log has no row for — a call made for a turn it never answered, or a bound round that arrived
// before the log was flushed — and reporting coverage above 100% would make the figure look
// broken rather than conservative.
func NewCoverage(decisions, bound int) CoverageStat {
	if decisions < 0 {
		decisions = 0
	}
	if bound < 0 {
		bound = 0
	}
	c := CoverageStat{Decisions: decisions, BoundDecisions: bound}
	if decisions == 0 {
		return c // Known stays false; Coverage stays 0
	}
	c.Known = true
	if bound > decisions {
		bound = decisions
	}
	c.Coverage = float64(bound) / float64(decisions)
	return c
}

// Tier downgrades a raw attribution rank by what coverage actually supports.
//
// `observedRank` is the tier the identification path reached (1 = the gateway saw the
// provider's own response, 2 = the SDK reported it, 3 = the manifest declared it). Coverage
// can only ever LOWER that: proving many decisions does not make a manifest claim into an
// observation, but failing to prove them does stop an observation from being called verified.
//
// A rank-1 row with unknown coverage is reported as observed rather than verified. That is
// the conservative direction, and it is the right one: an unmeasurable denominator is exactly
// the state an agent would engineer to keep the badge while avoiding the audit.
func Tier(observedRank int, cov CoverageStat) string {
	name := attributionName(observedRank)
	if name != AttrVerified {
		return name // coverage cannot promote a self-reported claim
	}
	if !cov.Known {
		return AttrObserved
	}
	switch {
	case cov.Coverage >= VerifiedCoverageThreshold:
		return AttrVerified
	case cov.Coverage >= PartialCoverageThreshold:
		return AttrPartial
	default:
		return AttrObserved
	}
}

// CostPerWinVerified is verified spend per win, and it refuses to answer on thin coverage.
//
// This is the metric the inversion attacked, so the guard lives with it rather than in the
// caller. Dividing partially-observed spend by fully-observed wins produces a number that is
// wrong in a specific direction — too cheap, by exactly the factor of unrouted traffic — and
// which looks entirely plausible on a board. Returning "not available" is more useful than a
// figure that rewards evasion.
//
// ok=false also when wins is zero: cost per win is undefined, not infinite, and an agent that
// has not won anything should be absent from the ranking rather than pinned at the bottom by
// an arbitrary sentinel.
func CostPerWinVerified(verifiedCostUSD float64, wins int, cov CoverageStat) (float64, bool) {
	if wins <= 0 || !cov.Known || cov.Coverage < VerifiedCoverageThreshold {
		return 0, false
	}
	if verifiedCostUSD < 0 || math.IsNaN(verifiedCostUSD) || math.IsInf(verifiedCostUSD, 0) {
		return 0, false
	}
	return verifiedCostUSD / float64(wins), true
}

// CostForRanking picks the USD figure a cost-per-win / cost-per-match column may divide,
// and names the basis it used.
//
// # Both directions of the attack, not just one
//
// The rule this replaces was "verified cost when there is any, self-reported otherwise",
// justified by self-reported cost being gameable — an agent that under-reports its spend
// would top a cost-efficiency column by lying. That reasoning is correct and incomplete. An
// INCOMPLETE verified figure is gameable in the opposite direction and more effectively,
// because it arrives wearing the label a reader trusts most: route 5% of your calls, and the
// gateway honestly reports 5% of your spend against 100% of your wins.
//
// So verified cost is used only when coverage says it represents the row. Below that, the
// self-reported total is preferred DESPITE being self-reported, because for a ratio
// completeness matters more than provenance: a complete number that could be shaded beats an
// honest number that is definitionally too small, and the basis says which one a reader got.
//
// When the only figure available is a thin verified one, the answer is no figure at all.
// A known-incomplete cost ratio is worse than a blank, and blank already means "not measured"
// on this board rather than "zero spend".
func CostForRanking(verifiedUSD, selfReportedUSD float64, cov CoverageStat) (float64, string) {
	verifiedUsable := verifiedUSD > 0 && cov.Known && cov.Coverage >= VerifiedCoverageThreshold
	if verifiedUsable {
		return verifiedUSD, CostVerified
	}
	if selfReportedUSD > 0 {
		return selfReportedUSD, CostSelfReported
	}
	return 0, ""
}
