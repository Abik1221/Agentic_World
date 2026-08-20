package antifraud

import (
	"math"
	"math/rand"
	"testing"
)

// TestHonestTownConvergenceIsNotCollusion is the test that decides whether this detector is
// usable at all.
//
// Mafia town play CONVERGES — that is the game working. A detector that flagged agreement
// would accuse the entire town every match, and on a staked table that means holding honest
// developers' money. Kappa measures agreement ABOVE each seat's own voting habits, so a pair
// that both follow the table consensus must score near zero.
func TestHonestTownConvergenceIsNotCollusion(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	var rounds []VotePair
	for i := 0; i < 200; i++ {
		// The table converges on one target most rounds; both seats usually follow it,
		// independently, and sometimes wander.
		consensus := rng.Intn(12)
		a, b := consensus, consensus
		if rng.Float64() < 0.3 {
			a = rng.Intn(12)
		}
		if rng.Float64() < 0.3 {
			b = rng.Intn(12)
		}
		rounds = append(rounds, VotePair{TargetA: a, TargetB: b})
	}
	k := VoteAgreement(rounds)
	t.Logf("honest town followers: kappa %.3f (threshold %.2f)", k, voteKappaThreshold)
	if VoteSuspect(rounds) {
		t.Fatalf("honest convergent town play was flagged as collusion (kappa %.3f); this "+
			"detector would hold the money of every honest table", k)
	}
}

// TestACoordinatedRingIsCaught. Two seats that always vote the same target, whatever it is.
func TestACoordinatedRingIsCaught(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	var rounds []VotePair
	for i := 0; i < 60; i++ {
		target := rng.Intn(12)
		rounds = append(rounds, VotePair{TargetA: target, TargetB: target})
	}
	k := VoteAgreement(rounds)
	t.Logf("coordinated ring: kappa %.3f", k)
	if !VoteSuspect(rounds) {
		t.Fatalf("a perfectly coordinated pair was not flagged (kappa %.3f)", k)
	}
}

// TestIndependentVotersScoreNearZero.
func TestIndependentVotersScoreNearZero(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	var rounds []VotePair
	for i := 0; i < 400; i++ {
		rounds = append(rounds, VotePair{TargetA: rng.Intn(12), TargetB: rng.Intn(12)})
	}
	k := VoteAgreement(rounds)
	t.Logf("independent voters: kappa %.3f", k)
	if math.Abs(k) > 0.15 {
		t.Fatalf("independent voting scored kappa %.3f, want near 0", k)
	}
	if VoteSuspect(rounds) {
		t.Fatal("independent voters were flagged")
	}
}

// TestTwoSeatsWithTheSameHabitAreNotAccused.
//
// The whole reason for chance correction. Both seats always vote seat 3 — not because they
// coordinate but because seat 3 is the loudest player. Raw agreement is 100%; kappa must not
// be, because their marginals already explain it.
func TestTwoSeatsWithTheSameHabitAreNotAccused(t *testing.T) {
	var rounds []VotePair
	for i := 0; i < 50; i++ {
		rounds = append(rounds, VotePair{TargetA: 3, TargetB: 3})
	}
	k := VoteAgreement(rounds)
	t.Logf("both always vote seat 3: raw agreement 100%%, kappa %.3f", k)
	if k != 0 {
		t.Fatalf("kappa %.3f; with degenerate marginals there is no chance-corrected signal "+
			"and the honest answer is 0", k)
	}
	if VoteSuspect(rounds) {
		t.Fatal("a shared habit was reported as coordination")
	}
}

// TestSmallSamplesNeverFire. A single shared vote must not accuse anyone.
func TestSmallSamplesNeverFire(t *testing.T) {
	for n := 0; n < voteMinRounds; n++ {
		rounds := make([]VotePair, n)
		for i := range rounds {
			rounds[i] = VotePair{TargetA: 1, TargetB: 1}
		}
		if VoteSuspect(rounds) {
			t.Fatalf("%d perfectly-agreeing rounds fired; the minimum is %d", n, voteMinRounds)
		}
	}
}

// TestGiftingIsCaughtButGoodNegotiationIsNot — the Monopoly signal.
//
// A developer who consistently wins trades is playing well and must not be held. A developer
// who repeatedly hands over value for nothing is moving money to a confederate.
func TestGiftingIsCaughtButGoodNegotiationIsNot(t *testing.T) {
	rng := rand.New(rand.NewSource(4))

	// A good negotiator: wins most trades, but both sides always give something real.
	var fair []Trade
	for i := 0; i < 40; i++ {
		give := int64(100 + rng.Intn(200))
		get := int64(120 + rng.Intn(220))
		fair = append(fair, Trade{GaveA: give, GaveB: get})
	}
	s, _ := TransferAsymmetry(fair)
	t.Logf("good negotiator: asymmetry %.3f", s)
	if TradeSuspect(fair) {
		t.Fatalf("a consistently good negotiator was flagged (asymmetry %.3f)", s)
	}

	// A gifter: hands over property for almost nothing, repeatedly, one direction.
	var gift []Trade
	for i := 0; i < 20; i++ {
		gift = append(gift, Trade{GaveA: int64(400 + rng.Intn(200)), GaveB: int64(rng.Intn(20))})
	}
	s, toB := TransferAsymmetry(gift)
	t.Logf("gifter: asymmetry %.3f, A is the net giver = %v", s, toB)
	if !TradeSuspect(gift) {
		t.Fatalf("systematic gifting was not flagged (asymmetry %.3f)", s)
	}
	if !toB {
		t.Fatal("the direction of the transfer is backwards")
	}
}

// TestTradeDetectorNeedsAPattern. Two lopsided trades are a negotiation.
func TestTradeDetectorNeedsAPattern(t *testing.T) {
	few := []Trade{{GaveA: 500, GaveB: 0}, {GaveA: 500, GaveB: 0}}
	if TradeSuspect(few) {
		t.Fatal("two trades fired the detector")
	}
	if s, _ := TransferAsymmetry(nil); s != 0 {
		t.Fatal("an empty trade list produced a score")
	}
	if s, _ := TransferAsymmetry([]Trade{{GaveA: 0, GaveB: 0}}); s != 0 {
		t.Fatal("zero-value trades produced a score")
	}
}

// TestCoSeatingAloneIsNotEvidence.
//
// On a small platform the same active developers share tables constantly. Treating that as
// suspicion would flag the most engaged users first, which is both wrong and the worst
// possible thing to do during a beta.
func TestCoSeatingAloneIsNotEvidence(t *testing.T) {
	if !(SeatRing{Agents: []string{"a", "b"}, Tables: ringMinTables}).WorthAnalysing() {
		t.Fatal("a genuine co-seating group was not selected for analysis")
	}
	if (SeatRing{Agents: []string{"a", "b"}, Tables: ringMinTables - 1}).WorthAnalysing() {
		t.Fatal("an infrequent group was selected")
	}
	if (SeatRing{Agents: []string{"a"}, Tables: 100}).WorthAnalysing() {
		t.Fatal("a single agent cannot be a ring")
	}
	// And the name says it: this gates ANALYSIS, not a hold. Nothing here returns a verdict.
}

// TestKappaFromCountsMatchesTheFullComputation. The SQL path and the in-memory path must
// agree, or a pair is judged differently depending on which one ran.
func TestKappaFromCountsMatchesTheFullComputation(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	var rounds []VotePair
	for i := 0; i < 300; i++ {
		a := rng.Intn(6)
		b := a
		if rng.Float64() < 0.4 {
			b = rng.Intn(6)
		}
		rounds = append(rounds, VotePair{TargetA: a, TargetB: b})
	}
	full := VoteAgreement(rounds)

	agree, n := 0, len(rounds)
	ma, mb := map[int]float64{}, map[int]float64{}
	for _, r := range rounds {
		if r.TargetA == r.TargetB {
			agree++
		}
		ma[r.TargetA]++
		mb[r.TargetB]++
	}
	pe := 0.0
	for tgt, a := range ma {
		if b, ok := mb[tgt]; ok {
			pe += (a / float64(n)) * (b / float64(n))
		}
	}
	if got := KappaFromCounts(agree, n, pe); math.Abs(got-full) > 1e-12 {
		t.Fatalf("aggregated kappa %.12f != full %.12f", got, full)
	}
}

func TestClampUnit(t *testing.T) {
	if clampUnit(-0.5) != 0 || clampUnit(1.5) != 1 || clampUnit(0.5) != 0.5 {
		t.Fatal("clampUnit is wrong")
	}
}
