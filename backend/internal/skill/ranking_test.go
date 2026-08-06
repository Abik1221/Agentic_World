package skill

import (
	"math"
	"testing"
)

// THE property this exists for.
//
// A raw mean puts a five-decision fluke above a four-thousand-decision professional, and
// that is what a public leaderboard would show. The shrunk estimate must not.
func TestSmallSampleFlukeDoesNotOutrankASeasonOfEvidence(t *testing.T) {
	obs := []Observation{
		{AgentPublicID: "ag_fluke", Decisions: 5, Quality: 1.00},  // perfect, on nothing
		{AgentPublicID: "ag_pro", Decisions: 4000, Quality: 0.78}, // strong, on everything
		{AgentPublicID: "ag_mid", Decisions: 900, Quality: 0.55},
		{AgentPublicID: "ag_weak", Decisions: 1200, Quality: 0.31},
	}
	ranked, _ := Rank(obs)
	byID := map[string]Ranked{}
	for _, r := range ranked {
		byID[r.AgentPublicID] = r
	}

	if byID["ag_fluke"].Observed <= byID["ag_pro"].Observed {
		t.Fatal("test setup: the fluke should out-score the pro on the RAW mean")
	}
	if !(byID["ag_pro"].RankScore > byID["ag_fluke"].RankScore) {
		t.Fatalf("a 5-decision perfect record (rank score %.4f) still outranks a 4000-decision "+
			"78%% record (%.4f) — the leaderboard is sortable by luck",
			byID["ag_fluke"].RankScore, byID["ag_pro"].RankScore)
	}
	// Rank returns leaderboard order directly, so the caller cannot sort on the wrong
	// field by accident.
	if ranked[0].AgentPublicID != "ag_pro" {
		t.Fatalf("leaderboard leader is %q, want ag_pro", ranked[0].AgentPublicID)
	}
	for i := 1; i < len(ranked); i++ {
		if ranked[i-1].RankScore < ranked[i].RankScore {
			t.Fatalf("Rank did not return descending order at position %d", i)
		}
	}
}

// The point estimate and the rank score answer different questions and both must be
// available: the estimate is the best guess to DISPLAY, the lower bound is what we are
// confident of and therefore what we SORT on. Collapsing them loses one or the other.
func TestEstimateAndRankScoreAreBothReported(t *testing.T) {
	p := Prior{Alpha: 25, Beta: 25, Mean: 0.5, Strength: 50}
	r := Shrink(Observation{AgentPublicID: "a", Decisions: 40, Quality: 0.9}, p)
	if r.RankScore != r.Lower {
		t.Fatalf("rank score %.4f is not the lower bound %.4f", r.RankScore, r.Lower)
	}
	if !(r.Estimate > r.RankScore) {
		t.Fatalf("estimate %.4f is not above the conservative rank score %.4f", r.Estimate, r.RankScore)
	}
}

// Shrinkage must be monotone in sample size: more evidence always means more of your own
// number and less of the population's. If this is not monotone the estimator is not doing
// Bayes, it is doing something arbitrary.
func TestShrinkageDecreasesMonotonicallyWithEvidence(t *testing.T) {
	p := Prior{Alpha: 25, Beta: 25, Mean: 0.5, Strength: 50}
	prev := 2.0
	for _, n := range []int{1, 5, 25, 100, 1000, 10000} {
		got := Shrink(Observation{Decisions: n, Quality: 0.9}, p).Shrinkage
		if got >= prev {
			t.Fatalf("n=%d shrank by %.4f, not less than the previous %.4f", n, got, prev)
		}
		if got < 0 || got > 1 {
			t.Fatalf("n=%d shrinkage %.4f outside [0,1]", n, got)
		}
		prev = got
	}
}

// With enough evidence the estimate must converge on the observed mean — otherwise the
// prior never lets go and strong agents are permanently capped.
func TestLargeSamplesConvergeOnTheObservedMean(t *testing.T) {
	p := Prior{Alpha: 25, Beta: 25, Mean: 0.5, Strength: 50}
	got := Shrink(Observation{Decisions: 200000, Quality: 0.91}, p)
	if math.Abs(got.Estimate-0.91) > 0.001 {
		t.Fatalf("with 200k decisions the estimate is %.4f, want ≈0.91 — the prior is never "+
			"releasing and a genuinely strong agent can never show it", got.Estimate)
	}
}

// An agent with no scored decisions must sit at the population mean with a maximally wide
// interval — not at 0 (which would read as "terrible") and not at 1.
func TestNoEvidenceLandsOnThePriorNotOnZero(t *testing.T) {
	p := Prior{Alpha: 30, Beta: 20, Mean: 0.6, Strength: 50}
	got := Shrink(Observation{AgentPublicID: "ag_new", Decisions: 0}, p)
	if math.Abs(got.Estimate-0.6) > 1e-9 {
		t.Fatalf("an unscored agent estimates %.4f, want the prior mean 0.6", got.Estimate)
	}
	if got.Shrinkage != 1 {
		t.Fatalf("shrinkage %.4f want 1 — nothing was learned from this agent", got.Shrinkage)
	}
	if got.Lower != 0 || got.Upper != 1 {
		t.Fatalf("interval [%.3f,%.3f] want the widest possible", got.Lower, got.Upper)
	}
}

// The interval has to narrow as evidence accumulates, or it is decoration rather than a
// statement of confidence.
func TestIntervalsNarrowWithEvidence(t *testing.T) {
	p := Prior{Alpha: 25, Beta: 25, Mean: 0.5, Strength: 50}
	wide := Shrink(Observation{Decisions: 10, Quality: 0.8}, p)
	tight := Shrink(Observation{Decisions: 5000, Quality: 0.8}, p)
	if !(wide.Upper-wide.Lower > tight.Upper-tight.Lower) {
		t.Fatalf("10 decisions gave a %.4f-wide interval and 5000 gave %.4f",
			wide.Upper-wide.Lower, tight.Upper-tight.Lower)
	}
	for _, r := range []Ranked{wide, tight} {
		if r.Lower < 0 || r.Upper > 1 || r.Lower > r.Estimate || r.Estimate > r.Upper {
			t.Fatalf("malformed interval: [%.4f, %.4f] around %.4f", r.Lower, r.Upper, r.Estimate)
		}
	}
}

// ── Fitting the prior ───────────────────────────────────────────────────────

// The prior must be fitted from the POPULATION, weighted by evidence. A crowd of tiny
// samples must not be able to drag it — every shrunk estimate depends on it, so a prior
// pulled around by noise miscalibrates the whole leaderboard at once.
func TestPriorIsWeightedByEvidenceNotByHeadcount(t *testing.T) {
	// Twenty agents with 2 decisions each, all terrible; one agent with 10000, strong.
	obs := []Observation{{AgentPublicID: "ag_big", Decisions: 10000, Quality: 0.80}}
	for i := 0; i < 20; i++ {
		obs = append(obs, Observation{AgentPublicID: "tiny", Decisions: 2, Quality: 0.0})
	}
	p := FitPrior(obs)
	if p.Mean < 0.7 {
		t.Fatalf("prior mean %.4f — 40 decisions' worth of noise outvoted 10000 decisions "+
			"of evidence, so the prior tracks headcount rather than information", p.Mean)
	}
}

// Degenerate populations must not produce a degenerate prior. Each of these would divide
// by zero or produce a negative strength under a naive method-of-moments fit.
func TestPriorSurvivesDegeneratePopulations(t *testing.T) {
	cases := map[string][]Observation{
		"empty":            nil,
		"all zero-sample":  {{Decisions: 0, Quality: 0.5}, {Decisions: 0, Quality: 0.9}},
		"no variance":      {{Decisions: 100, Quality: 0.5}, {Decisions: 100, Quality: 0.5}, {Decisions: 100, Quality: 0.5}},
		"everyone perfect": {{Decisions: 100, Quality: 1}, {Decisions: 100, Quality: 1}, {Decisions: 100, Quality: 1}},
		"everyone zero":    {{Decisions: 100, Quality: 0}, {Decisions: 100, Quality: 0}, {Decisions: 100, Quality: 0}},
		"single agent":     {{Decisions: 100, Quality: 0.7}},
	}
	for name, obs := range cases {
		p := FitPrior(obs)
		if math.IsNaN(p.Alpha) || math.IsNaN(p.Beta) || math.IsInf(p.Alpha, 0) || math.IsInf(p.Beta, 0) {
			t.Errorf("%s: prior is not finite (α=%v β=%v)", name, p.Alpha, p.Beta)
		}
		if p.Alpha <= 0 || p.Beta <= 0 {
			t.Errorf("%s: prior is not a valid Beta (α=%.4f β=%.4f)", name, p.Alpha, p.Beta)
		}
		if p.Strength <= 0 {
			t.Errorf("%s: prior strength %.4f", name, p.Strength)
		}
		// And it must still produce usable estimates.
		r := Shrink(Observation{Decisions: 50, Quality: 0.6}, p)
		if math.IsNaN(r.Estimate) || r.Estimate < 0 || r.Estimate > 1 {
			t.Errorf("%s: estimate %.4f is unusable", name, r.Estimate)
		}
	}
}

// The prior must not become so strong that it pins everyone to the mean — a leaderboard
// that cannot discriminate is worse than no leaderboard.
func TestPriorStaysWeaklyInformative(t *testing.T) {
	// A population with almost no spread would fit an enormous strength unbounded.
	var obs []Observation
	for i := 0; i < 50; i++ {
		obs = append(obs, Observation{Decisions: 1000, Quality: 0.5 + float64(i%2)*1e-6})
	}
	p := FitPrior(obs)
	if p.Strength > 500 {
		t.Fatalf("prior strength %.1f exceeds the cap; every agent would be pinned to the mean", p.Strength)
	}
	strong := Shrink(Observation{Decisions: 2000, Quality: 0.95}, p)
	weak := Shrink(Observation{Decisions: 2000, Quality: 0.20}, p)
	if !(strong.Estimate-weak.Estimate > 0.2) {
		t.Fatalf("with equal, large samples the estimator separated 95%% from 20%% by only "+
			"%.4f — it has stopped discriminating", strong.Estimate-weak.Estimate)
	}
}

// Ranking must be deterministic and must not depend on input order.
func TestRankingIsOrderIndependent(t *testing.T) {
	a := []Observation{
		{AgentPublicID: "x", Decisions: 100, Quality: 0.7},
		{AgentPublicID: "y", Decisions: 800, Quality: 0.4},
		{AgentPublicID: "z", Decisions: 12, Quality: 0.95},
	}
	b := []Observation{a[2], a[0], a[1]}

	ra, pa := Rank(a)
	rb, pb := Rank(b)
	if pa != pb {
		t.Fatalf("the fitted prior depends on input order: %+v vs %+v", pa, pb)
	}
	find := func(rs []Ranked, id string) Ranked {
		for _, r := range rs {
			if r.AgentPublicID == id {
				return r
			}
		}
		t.Fatalf("missing %s", id)
		return Ranked{}
	}
	for _, id := range []string{"x", "y", "z"} {
		if find(ra, id) != find(rb, id) {
			t.Fatalf("%s ranked differently depending on input order", id)
		}
	}
}
