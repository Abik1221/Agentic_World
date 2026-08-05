package rating

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 0.001 }

// Tokens per minute is measured against REAL match wall-clock, because that is what
// the public board promises. 120k tokens across two minutes of play is 60k/min.
func TestTokensPerMinuteUsesRealPlayTime(t *testing.T) {
	m := &ModelStat{Tokens: 120_000, PlaySeconds: 120, Decisions: 300}
	deriveModelStat(m)
	if !approx(m.TokensPerMin, 60_000) {
		t.Fatalf("tokens/min = %v, want 60000", m.TokensPerMin)
	}
}

// No finished match has contributed a duration yet: report 0, never an extrapolation.
// A rate nobody can reproduce is worse than an honest blank.
func TestNoPlayTimeYieldsZeroRateNotAGuess(t *testing.T) {
	m := &ModelStat{Tokens: 50_000, PlaySeconds: 0, Decisions: 300}
	deriveModelStat(m)
	if m.TokensPerMin != 0 {
		t.Fatalf("tokens/min = %v with no measured play time; want 0", m.TokensPerMin)
	}
}

// A model that has barely played must not be able to top a public leaderboard on a
// lucky run, so intelligence stays 0 below the sample threshold.
func TestIntelligenceNeedsARealSample(t *testing.T) {
	small := &ModelStat{Decisions: intelMinDecisions - 1, Legal: intelMinDecisions - 1, AvgLatencyMs: 100}
	deriveModelStat(small)
	if small.Intelligence != 0 {
		t.Fatalf("scored %d on a %d-decision sample; want 0", small.Intelligence, small.Decisions)
	}
	// Rates are still reported — they are direct observations, not a score.
	if !approx(small.LegalRate, 1) {
		t.Fatalf("legal rate = %v, want 1", small.LegalRate)
	}
}

// A perfect model: every move legal, no fallbacks, comfortably inside the fast
// latency band. That is full marks on all three components.
func TestPerfectModelScoresFullMarks(t *testing.T) {
	m := &ModelStat{Decisions: 1000, Legal: 1000, Fallbacks: 0, AvgLatencyMs: 200}
	deriveModelStat(m)
	if m.Intelligence != intelScale {
		t.Fatalf("intelligence = %d, want %d", m.Intelligence, intelScale)
	}
}

// Slow past the ceiling zeroes the SPEED component only — a model that plays legally
// but slowly still keeps its legal and reliability credit.
func TestSlowModelLosesOnlyTheSpeedComponent(t *testing.T) {
	m := &ModelStat{Decisions: 1000, Legal: 1000, Fallbacks: 0, AvgLatencyMs: 20_000}
	deriveModelStat(m)
	want := int((intelWLegal + intelWReliability) * intelScale)
	if m.Intelligence != want {
		t.Fatalf("intelligence = %d, want %d (legal + reliability, no speed)", m.Intelligence, want)
	}
}

// Fallbacks are the engine substituting a move the agent failed to produce, so they
// cost reliability directly.
func TestFallbacksCostReliability(t *testing.T) {
	m := &ModelStat{Decisions: 1000, Legal: 500, Fallbacks: 500, AvgLatencyMs: 200}
	deriveModelStat(m)
	if !approx(m.FallbackRate, 0.5) || !approx(m.LegalRate, 0.5) {
		t.Fatalf("rates = legal %v / fallback %v, want 0.5 each", m.LegalRate, m.FallbackRate)
	}
	// 0.4*0.5 + 0.4*0.5 + 0.2*1.0 = 0.6
	if m.Intelligence != 600 {
		t.Fatalf("intelligence = %d, want 600", m.Intelligence)
	}
}

// Zero decisions must not divide by zero or produce a score.
func TestNoDecisionsIsInert(t *testing.T) {
	m := &ModelStat{Decisions: 0, Tokens: 100}
	deriveModelStat(m)
	if m.Intelligence != 0 || m.LegalRate != 0 || m.FallbackRate != 0 {
		t.Fatalf("a model with no decisions produced %+v", m)
	}
}
