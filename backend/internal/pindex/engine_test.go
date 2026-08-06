package pindex

import (
	"testing"
	"time"
)

func betaConfig(t *testing.T) Config {
	t.Helper()
	cfg, err := ParseConfig(1, []byte(`{
	  "scale": 1000,
	  "weights": {"arena": 0.50, "consistency": 0.20, "difficulty": 0.15, "activity": 0.15},
	  "norm": {"low": 1000, "high": 2500},
	  "arena": {"min_matches": 5, "match_cap": 50},
	  "consistency": {"rd_low": 50, "rd_high": 350, "sigma_low": 1.0, "sigma_high": 8.333333, "min_matches": 10},
	  "activity": {"match_k": 20, "diversity_target": 3, "recency_days": 7}
	}`))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}

// v2Config activates the Intelligence dimension (rebalanced to still sum to 1.0).
func v2Config(t *testing.T) Config {
	t.Helper()
	cfg, err := ParseConfig(2, []byte(`{
	  "scale": 1000,
	  "weights": {"arena": 0.45, "consistency": 0.20, "difficulty": 0.10, "activity": 0.10, "intelligence": 0.15},
	  "norm": {"low": 1000, "high": 2500},
	  "arena": {"min_matches": 5, "match_cap": 50},
	  "consistency": {"rd_low": 50, "rd_high": 350, "sigma_low": 1.0, "sigma_high": 8.333333, "min_matches": 10},
	  "activity": {"match_k": 20, "diversity_target": 3, "recency_days": 7},
	  "intelligence": {"w_legal": 0.4, "w_reliability": 0.4, "w_speed": 0.2, "latency_fast_ms": 500, "latency_slow_ms": 8000, "min_decisions": 200}
	}`))
	if err != nil {
		t.Fatalf("parse v2 config: %v", err)
	}
	return cfg
}

func TestIntelligenceEmptyIsZero(t *testing.T) {
	sub := Intelligence{}.Score(DeveloperInputs{}, v2Config(t))
	if sub.Score != 0 {
		t.Fatalf("no benchmarked decisions must score 0, got %.2f", sub.Score)
	}
}

func TestIntelligenceMonotonic(t *testing.T) {
	cfg := v2Config(t)
	// A clean, fast, well-sampled agent beats a sloppy, slow, force-defaulted one.
	good := Intelligence{}.Score(DeveloperInputs{BenchDecisions: 400, LegalRate: 1.0, FallbackRate: 0.0, AvgLatencyMS: 400}, cfg)
	bad := Intelligence{}.Score(DeveloperInputs{BenchDecisions: 400, LegalRate: 0.6, FallbackRate: 0.4, AvgLatencyMS: 9000}, cfg)
	if !(good.Score > bad.Score) {
		t.Fatalf("clean+fast (%.1f) should beat sloppy+slow (%.1f)", good.Score, bad.Score)
	}
	if good.Score < 900 {
		t.Fatalf("a perfect, fast, well-sampled agent should score near the ceiling, got %.1f", good.Score)
	}
}

func TestIntelligenceSampleGate(t *testing.T) {
	cfg := v2Config(t) // min_decisions 200
	full := Intelligence{}.Score(DeveloperInputs{BenchDecisions: 200, LegalRate: 1, FallbackRate: 0, AvgLatencyMS: 400}, cfg)
	half := Intelligence{}.Score(DeveloperInputs{BenchDecisions: 100, LegalRate: 1, FallbackRate: 0, AvgLatencyMS: 400}, cfg)
	if !(half.Score < full.Score) {
		t.Fatalf("fewer decisions should get gated (partial) credit: half=%.1f full=%.1f", half.Score, full.Score)
	}
}

func TestComputeV2SumsAndCountsIntelligence(t *testing.T) {
	cfg := v2Config(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	in := DeveloperInputs{
		UserPublicID: "usr_1", Season: 18,
		Arenas:       []ArenaInput{{Game: "goofspiel", Rating: 2000, RD: 60, Algo: "glicko2", Matches: 40}},
		TotalMatches: 40, DistinctArenas: 1, AvgOppRating: 1800, AvgOppRatingOnWin: 1850,
		BenchDecisions: 400, LegalRate: 0.98, FallbackRate: 0.02, AvgLatencyMS: 900,
		LastMatchAt: now.Add(-24 * time.Hour), AsOf: now,
	}
	res := NewEngine().Compute(in, cfg)
	if len(res.Contributions) != 6 {
		t.Fatalf("want 6 dimensions, got %d", len(res.Contributions))
	}
	if res.Sub(keyIntelligence) <= 0 {
		t.Fatal("intelligence sub-score should be > 0 with strong signals")
	}
	var sum float64
	for _, c := range res.Contributions {
		sum += c.Weighted
	}
	if d := sum - res.PIndex; d > 0.02 || d < -0.02 {
		t.Fatalf("contributions %.2f != P-Index %.2f", sum, res.PIndex)
	}
}

func TestParseConfigRejectsBadWeights(t *testing.T) {
	_, err := ParseConfig(9, []byte(`{"weights":{"arena":0.9,"consistency":0.2,"difficulty":0.15,"activity":0.15}}`))
	if err == nil {
		t.Fatal("expected error for weights summing to > 1")
	}
}

// A strong, active, varied developer should score high; the breakdown must be
// internally consistent (weighted contributions sum to the P-Index).
func TestComputeStrongDeveloper(t *testing.T) {
	cfg := betaConfig(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	in := DeveloperInputs{
		UserPublicID: "usr_1", Season: 18,
		Arenas: []ArenaInput{
			{Game: "goofspiel", Rating: 2100, RD: 60, Algo: "glicko2", Matches: 40},
			{Game: "mafia", Rating: 1900, Sigma: 2.0, Algo: "trueskill", Matches: 30},
		},
		TotalMatches: 70, DistinctArenas: 2,
		AvgOppRating: 1800, AvgOppRatingOnWin: 1850,
		LastMatchAt: now.Add(-24 * time.Hour), AsOf: now,
	}
	res := NewEngine().Compute(in, cfg)

	if res.PIndex < 500 || res.PIndex > 1000 {
		t.Fatalf("strong developer P-Index out of expected band: %.2f", res.PIndex)
	}
	var sum float64
	for _, c := range res.Contributions {
		sum += c.Weighted
	}
	if d := sum - res.PIndex; d > 0.02 || d < -0.02 {
		t.Fatalf("contributions %.2f != P-Index %.2f", sum, res.PIndex)
	}
	if len(res.Contributions) != 6 {
		t.Fatalf("want 6 dimensions, got %d", len(res.Contributions))
	}
}

// Same inputs → identical result and identical inputs hash (reproducibility).
func TestComputeDeterministic(t *testing.T) {
	cfg := betaConfig(t)
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	in := DeveloperInputs{
		UserPublicID: "usr_2", Season: 18,
		Arenas:       []ArenaInput{{Game: "goofspiel", Rating: 1600, RD: 120, Algo: "glicko2", Matches: 12}},
		TotalMatches: 12, DistinctArenas: 1, AvgOppRating: 1550, AvgOppRatingOnWin: 1500,
		LastMatchAt: now.Add(-3 * time.Hour), AsOf: now,
	}
	a := NewEngine().Compute(in, cfg)
	b := NewEngine().Compute(in, cfg)
	h1, h2 := in.Hash(), in.Hash()
	if a.PIndex != b.PIndex || h1 != h2 {
		t.Fatalf("non-deterministic: PIndex %.4f vs %.4f, hash %q vs %q", a.PIndex, b.PIndex, h1, h2)
	}
}

// A brand-new developer with no matches scores 0 across the board.
func TestComputeEmptyDeveloper(t *testing.T) {
	cfg := betaConfig(t)
	res := NewEngine().Compute(DeveloperInputs{UserPublicID: "usr_3", Season: 18, AsOf: time.Now()}, cfg)
	if res.PIndex != 0 {
		t.Fatalf("empty developer should be 0, got %.2f", res.PIndex)
	}
}

// Provisional arenas (below min_matches) must contribute less than the same rating
// with a full match count — a lucky short run can't dominate.
func TestArenaProvisionalWeighting(t *testing.T) {
	cfg := betaConfig(t)
	now := time.Now()
	few := DeveloperInputs{Arenas: []ArenaInput{{Game: "goofspiel", Rating: 2400, RD: 200, Algo: "glicko2", Matches: 2}}, TotalMatches: 2, DistinctArenas: 1, AsOf: now}
	many := DeveloperInputs{Arenas: []ArenaInput{{Game: "goofspiel", Rating: 2400, RD: 60, Algo: "glicko2", Matches: 40}}, TotalMatches: 40, DistinctArenas: 1, AsOf: now}
	if NewEngine().Compute(few, cfg).PIndex >= NewEngine().Compute(many, cfg).PIndex {
		t.Fatal("a 2-match arena should not score as high as a 40-match arena at the same rating")
	}
}
