package rating_test

import (
	"testing"

	"github.com/agent-arena/arena/internal/rating"
)

func newAgent() rating.PlayerRating  { return rating.PlayerRating{Elo: 1500, RD: 350, Vol: 0.06} }
func settled() rating.PlayerRating   { return rating.PlayerRating{Elo: 1500, RD: 50, Vol: 0.06} }

// A win raises the winner, lowers the loser, and shrinks both deviations (a played
// game is evidence, so uncertainty drops).
func TestGlicko2WinDirectionAndRDShrinks(t *testing.T) {
	a, b := rating.Glicko2(newAgent(), newAgent(), 1) // A wins
	if a.Elo <= 1500 {
		t.Fatalf("winner rating did not rise: %d", a.Elo)
	}
	if b.Elo >= 1500 {
		t.Fatalf("loser rating did not fall: %d", b.Elo)
	}
	if a.RD >= 350 || b.RD >= 350 {
		t.Fatalf("RD should shrink after a game: a=%.1f b=%.1f", a.RD, b.RD)
	}
}

// The core reason for Glicko-2 over fixed-K Elo: a player's own rating moves MORE
// when it is uncertain (high RD) than when it is settled (low RD), for the same
// result against the same opponent. Fixed-K cannot express this.
func TestGlicko2UncertaintyMovesRatingMore(t *testing.T) {
	opp := rating.PlayerRating{Elo: 1500, RD: 200, Vol: 0.06}
	hi, _ := rating.Glicko2(newAgent(), opp, 1) // uncertain winner
	lo, _ := rating.Glicko2(settled(), opp, 1)  // settled winner
	gainHi := hi.Elo - 1500
	gainLo := lo.Elo - 1500
	if !(gainHi > gainLo) {
		t.Fatalf("uncertain agent should move more: high-RD gain %d, low-RD gain %d", gainHi, gainLo)
	}
}

// Beating a stronger opponent (an upset) gains more than beating a weaker one.
func TestGlicko2UpsetRewardsMore(t *testing.T) {
	me := newAgent()
	favWin, _ := rating.Glicko2(me, rating.PlayerRating{Elo: 1300, RD: 200}, 1) // beat weaker
	dogWin, _ := rating.Glicko2(me, rating.PlayerRating{Elo: 1700, RD: 200}, 1) // beat stronger
	if !(dogWin.Elo > favWin.Elo) {
		t.Fatalf("upset win (%d) should exceed expected win (%d)", dogWin.Elo, favWin.Elo)
	}
}

// A tie nudges the higher-rated down and the lower-rated up (convergence).
func TestGlicko2TieConverges(t *testing.T) {
	hiP := rating.PlayerRating{Elo: 1700, RD: 200}
	loP := rating.PlayerRating{Elo: 1300, RD: 200}
	a, b := rating.Glicko2(hiP, loP, 0.5)
	if !(a.Elo < 1700 && b.Elo > 1300) {
		t.Fatalf("tie should converge: hi %d→%d, lo %d→%d", 1700, a.Elo, 1300, b.Elo)
	}
}

// Pure + deterministic: identical inputs give identical outputs.
func TestGlicko2Deterministic(t *testing.T) {
	a1, b1 := rating.Glicko2(newAgent(), settled(), 1)
	a2, b2 := rating.Glicko2(newAgent(), settled(), 1)
	if a1 != a2 || b1 != b2 {
		t.Fatalf("non-deterministic: %+v/%+v vs %+v/%+v", a1, b1, a2, b2)
	}
}

func TestScoreForSeat0(t *testing.T) {
	if rating.ScoreForSeat0(0) != 1 || rating.ScoreForSeat0(1) != 0 || rating.ScoreForSeat0(rating.Tie) != 0.5 {
		t.Fatal("ScoreForSeat0 mapping wrong")
	}
}
