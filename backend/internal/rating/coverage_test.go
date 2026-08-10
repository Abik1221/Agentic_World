package rating

import (
	"math"
	"testing"
)

func TestCoverageIsBoundOverDecisions(t *testing.T) {
	c := NewCoverage(13, 13)
	if !c.Known || c.Coverage != 1 {
		t.Fatalf("13/13 = %+v, want fully covered", c)
	}
	c = NewCoverage(200, 20)
	if math.Abs(c.Coverage-0.10) > 1e-9 {
		t.Fatalf("20/200 = %v, want 0.10", c.Coverage)
	}
}

func TestUnknownCoverageIsNotZeroCoverage(t *testing.T) {
	// "We cannot say" and "nothing was proven" are different claims, and only one of them
	// accuses the developer of anything. A row with no decision log must not be presented as
	// a row that failed to prove itself.
	none := NewCoverage(0, 0)
	if none.Known {
		t.Fatal("no denominator must report Known=false")
	}
	zero := NewCoverage(100, 0)
	if !zero.Known || zero.Coverage != 0 {
		t.Fatalf("100 decisions and 0 bound = %+v, want known and zero", zero)
	}
}

func TestBoundIsClampedSoCoverageNeverExceedsOne(t *testing.T) {
	// An agent can legitimately bind a round the decision log has no row for — a call for a
	// turn it never answered, or a bound round recorded before the log flushed. Reporting
	// 130% would make the figure look broken rather than conservative.
	c := NewCoverage(10, 13)
	if c.Coverage != 1 {
		t.Fatalf("coverage = %v, want clamped to 1", c.Coverage)
	}
}

func TestOneBoundCallCannotLabelATenThousandDecisionRowVerified(t *testing.T) {
	// THE inversion. MIN(attr_rank) meant a single gateway-verified call marked an entire
	// model row "verified", and nothing on the row distinguished it from a row where every
	// decision was proven.
	cov := NewCoverage(10_000, 1)
	if got := Tier(1, cov); got != AttrObserved {
		t.Fatalf("tier = %q, want %q — one proven decision in ten thousand cannot describe the row",
			got, AttrObserved)
	}
}

func TestFullCoverageEarnsVerified(t *testing.T) {
	// The live end-to-end run: 13 of 13 decisions bound.
	if got := Tier(1, NewCoverage(13, 13)); got != AttrVerified {
		t.Fatalf("tier = %q, want %q", got, AttrVerified)
	}
}

func TestPartialIsItsOwnTierRatherThanRoundingEitherWay(t *testing.T) {
	// Rounding UP created the inversion. Rounding DOWN would tell a developer mid-migration
	// that half their routed traffic counted for nothing, which is both false and
	// discouraging. So it is a third tier.
	got := Tier(1, NewCoverage(100, 50))
	if got != AttrPartial {
		t.Fatalf("50%% coverage tier = %q, want %q", got, AttrPartial)
	}
}

func TestCoverageCannotPromoteASelfReportedClaim(t *testing.T) {
	// Proving many decisions does not turn a manifest string into an observation. Coverage is
	// only ever allowed to lower a tier, never to raise one — otherwise an agent could earn
	// "verified" for the model it merely claimed to be running.
	full := NewCoverage(500, 500)
	if got := Tier(2, full); got != AttrObserved {
		t.Fatalf("observed row with full coverage = %q, want %q", got, AttrObserved)
	}
	if got := Tier(3, full); got != AttrDeclared {
		t.Fatalf("declared row with full coverage = %q, want %q", got, AttrDeclared)
	}
}

func TestUnknownCoverageIsNotTreatedAsVerified(t *testing.T) {
	// An unmeasurable denominator is exactly the state an agent would engineer to keep the
	// badge while avoiding the audit, so it must not pass.
	if got := Tier(1, NewCoverage(0, 0)); got != AttrObserved {
		t.Fatalf("tier = %q, want %q when there is no denominator", got, AttrObserved)
	}
}

func TestCostPerWinRefusesThinCoverage(t *testing.T) {
	// The metric the inversion attacked. An agent routing 1% of its calls reports 1% of its
	// spend against 100% of its wins and tops a cost-efficiency board BECAUSE it declined to
	// be measured. Refusing to compute is more useful than a plausible wrong number.
	if _, ok := CostPerWinVerified(0.50, 10, NewCoverage(1000, 10)); ok {
		t.Fatal("cost per win must not be reported at 1% coverage")
	}
	got, ok := CostPerWinVerified(0.50, 10, NewCoverage(1000, 1000))
	if !ok || math.Abs(got-0.05) > 1e-9 {
		t.Fatalf("cost per win = %v (ok=%v), want 0.05", got, ok)
	}
}

func TestThinCoverageWouldHaveLookedCheapest(t *testing.T) {
	// Demonstrates the size of the distortion rather than only asserting the guard fires.
	// Two agents with identical real spend and identical wins: one routes everything, the
	// other routes a twentieth. Unguarded, the evasive one reports 5% of the cost.
	const realSpend, wins = 1.00, 10
	honest := NewCoverage(1000, 1000)
	evasive := NewCoverage(1000, 50)

	honestCost, ok := CostPerWinVerified(realSpend, wins, honest)
	if !ok {
		t.Fatal("full coverage must be reportable")
	}
	// What the evasive agent's verified cost would be: only the routed slice is observed.
	evasiveObserved := realSpend * evasive.Coverage
	if _, ok := CostPerWinVerified(evasiveObserved, wins, evasive); ok {
		t.Fatal("5% coverage must not be reportable")
	}
	if evasiveObserved/float64(wins) >= honestCost {
		t.Fatal("premise wrong: the evasive agent should look cheaper, which is the whole problem")
	}
}

func TestThresholdsAreOrderedAndInRange(t *testing.T) {
	// A guard on the constants themselves: swapping them would silently invert the tiers.
	if !(PartialCoverageThreshold > 0 && PartialCoverageThreshold < VerifiedCoverageThreshold &&
		VerifiedCoverageThreshold < 1) {
		t.Fatalf("thresholds out of order: partial=%v verified=%v",
			PartialCoverageThreshold, VerifiedCoverageThreshold)
	}
}
