package modelboard

import (
	"fmt"
	"math"
	"testing"
)

// These tests recover KNOWN parameters from synthetic data rather than asserting that the fit
// runs. A ranking that merely executes is worthless; the question is whether it returns the
// truth when the truth is known, and whether it refuses to when the data cannot support one.

// gen builds deterministic comparisons where model A beats B in a fixed proportion.
//
// Deterministic counts rather than sampled outcomes: the estimator is being tested, not the
// PRNG, and exact proportions make the expected theta analytic.
func gen(matchPrefix, ma, mb, sa, sb string, wins, losses, draws int) []Comparison {
	var out []Comparison
	add := func(o Outcome, n int) {
		for i := 0; i < n; i++ {
			out = append(out, Comparison{
				MatchID: fmt.Sprintf("%s_%s_%d_%d", matchPrefix, ma, o, i),
				ModelA:  ma, ModelB: mb, StratumA: sa, StratumB: sb,
				Outcome: o, Weight: 1,
			})
		}
	}
	add(Win, wins)
	add(Loss, losses)
	add(Draw, draws)
	return out
}

func ratingOf(t *testing.T, f Fit, model string) Rating {
	t.Helper()
	for _, r := range f.Ratings {
		if r.Model == model {
			return r
		}
	}
	t.Fatalf("model %q absent from the fit", model)
	return Rating{}
}

func fastConfig() Config {
	c := DefaultConfig()
	// Enough replicates for a real percentile interval, few enough to keep the suite quick.
	c.BootstrapReplicates = 200
	c.MinComparisons = 10
	// The FIT budget matters as much as the replicate count, because every replicate refits.
	// Inheriting the production MaxIter/Tol meant up to 2000 Newton iterations × 200
	// replicates × each Estimate call, and under -race that put this package at 22 MINUTES —
	// past `go test`'s 10-minute default, so CI saw "panic: test timed out" rather than a
	// result. These tests assert ORDER, DETERMINISM and interval monotonicity; none of them
	// asserts numerical precision to 1e-7, and the determinism property is independent of how
	// many iterations the fit takes. Production keeps the tight budget.
	c.MaxIter = 200
	c.Tol = 1e-5
	return c
}

func TestRecoversTheStrongerModel(t *testing.T) {
	// A beats B 70/30 with no draws. With the ridge penalty the recovered gap is shrunk toward
	// zero, so the assertion is on the ORDER and the sign, which is what a board publishes.
	cmp := gen("m", "A", "B", "s1", "s2", 70, 30, 0)
	f := Estimate(cmp, fastConfig())
	a, b := ratingOf(t, f, "A"), ratingOf(t, f, "B")
	if a.Theta <= b.Theta {
		t.Fatalf("A (70%% winner) theta=%v must exceed B theta=%v", a.Theta, b.Theta)
	}
	if a.Rank != 1 {
		t.Errorf("A rank = %d, want 1", a.Rank)
	}
	if !f.Converged {
		t.Errorf("fit did not converge in %d iterations", f.Iterations)
	}
}

func TestEqualRecordsProduceEqualStrength(t *testing.T) {
	// The null case. If a 50/50 record produced any separation the board would invent an
	// ordering out of nothing, which is the most damaging possible failure.
	f := Estimate(gen("m", "A", "B", "s1", "s2", 50, 50, 0), fastConfig())
	a, b := ratingOf(t, f, "A"), ratingOf(t, f, "B")
	if math.Abs(a.Theta-b.Theta) > 1e-6 {
		t.Fatalf("equal records gave theta %v vs %v", a.Theta, b.Theta)
	}
}

func TestTiesAreInformationNotNoise(t *testing.T) {
	// Two models that DRAW constantly are equally strong, and the tie parameter should absorb
	// the draws rather than the strengths being pushed apart. Dropping draws entirely (the
	// common shortcut) would leave this pair with almost no evidence at all.
	f := Estimate(gen("m", "A", "B", "s1", "s2", 10, 10, 200), fastConfig())
	a, b := ratingOf(t, f, "A"), ratingOf(t, f, "B")
	if math.Abs(a.Theta-b.Theta) > 1e-6 {
		t.Errorf("draw-heavy equal pair separated: %v vs %v", a.Theta, b.Theta)
	}
	// nu large means "these games are not separating these models", which is itself a finding.
	if f.Nu < 1.0 {
		t.Errorf("nu = %v, want > 1 when draws dominate", f.Nu)
	}
	if a.Draws != 200 || b.Draws != 200 {
		t.Errorf("draws not tallied: %d/%d", a.Draws, b.Draws)
	}
}

func TestDavidsonProbabilitiesAreAValidDistribution(t *testing.T) {
	// The likelihood is only a likelihood if the three outcomes sum to one, at every gap and
	// every tie level. A subtle error here would bias every rating and never announce itself.
	for _, d := range []float64{-8, -1, -0.1, 0, 0.1, 1, 8} {
		for _, nu := range []float64{0, 0.5, 1, 3} {
			w, l, dr := probs(d, nu)
			if s := w + l + dr; math.Abs(s-1) > 1e-12 {
				t.Errorf("d=%v nu=%v: probabilities sum to %v", d, nu, s)
			}
			if w < 0 || l < 0 || dr < 0 {
				t.Errorf("d=%v nu=%v: negative probability %v/%v/%v", d, nu, w, l, dr)
			}
		}
	}
	// Symmetry: reversing the gap must swap win and loss exactly.
	w1, l1, d1 := probs(1.3, 0.7)
	w2, l2, d2 := probs(-1.3, 0.7)
	if math.Abs(w1-l2) > 1e-12 || math.Abs(l1-w2) > 1e-12 || math.Abs(d1-d2) > 1e-12 {
		t.Error("Davidson probabilities are not symmetric under d -> -d")
	}
	// nu = 0 must recover plain Bradley-Terry: no draws, logistic win probability.
	w, _, dr := probs(2.0, 0)
	if dr != 0 {
		t.Errorf("nu=0 gave draw probability %v, want 0", dr)
	}
	if want := 1 / (1 + math.Exp(-2.0)); math.Abs(w-want) > 1e-12 {
		t.Errorf("nu=0 win probability %v, want logistic %v", w, want)
	}
}

func TestExtremeGapsDoNotOverflow(t *testing.T) {
	// A dominant model must saturate, not produce NaN. An arena that prints NaN for its best
	// model is broken in the most visible possible place.
	for _, d := range []float64{500, -500, 1e6} {
		w, l, dr := probs(d, 1)
		if math.IsNaN(w) || math.IsNaN(l) || math.IsNaN(dr) {
			t.Fatalf("d=%v produced NaN: %v/%v/%v", d, w, l, dr)
		}
		if s := w + l + dr; math.Abs(s-1) > 1e-9 {
			t.Errorf("d=%v: sum %v", d, s)
		}
	}
}

// THE test this package exists for.
//
// A weak model in the hands of a strong developer posts a BETTER raw record than a strong model
// in the hands of a weak one. A win-rate table crowns the weak model; this estimator must not.
//
// The fixture is built around a common reference opponent so the confound is real rather than
// asserted. My first attempt at it was arithmetically incoherent — the "weak" model finished
// 75-125 and the premise never held — so the premise is now checked explicitly before the
// conclusion, and the numbers are laid out here to be read:
//
//	weak   @ dev_good  vs ref   60W-40L   -> weak raw   = 60%
//	strong @ dev_bad   vs ref   30W-70L
//	strong @ dev_good  vs ref   75W-25L   -> strong raw = 52.5%   (WORSE than weak)
//
// Within dev_good, where the harness is held constant, strong wins 75% against the same opponent
// that weak beats 60% of the time. That within-stratum contrast is the only real evidence about
// the models, and recovering it is the entire job.
func TestHarnessIsConditionedOutSoTheWeakModelDoesNotWin(t *testing.T) {
	var cmp []Comparison
	cmp = append(cmp, gen("w", "weak", "ref", "dev_good/s1", "dev_ref/s0", 60, 40, 0)...)
	cmp = append(cmp, gen("sb", "strong", "ref", "dev_bad/s2", "dev_ref/s0", 30, 70, 0)...)
	cmp = append(cmp, gen("sg", "strong", "ref", "dev_good/s1", "dev_ref/s0", 75, 25, 0)...)

	f := Estimate(cmp, fastConfig())
	strong, weak := ratingOf(t, f, "strong"), ratingOf(t, f, "weak")

	// The premise, checked rather than assumed: the weak model really does have the better raw
	// record. Without this the test could pass on data where there was no confound to remove.
	weakRate := float64(weak.Wins) / float64(weak.Wins+weak.Losses)
	strongRate := float64(strong.Wins) / float64(strong.Wins+strong.Losses)
	if weakRate <= strongRate {
		t.Fatalf("premise broken: weak %.3f is not above strong %.3f, so there is no confound "+
			"for the estimator to remove", weakRate, strongRate)
	}

	if strong.Theta <= weak.Theta {
		t.Fatalf("the weak model outranked the strong one (theta %v vs %v) despite losing the "+
			"within-harness comparison — the harness was not conditioned out, which is the only "+
			"thing this estimator is for", weak.Theta, strong.Theta)
	}
	if strong.Rank != 1 {
		t.Errorf("strong model rank = %d, want 1", strong.Rank)
	}
}

func TestSeparabilityFlagsAModelOnlyOneHarnessEverRan(t *testing.T) {
	// A model run by exactly one developer on one scaffold is not distinguishable from that
	// developer, however many matches it played. The rating is still produced — refusing to show
	// it would hide a real competitor — but separability says what it is worth.
	var cmp []Comparison
	// "solo" only ever appears under one stratum, which runs nothing else.
	cmp = append(cmp, gen("s", "solo", "common", "dev_solo/s", "dev_a/s", 40, 10, 0)...)
	// "common" is run by a second stratum that ALSO runs a third model, so it bridges.
	cmp = append(cmp, gen("c", "common", "third", "dev_a/s", "dev_b/s", 25, 25, 0)...)
	cmp = append(cmp, gen("c2", "third", "common", "dev_a/s", "dev_b/s", 20, 20, 0)...)

	f := Estimate(cmp, fastConfig())
	solo := ratingOf(t, f, "solo")
	common := ratingOf(t, f, "common")

	if solo.Harnesses != 1 {
		t.Errorf("solo harnesses = %d, want 1", solo.Harnesses)
	}
	if solo.Separability != 0 {
		t.Errorf("solo separability = %v, want 0 — its stratum never ran another model, so "+
			"nothing separates the model from the developer", solo.Separability)
	}
	if common.Separability <= 0 {
		t.Errorf("common separability = %v, want > 0 — it shares a stratum with another model",
			common.Separability)
	}
}

func TestThinEvidenceIsMarkedProvisionalAndShrunk(t *testing.T) {
	// A 3-0 record is not evidence of dominance. Unregularized Bradley-Terry sends an unbeaten
	// model's strength to infinity; the ridge prior keeps it finite and the flag keeps a reader
	// from mistaking it for a measurement.
	var cmp []Comparison
	cmp = append(cmp, gen("t", "unbeaten", "veteran", "d1/s", "d2/s", 3, 0, 0)...)
	cmp = append(cmp, gen("v", "veteran", "other", "d2/s", "d3/s", 60, 60, 0)...)

	f := Estimate(cmp, fastConfig())
	u := ratingOf(t, f, "unbeaten")
	if !u.Provisional {
		t.Errorf("a 3-0 model with %d comparisons was not marked provisional", u.Comparisons)
	}
	if math.IsInf(u.Theta, 0) || math.Abs(u.Theta) > 5 {
		t.Errorf("unbeaten theta = %v, want finite and shrunk toward the mean", u.Theta)
	}
}

func TestRankingUsesTheLowerBoundNotThePointEstimate(t *testing.T) {
	// A thinly-observed model with a flattering record must not outrank a heavily-observed one
	// on a point estimate its interval cannot support. Ranking on the point estimate
	// systematically promotes the least-measured models, which is the opposite of the job.
	var cmp []Comparison
	cmp = append(cmp, gen("thin", "lucky", "anchor", "d1/s", "d0/s", 8, 1, 0)...)
	cmp = append(cmp, gen("thick", "proven", "anchor", "d2/s", "d0/s", 300, 200, 0)...)
	cmp = append(cmp, gen("bridge", "proven", "lucky", "d2/s", "d1/s", 30, 30, 0)...)

	f := Estimate(cmp, fastConfig())
	lucky, proven := ratingOf(t, f, "lucky"), ratingOf(t, f, "proven")
	if lucky.EloHigh-lucky.EloLow <= proven.EloHigh-proven.EloLow {
		t.Fatalf("premise broken: the thin model's interval (%v) is not wider than the thick "+
			"one's (%v)", lucky.EloHigh-lucky.EloLow, proven.EloHigh-proven.EloLow)
	}
	// The ordering is by lower bound, so verify the invariant directly on the sorted output.
	for i := 1; i < len(f.Ratings); i++ {
		if f.Ratings[i-1].EloLow < f.Ratings[i].EloLow {
			t.Fatalf("ratings are not ordered by lower bound at %d: %v then %v",
				i, f.Ratings[i-1].EloLow, f.Ratings[i].EloLow)
		}
	}
}

func TestIntervalsAreNarrowerWithMoreEvidence(t *testing.T) {
	// The basic property that makes an interval meaningful. If evidence did not narrow it, the
	// interval would be decoration.
	small := Estimate(gen("s", "A", "B", "d1/s", "d2/s", 12, 8, 0), fastConfig())
	large := Estimate(gen("l", "A", "B", "d1/s", "d2/s", 600, 400, 0), fastConfig())
	ws := ratingOf(t, small, "A").EloHigh - ratingOf(t, small, "A").EloLow
	wl := ratingOf(t, large, "A").EloHigh - ratingOf(t, large, "A").EloLow
	if wl >= ws {
		t.Fatalf("interval did not narrow with 50x the evidence: %v -> %v", ws, wl)
	}
}

func TestBootstrapClustersOnMatchesNotComparisons(t *testing.T) {
	// An N-player match yields several correlated comparisons. Resampling them independently would
	// treat one match as several and shrink every interval by roughly the square root of the seats
	// per match — a board that overstates its certainty in a way no reader can see.
	//
	// The clusters must be HETEROGENEOUS for this to test anything. My first version gave every
	// table the same 6W-4L composition, so resampling tables had almost no variance and the
	// clustered interval came out NARROWER — the test failed, but had I flipped the assertion it
	// would have "passed" while asserting the opposite of the truth. Real tables vary: a strong
	// seat sweeps one table and is swept at another, and that between-cluster spread is exactly
	// what clustering is supposed to preserve.
	var spread, clustered []Comparison
	const tables, seats = 12, 10
	for tbl := 0; tbl < tables; tbl++ {
		// Alternate whole tables between sweeps and losses: maximum between-cluster variance,
		// identical overall record.
		o := Win
		if tbl%2 == 1 {
			o = Loss
		}
		for seat := 0; seat < seats; seat++ {
			i := tbl*seats + seat
			spread = append(spread, Comparison{
				MatchID: fmt.Sprintf("distinct_%d", i),
				ModelA:  "A", ModelB: "B", StratumA: "d1/s", StratumB: "d2/s", Outcome: o, Weight: 1,
			})
			clustered = append(clustered, Comparison{
				MatchID: fmt.Sprintf("table_%d", tbl),
				ModelA:  "A", ModelB: "B", StratumA: "d1/s", StratumB: "d2/s", Outcome: o, Weight: 1,
			})
		}
	}
	ws := Estimate(spread, fastConfig())
	wc := Estimate(clustered, fastConfig())
	sw := ratingOf(t, ws, "A").EloHigh - ratingOf(t, ws, "A").EloLow
	cw := ratingOf(t, wc, "A").EloHigh - ratingOf(t, wc, "A").EloLow
	if cw <= sw {
		t.Fatalf("clustering did not widen the interval (%v clustered vs %v independent) — "+
			"correlated comparisons are being counted as independent evidence", cw, sw)
	}
}

func TestRankStabilityDistinguishesAConfidentOrderingFromACoinFlip(t *testing.T) {
	// A rank held in 60% of replicates and one held in 99% are different claims. Printing both
	// as an integer position hides exactly the difference a reader needs.
	clear := Estimate(gen("c", "A", "B", "d1/s", "d2/s", 900, 100, 0), fastConfig())
	toss := Estimate(gen("t", "A", "B", "d1/s", "d2/s", 26, 24, 0), fastConfig())
	cs := ratingOf(t, clear, "A").RankStability
	ts := ratingOf(t, toss, "A").RankStability
	if cs < 0.95 {
		t.Errorf("a 9:1 record gave rank stability %v, want >= 0.95", cs)
	}
	if ts >= cs {
		t.Errorf("a near-coin-flip gave stability %v, not below the decisive case's %v", ts, cs)
	}
}

func TestSelfComparisonsCarryNoInformation(t *testing.T) {
	// Two agents running the SAME model tell you nothing about that model's strength — the theta
	// terms cancel — so including them would fit harness noise into a model rating. Dropped, and
	// the drop is reported rather than silent.
	cmp := append(gen("self", "A", "A", "d1/s", "d2/s", 50, 50, 0),
		gen("real", "A", "B", "d1/s", "d2/s", 30, 20, 0)...)
	f := Estimate(cmp, fastConfig())
	if f.Excluded["same_model"] != 100 {
		t.Errorf("excluded same_model = %d, want 100", f.Excluded["same_model"])
	}
	if f.Comparisons != 50 {
		t.Errorf("fit used %d comparisons, want the 50 informative ones", f.Comparisons)
	}
}

func TestUnattributedComparisonsAreRefused(t *testing.T) {
	// A comparison with no model on one side cannot inform a model board. Admitting it under an
	// empty-string key would create a phantom "" model and rank it.
	cmp := []Comparison{
		{MatchID: "m1", ModelA: "A", ModelB: "", StratumA: "d1/s", Outcome: Win, Weight: 1},
		{MatchID: "m2", ModelA: "A", ModelB: "B", StratumA: "d1/s", StratumB: "d2/s", Outcome: Win, Weight: 1},
	}
	f := Estimate(cmp, fastConfig())
	if f.Excluded["unattributed"] != 1 {
		t.Errorf("excluded unattributed = %d, want 1", f.Excluded["unattributed"])
	}
	for _, r := range f.Ratings {
		if r.Model == "" {
			t.Error(`a phantom "" model was ranked`)
		}
	}
}

func TestFitIsReproducible(t *testing.T) {
	// A published interval that cannot be recomputed is not a published interval. Same data and
	// same seed must give bit-identical output, which requires deterministic parameter ordering
	// and a PRNG that does not depend on the Go release.
	cmp := append(gen("a", "A", "B", "d1/s", "d2/s", 40, 30, 10),
		gen("b", "B", "C", "d2/s", "d3/s", 25, 25, 5)...)
	f1 := Estimate(cmp, fastConfig())
	f2 := Estimate(cmp, fastConfig())
	if len(f1.Ratings) != len(f2.Ratings) {
		t.Fatalf("different rating counts: %d vs %d", len(f1.Ratings), len(f2.Ratings))
	}
	for i := range f1.Ratings {
		a, b := f1.Ratings[i], f2.Ratings[i]
		if a.Model != b.Model || a.Theta != b.Theta || a.EloLow != b.EloLow || a.EloHigh != b.EloHigh {
			t.Fatalf("run-to-run drift at %d: %+v vs %+v", i, a, b)
		}
	}
	if f1.Nu != f2.Nu {
		t.Errorf("nu drifted: %v vs %v", f1.Nu, f2.Nu)
	}
}

func TestEloScaleMatchesTheStandardCorrespondence(t *testing.T) {
	// 400 points per factor of 10 in odds is the definition of the Elo scale, and it is what
	// makes our numbers comparable in spirit to the boards readers already know.
	if got := ToElo(0); got != eloAnchor {
		t.Errorf("ToElo(0) = %v, want the anchor %v", got, eloAnchor)
	}
	// A gap of ln(10) log-odds is 10:1 odds, which must be exactly 400 Elo points.
	if got := ToElo(math.Ln10) - ToElo(0); math.Abs(got-400) > 1e-9 {
		t.Errorf("ln(10) log-odds mapped to %v Elo, want 400", got)
	}
}

func TestEmptyInputIsNotAnError(t *testing.T) {
	// A new season has no matches. That must produce an empty board, not a panic and not a
	// fabricated ranking.
	f := Estimate(nil, fastConfig())
	if len(f.Ratings) != 0 || f.Comparisons != 0 {
		t.Fatalf("empty input produced %d ratings", len(f.Ratings))
	}
}

// Reproducibility must survive PARALLELISM, not just repetition.
//
// The bootstrap runs replicates concurrently. With a shared RNG stream the draws a replicate
// received would depend on which goroutine reached it first, so identical data and seed would give
// different intervals on every run — and an interval that cannot be recomputed is not evidence.
// Each replicate therefore derives its own seed from (Seed, rep).
//
// Run under -race this also asserts the workers do not touch shared state.
func TestParallelBootstrapIsStillDeterministic(t *testing.T) {
	cmp := append(gen("a", "A", "B", "d1/s", "d2/s", 40, 30, 10),
		gen("b", "B", "C", "d2/s", "d3/s", 25, 25, 5)...)
	cmp = append(cmp, gen("c", "C", "A", "d3/s", "d1/s", 15, 20, 8)...)
	cfg := fastConfig()

	first := Estimate(cmp, cfg)
	// Several repeats: a scheduling-dependent bug is intermittent by nature, so one match proves
	// little.
	for run := 0; run < 4; run++ {
		again := Estimate(cmp, cfg)
		if len(again.Ratings) != len(first.Ratings) {
			t.Fatalf("run %d: %d ratings vs %d", run, len(again.Ratings), len(first.Ratings))
		}
		for i := range first.Ratings {
			a, b := first.Ratings[i], again.Ratings[i]
			if a.Model != b.Model {
				t.Fatalf("run %d: order changed at %d: %s vs %s", run, i, a.Model, b.Model)
			}
			if a.EloLow != b.EloLow || a.EloHigh != b.EloHigh {
				t.Fatalf("run %d: %s interval drifted [%v,%v] vs [%v,%v] — the bootstrap is "+
					"scheduling-dependent", run, a.Model, a.EloLow, a.EloHigh, b.EloLow, b.EloHigh)
			}
			if a.RankStability != b.RankStability {
				t.Fatalf("run %d: %s stability drifted %v vs %v", run, a.Model,
					a.RankStability, b.RankStability)
			}
		}
	}
}

func TestADifferentSeedGivesADifferentResample(t *testing.T) {
	// Determinism must come from the SEED, not from the bootstrap silently doing nothing. If two
	// seeds produced identical intervals, the resampling would not be resampling.
	cmp := gen("s", "A", "B", "d1/s", "d2/s", 30, 20, 5)
	c1 := fastConfig()
	c2 := fastConfig()
	c2.Seed = c1.Seed + 1
	a := ratingOf(t, Estimate(cmp, c1), "A")
	b := ratingOf(t, Estimate(cmp, c2), "A")
	if a.EloLow == b.EloLow && a.EloHigh == b.EloHigh {
		t.Fatal("two different seeds gave identical intervals — the resampling is not resampling")
	}
	// Same point estimate though: the seed only affects the interval, never the fit.
	if a.Elo != b.Elo {
		t.Errorf("the seed moved the point estimate: %v vs %v", a.Elo, b.Elo)
	}
}
