package query

import (
	"math"
	"testing"
	"time"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestBenchmarkStat_Compute(t *testing.T) {
	s := BenchmarkStat{
		Matches: 10, Wins: 6, Losses: 4, Decisions: 20, Legal: 15, Fallbacks: 5, Timeouts: 3, latencySumMS: 4000,
	}
	s.compute()
	if !approx(s.WinRate, 0.6) {
		t.Errorf("win_rate=%v want 0.6", s.WinRate)
	}
	if !approx(s.LegalRate, 0.75) {
		t.Errorf("legal_rate=%v want 0.75", s.LegalRate)
	}
	if !approx(s.FallbackRate, 0.25) {
		t.Errorf("fallback_rate=%v want 0.25", s.FallbackRate)
	}
	if !approx(s.TimeoutRate, 0.15) {
		t.Errorf("timeout_rate=%v want 0.15", s.TimeoutRate)
	}
	if !approx(s.AvgLatencyMS, 200) {
		t.Errorf("avg_latency_ms=%v want 200", s.AvgLatencyMS)
	}
}

func TestBenchmarkStat_ComputeZeroDecisionsNoNaN(t *testing.T) {
	var s BenchmarkStat
	s.compute()
	if s.LegalRate != 0 || s.FallbackRate != 0 || s.AvgLatencyMS != 0 || s.WinRate != 0 {
		t.Errorf("zero counters must yield zero rates, got %+v", s)
	}
}

func TestWilsonLowerBound(t *testing.T) {
	// Degenerate cases.
	if wilsonLowerBound(0, 0, 1.96) != 0 {
		t.Error("n=0 must be 0")
	}
	if wilsonLowerBound(0, 10, 1.96) != 0 {
		t.Error("0 wins must be 0")
	}
	// Bound is always <= the point estimate and within [0,1].
	lb := wilsonLowerBound(80, 100, 1.96)
	if lb <= 0 || lb >= 0.8 {
		t.Errorf("lb=%v should be in (0, 0.8)", lb)
	}
	// THE property: a 3/3 (100%) fluke must rank BELOW a 400/500 (80%) solid record.
	fluke := wilsonLowerBound(3, 3, 1.96)
	solid := wilsonLowerBound(400, 500, 1.96)
	if !(fluke < solid) {
		t.Errorf("3/3 lb=%v should be < 400/500 lb=%v (sample-size awareness)", fluke, solid)
	}
	// More matches at the same rate ⇒ higher (tighter) lower bound.
	if !(wilsonLowerBound(80, 100, 1.96) < wilsonLowerBound(800, 1000, 1.96)) {
		t.Error("larger sample at same rate should have higher lower bound")
	}
}

func TestBenchmarkStat_ComputeSetsWinRateLB(t *testing.T) {
	s := BenchmarkStat{Matches: 100, Wins: 80}
	s.compute()
	if s.WinRateLB <= 0 || s.WinRateLB >= s.WinRate {
		t.Errorf("win_rate_lb=%v should be in (0, win_rate=%v)", s.WinRateLB, s.WinRate)
	}
}

func TestRatio(t *testing.T) {
	if ratio(3, 4) != 0.75 || ratio(0, 0) != 0 || ratio(5, 0) != 0 {
		t.Error("ratio math/zero-guard wrong")
	}
}

func TestDayBucketDaysAgo(t *testing.T) {
	got := dayBucketDaysAgo(7)
	if got.Hour() != 0 || got.Minute() != 0 || got.Location() != time.UTC {
		t.Errorf("bucket not midnight UTC: %v", got)
	}
	// A non-positive window defaults to 30 days (not "now").
	if !dayBucketDaysAgo(0).Before(time.Now().UTC().AddDate(0, 0, -29)) {
		t.Error("days<=0 should default to 30")
	}
}
