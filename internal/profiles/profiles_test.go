package profiles

import (
	"math"
	"strings"
	"testing"
)

func TestDeriveComputesTotals(t *testing.T) {
	st := derive(Stats{Wins: 6, Losses: 3, Ties: 1, Elo: 1300})
	if st.Matches != 10 {
		t.Fatalf("matches = %d, want 10", st.Matches)
	}
	if math.Abs(st.WinRate-0.6) > 1e-9 {
		t.Fatalf("win rate = %v, want 0.6", st.WinRate)
	}
}

func TestDeriveDefaultsElo(t *testing.T) {
	if st := derive(Stats{}); st.Elo != 1200 {
		t.Fatalf("unrated ELO = %d, want 1200 default", st.Elo)
	}
}

func TestResultOf(t *testing.T) {
	if resultOf(50) != "win" || resultOf(-50) != "loss" || resultOf(0) != "tie" {
		t.Fatal("resultOf mapping wrong")
	}
}

func TestStyleReflectsData(t *testing.T) {
	if s := style(Stats{}); !strings.Contains(s, "Unproven") {
		t.Fatalf("no-match style = %q", s)
	}
	dominant := derive(Stats{Wins: 14, Losses: 4, Ties: 2, CurrentStreak: 4})
	s := style(dominant)
	if !strings.Contains(s, "Dominant") {
		t.Fatalf("dominant style = %q", s)
	}
	if !strings.Contains(s, "streak") {
		t.Fatalf("expected streak note for a 4-match streak: %q", s)
	}
}
