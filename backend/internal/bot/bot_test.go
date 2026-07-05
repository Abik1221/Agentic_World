package bot_test

import (
	"math/rand"
	"testing"

	"github.com/agent-arena/arena/internal/bot"
	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// playOut runs a full match between two policies via the pure engine and returns
// the final scores. It mirrors how the live house agent feeds the engine, so it
// doubles as a check that every policy always returns a legal card.
func playOut(t *testing.T, seedByte byte, a, b bot.Policy) (int, int) {
	t.Helper()
	eng := gs.New(gs.DefaultConfig())
	seed := make([]byte, 32)
	seed[0] = seedByte
	s, _ := eng.Init(seed)
	rng := rand.New(rand.NewSource(int64(seedByte) + 1)) //nolint:gosec // test RNG

	for !s.Finished {
		for _, seat := range []int{gs.SeatA, gs.SeatB} {
			pol := a
			if seat == gs.SeatB {
				pol = b
			}
			card := pol(s, seat, rng)
			if !inHand(s.Hands[seat], card) {
				t.Fatalf("policy returned illegal card %d; hand=%v", card, s.Hands[seat])
			}
			ns, _, err := eng.Seal(s, seat, card)
			if err != nil {
				t.Fatalf("seal seat %d card %d: %v", seat, card, err)
			}
			s = ns
		}
		ns, _, err := eng.Resolve(s)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		s = ns
	}
	return s.Scores[gs.SeatA], s.Scores[gs.SeatB]
}

func inHand(hand []int, c int) bool {
	for _, h := range hand {
		if h == c {
			return true
		}
	}
	return false
}

// TestAllStrategiesPlayLegalMoves runs every registered strategy against every
// other over several seeds; playOut fails on any illegal card.
func TestAllStrategiesPlayLegalMoves(t *testing.T) {
	for _, a := range bot.Strategies {
		for _, b := range bot.Strategies {
			for seed := byte(1); seed <= 5; seed++ {
				_, _ = playOut(t, seed, a, b)
			}
		}
	}
}

// TestBalancedBeatsRandom asserts the "hard" house policy is genuinely stronger
// than the "easy" one over a batch of seeded games — so difficulty means something.
func TestBalancedBeatsRandom(t *testing.T) {
	balancedWins := 0
	const games = 40
	for seed := byte(1); seed <= games; seed++ {
		// Balanced at seat A, random at seat B.
		a, b := playOut(t, seed, bot.BalancedPlay, bot.RandomPlay)
		if a > b {
			balancedWins++
		}
	}
	if balancedWins <= games/2 {
		t.Fatalf("balanced won only %d/%d vs random; expected a clear majority", balancedWins, games)
	}
}

// TestServicePickIsLegal verifies the house move picker returns a legal card for
// each difficulty's policy.
func TestServicePickIsLegal(t *testing.T) {
	svc := bot.NewService()
	eng := gs.New(gs.DefaultConfig())
	seed := make([]byte, 32)
	s, _ := eng.Init(seed)
	for _, level := range []string{bot.Easy, bot.Medium, bot.Hard} {
		policy := bot.PolicyForDifficulty(level)
		card := svc.Pick(s, gs.SeatB, policy)
		if !inHand(s.Hands[gs.SeatB], card) {
			t.Fatalf("%s/%s: illegal card %d; hand=%v", level, policy, card, s.Hands[gs.SeatB])
		}
	}
}
