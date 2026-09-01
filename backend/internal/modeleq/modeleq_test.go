package modeleq_test

import (
	"math"
	"math/rand"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/modeleq"
)

var now = time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)

// sample draws n answers from a categorical distribution and returns the histogram.
func sample(rng *rand.Rand, p map[string]float64, n int) map[string]int {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	// Deterministic order so a seeded run is reproducible.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	out := map[string]int{}
	for i := 0; i < n; i++ {
		u, acc := rng.Float64(), 0.0
		for _, k := range keys {
			acc += p[k]
			if u <= acc {
				out[k]++
				break
			}
		}
	}
	return out
}

func baselineFrom(obs []modeleq.Observation, captured time.Time) modeleq.Baseline {
	return modeleq.Baseline{
		Provider: "openrouter", Model: "anthropic/claude-opus-5",
		CapturedAt: captured,
		Prompts:    []modeleq.Prompt{{ID: "p1"}, {ID: "p2"}},
		Obs:        obs,
	}
}

// TestSameDistributionDoesNotFlag is the false-positive guard.
//
// A test that flags a healthy endpoint is worse than no test: the alert is ignored within a
// week and the real substitution goes unnoticed with it. Run many independent trials against
// an identical distribution and require the flag rate to sit near alpha rather than above it.
func TestSameDistributionDoesNotFlag(t *testing.T) {
	dist := map[string]float64{"1": 0.15, "2": 0.20, "3": 0.30, "4": 0.20, "5": 0.15}
	const alpha, trials = 0.05, 300
	flagged := 0
	for i := 0; i < trials; i++ {
		rng := rand.New(rand.NewSource(int64(i) + 1))
		base := []modeleq.Observation{
			{PromptID: "p1", Counts: sample(rng, dist, 300)},
			{PromptID: "p2", Counts: sample(rng, dist, 300)},
		}
		got := []modeleq.Observation{
			{PromptID: "p1", Counts: sample(rng, dist, 300)},
			{PromptID: "p2", Counts: sample(rng, dist, 300)},
		}
		v, err := modeleq.Compare(baselineFrom(base, now), got, alpha, now)
		if err != nil {
			t.Fatal(err)
		}
		if v.Differs {
			flagged++
		}
	}
	rate := float64(flagged) / trials
	// Generous ceiling: the point is that it is near alpha, not far above it.
	if rate > 3*alpha {
		t.Fatalf("false-positive rate %.3f on identical distributions, want <= %.3f — a test that "+
			"cries wolf gets muted, and then it protects nothing", rate, 3*alpha)
	}
	t.Logf("false-positive rate %.3f at alpha %.2f", rate, alpha)
}

// TestSubstitutedModelIsDetected is the whole point: a materially different answer
// distribution must be caught.
func TestSubstitutedModelIsDetected(t *testing.T) {
	real := map[string]float64{"1": 0.10, "2": 0.15, "3": 0.50, "4": 0.15, "5": 0.10}
	// A cheaper stand-in that leans on one answer — the shape a distilled or quantised model
	// tends to take.
	swapped := map[string]float64{"1": 0.30, "2": 0.25, "3": 0.20, "4": 0.15, "5": 0.10}

	const alpha, trials = 0.05, 200
	caught := 0
	for i := 0; i < trials; i++ {
		rng := rand.New(rand.NewSource(int64(i) + 1000))
		base := []modeleq.Observation{
			{PromptID: "p1", Counts: sample(rng, real, 300)},
			{PromptID: "p2", Counts: sample(rng, real, 300)},
		}
		got := []modeleq.Observation{
			{PromptID: "p1", Counts: sample(rng, swapped, 300)},
			{PromptID: "p2", Counts: sample(rng, swapped, 300)},
		}
		v, err := modeleq.Compare(baselineFrom(base, now), got, alpha, now)
		if err != nil {
			t.Fatal(err)
		}
		if v.Differs {
			caught++
		}
	}
	power := float64(caught) / trials
	if power < 0.95 {
		t.Fatalf("detected a substituted distribution only %.2f of the time at n=300/prompt; "+
			"a substitution test this blind would license the thing it exists to catch", power)
	}
	t.Logf("power %.2f against a moderately different distribution", power)
}

// TestSmallSampleIsReportedUnderpoweredNotPassed pins the honesty rule.
//
// "No difference detected" from a handful of samples is not evidence of sameness, and
// returning a clean pass there is how a green tick comes to mean nothing.
func TestSmallSampleIsReportedUnderpoweredNotPassed(t *testing.T) {
	dist := map[string]float64{"1": 0.5, "2": 0.5}
	rng := rand.New(rand.NewSource(7))
	base := []modeleq.Observation{{PromptID: "p1", Counts: sample(rng, dist, 20)}}
	got := []modeleq.Observation{{PromptID: "p1", Counts: sample(rng, dist, 20)}}
	b := baselineFrom(base, now)
	b.Obs = base
	v, err := modeleq.Compare(b, got, 0.05, now)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Underpowered {
		t.Fatalf("20 samples per prompt was not marked underpowered (floor is %d)",
			modeleq.MinSamplesPerPrompt)
	}
	if v.Differs {
		t.Fatal("identical distributions flagged as differing")
	}
	if v.Note == "" {
		t.Fatal("an underpowered result must carry its caveat, or a reader sees only the pass")
	}
}

// TestStaleBaselineIsFlagged: a baseline is a claim about a deployment at a moment. Comparing
// against a year-old one and reporting a clean difference as substitution would be wrong.
func TestStaleBaselineIsFlagged(t *testing.T) {
	dist := map[string]float64{"1": 0.5, "2": 0.5}
	rng := rand.New(rand.NewSource(11))
	obs := []modeleq.Observation{{PromptID: "p1", Counts: sample(rng, dist, 300)}}
	old := now.Add(-modeleq.DefaultFreshness - time.Hour)
	v, err := modeleq.Compare(baselineFrom(obs, old), obs, 0.05, now)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Stale {
		t.Fatal("a baseline older than the freshness window was not marked stale")
	}
}

// TestNoCommonPromptsIsAnError, not a pass. Comparing zero prompts and returning "no
// difference" is the most dangerous possible output.
func TestNoCommonPromptsIsAnError(t *testing.T) {
	base := []modeleq.Observation{{PromptID: "p1", Counts: map[string]int{"1": 100}}}
	got := []modeleq.Observation{{PromptID: "other", Counts: map[string]int{"1": 100}}}
	if _, err := modeleq.Compare(baselineFrom(base, now), got, 0.05, now); err == nil {
		t.Fatal("comparing disjoint prompt sets returned success; it must be an error")
	}
}

// TestPValueIsCalibrated sanity-checks the incomplete-gamma implementation against known
// chi-square upper-tail values. A miscalibrated tail silently changes every verdict.
func TestPValueIsCalibrated(t *testing.T) {
	// Compare uses the tail internally; drive it through a constructed table whose statistic
	// is known, then check the p-value is in the right region rather than re-deriving it.
	// x=3.841, df=1 is the classic 0.05 critical value.
	for _, tc := range []struct {
		x, want float64
		df      int
	}{
		{3.841, 0.05, 1},
		{5.991, 0.05, 2},
		{9.488, 0.05, 4},
	} {
		got := modeleq.UpperTail(tc.x, tc.df)
		if math.Abs(got-tc.want) > 0.005 {
			t.Errorf("upper tail at x=%.3f df=%d = %.4f, want ~%.2f", tc.x, tc.df, got, tc.want)
		}
	}
}
