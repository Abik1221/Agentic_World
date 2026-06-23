package rating_test

import (
	"math"
	"testing"

	"github.com/agent-arena/arena/internal/rating"
)

func TestExpectedSymmetry(t *testing.T) {
	if e := rating.Expected(1200, 1200); math.Abs(e-0.5) > 1e-9 {
		t.Fatalf("equal ratings expected %v, want 0.5", e)
	}
	if s := rating.Expected(1500, 1300) + rating.Expected(1300, 1500); math.Abs(s-1) > 1e-9 {
		t.Fatalf("expected scores should sum to 1, got %v", s)
	}
}

func TestUpdateConservesAndDirects(t *testing.T) {
	// Equal ratings, A wins: A gains, B loses symmetrically.
	na, nb := rating.Update(1200, 1200, 1, 32)
	if na != 1216 || nb != 1184 {
		t.Fatalf("equal-rating win = (%d,%d), want (1216,1184)", na, nb)
	}
	if (na + nb) != 2400 {
		t.Fatalf("points not conserved: %d", na+nb)
	}
}

func TestUpsetRewardsMoreThanExpectedWin(t *testing.T) {
	// Favorite (1400) beats underdog (1200): small gain.
	favWin, _ := rating.Update(1400, 1200, 1, 32)
	favGain := favWin - 1400
	// Underdog (1200) beats favorite (1400): large gain.
	dogWin, _ := rating.Update(1200, 1400, 1, 32)
	dogGain := dogWin - 1200
	if !(dogGain > favGain) {
		t.Fatalf("upset gain %d should exceed expected-win gain %d", dogGain, favGain)
	}
}

func TestTieMovesTowardConvergence(t *testing.T) {
	// A tie nudges the higher-rated down and the lower-rated up.
	na, nb := rating.Update(1400, 1200, 0.5, 32)
	if !(na < 1400 && nb > 1200) {
		t.Fatalf("tie should converge ratings, got (%d,%d)", na, nb)
	}
}

func TestKScalesDelta(t *testing.T) {
	lo, _ := rating.Update(1200, 1200, 1, 16)
	hi, _ := rating.Update(1200, 1200, 1, 40)
	if (hi - 1200) <= (lo - 1200) {
		t.Fatalf("higher K should move ELO more: K40=%d K16=%d", hi, lo)
	}
}

func TestScoreForSeat0(t *testing.T) {
	if rating.ScoreForSeat0(0) != 1 || rating.ScoreForSeat0(1) != 0 || rating.ScoreForSeat0(rating.Tie) != 0.5 {
		t.Fatal("ScoreForSeat0 mapping wrong")
	}
}
