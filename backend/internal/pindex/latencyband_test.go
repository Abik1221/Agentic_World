package pindex

import "testing"

// The speed curve has to discriminate across the latencies that actually occur.
//
// The shipped band scored 500ms → 8,000ms. Measured against the platform's real
// distribution — p50 6,988ms, p90 23,161ms, p99 55,011ms — that is saturated for more
// than half of all honest decisions, and it cannot tell a 23-second agent from a
// 55-second one from a broken one. Latency is 20% of this dimension and was carrying
// almost no information.
//
// That matters more since adaptive windows let a slow local or reasoning model take the
// time it genuinely needs: the deliberate trade is that slowness costs REPUTATION rather
// than the match, and that trade only works if reputation can see the difference.
//
// These assert the SHAPE the curve must have, so a future band that re-saturates fails
// here rather than silently flattening the ranking.
func TestSpeedCurveDiscriminatesAcrossRealLatencies(t *testing.T) {
	var cfg Config
	cfg.Scale = 1000
	cfg.Weights.Intelligence = 1
	cfg.Intelligence.WSpeed = 1 // isolate speed from legality and reliability
	cfg.Intelligence.LatencyFastMS = 2000
	cfg.Intelligence.LatencySlowMS = 60000
	cfg.Intelligence.MinDecisions = 1

	score := func(ms float64) float64 {
		in := DeveloperInputs{BenchDecisions: 500, LegalRate: 1, FallbackRate: 0, AvgLatencyMS: ms}
		return Intelligence{}.Score(in, cfg).Score
	}

	// The three points the platform actually occupies must be separated, not clustered.
	p50, p90, p99 := score(6988), score(23161), score(55011)
	if !(p50 > p90 && p90 > p99) {
		t.Fatalf("the curve does not order real latencies: p50=%.1f p90=%.1f p99=%.1f", p50, p90, p99)
	}
	if p50-p90 < 100 || p90-p99 < 100 {
		t.Fatalf("real latencies are barely separated (p50=%.1f p90=%.1f p99=%.1f); the band "+
			"is still too narrow to rank the agents this platform has", p50, p90, p99)
	}
	// A median agent should not be treated as though it were broken.
	if p50 < 800 {
		t.Errorf("a median 7s agent scores %.1f/1000 on speed; that is a penalty for being "+
			"typical rather than slow", p50)
	}
	// And a genuinely slow one must still be distinguishable from a stopped one.
	if p99 <= 0 {
		t.Errorf("a 55s agent scores %.1f — indistinguishable from one that never answers", p99)
	}
}

// The old band, kept as the regression it was: it must NOT be able to satisfy the above.
// If a future change makes 500→8000 look acceptable, the test above has stopped meaning
// anything.
func TestTheOldBandWouldFailTheDiscriminationCheck(t *testing.T) {
	var cfg Config
	cfg.Scale = 1000
	cfg.Weights.Intelligence = 1
	cfg.Intelligence.WSpeed = 1
	cfg.Intelligence.LatencyFastMS = 500
	cfg.Intelligence.LatencySlowMS = 8000
	cfg.Intelligence.MinDecisions = 1

	score := func(ms float64) float64 {
		in := DeveloperInputs{BenchDecisions: 500, LegalRate: 1, FallbackRate: 0, AvgLatencyMS: ms}
		return Intelligence{}.Score(in, cfg).Score
	}
	if score(23161) != score(55011) {
		t.Fatal("the old band separates 23s from 55s; the premise of migration 0078 is wrong " +
			"and its justification needs revisiting")
	}
}
