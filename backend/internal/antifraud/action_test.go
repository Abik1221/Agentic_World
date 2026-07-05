package antifraud

import (
	"math"
	"testing"
)

// card values that fall in each third of a 13-card deck.
const (
	lowCard  = 2  // bin 0
	midCard  = 6  // bin 1
	highCard = 12 // bin 2
)

func rep(s MoveSample, n int) []MoveSample {
	out := make([]MoveSample, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, s)
	}
	return out
}

// Independent bids: the full 3×3 grid of bins appears equally often, so the joint
// equals the product of the marginals → mutual information is ~0.
func TestActionMI_IndependentIsZero(t *testing.T) {
	cards := []int{lowCard, midCard, highCard}
	var samples []MoveSample
	for _, a := range cards {
		for _, b := range cards {
			samples = append(samples, rep(MoveSample{CardA: a, CardB: b, Deck: 13}, 5)...)
		}
	}
	if mi := ActionMI(samples); mi > 0.01 {
		t.Fatalf("independent bids should give MI ~0, got %.4f", mi)
	}
}

// Perfectly coordinated bids: B's bin is always equal to A's, and A uses all three
// bins evenly → normalized MI = 1.
func TestActionMI_PerfectDependenceIsOne(t *testing.T) {
	var samples []MoveSample
	for _, c := range []int{lowCard, midCard, highCard} {
		samples = append(samples, rep(MoveSample{CardA: c, CardB: c, Deck: 13}, 12)...)
	}
	if mi := ActionMI(samples); math.Abs(mi-1) > 0.01 {
		t.Fatalf("perfectly dependent bids should give MI ~1, got %.4f", mi)
	}
}

// If one agent always bids the same band there is no signal → MI 0 (conservative).
func TestActionMI_ConstantSideIsZero(t *testing.T) {
	var samples []MoveSample
	for _, b := range []int{lowCard, midCard, highCard} {
		samples = append(samples, rep(MoveSample{CardA: lowCard, CardB: b, Deck: 13}, 12)...)
	}
	if mi := ActionMI(samples); mi != 0 {
		t.Fatalf("constant A should give MI 0, got %.4f", mi)
	}
}

// The gate fires only in the sub-ban concentration band with enough samples.
func TestActionSuspectBand(t *testing.T) {
	// 70% one-sided over 10 games — suspicious but below the 85% ban → in-band.
	inBand := Pair{Games: 10, AWins: 7, BWins: 3}
	if !actionSuspect(inBand, actionMinSamples) {
		t.Fatal("70% one-sided should be in the action-correlation band")
	}
	// Above the ban threshold → handled by IsColluding, not here.
	if actionSuspect(Pair{Games: 10, AWins: 9, BWins: 1}, actionMinSamples) {
		t.Fatal("≥85% should be left to the pairwise ban test")
	}
	// Balanced/competitive → ignored.
	if actionSuspect(Pair{Games: 10, AWins: 5, BWins: 5}, actionMinSamples) {
		t.Fatal("balanced pair should not be flagged")
	}
	// Too few sampled rounds → ignored regardless of results.
	if actionSuspect(inBand, actionMinSamples-1) {
		t.Fatal("below the sample floor should not be flagged")
	}
}
