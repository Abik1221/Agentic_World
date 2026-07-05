package bot

import (
	crand "crypto/rand"
	"encoding/binary"
	"math/rand"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// Difficulty levels exposed to developers when starting a sandbox match.
const (
	Easy   = "easy"
	Medium = "medium"
	Hard   = "hard"
)

// PolicyForDifficulty maps a coarse difficulty to a strategy name. Unknown
// levels fall back to medium so a typo still yields a sensible opponent.
func PolicyForDifficulty(level string) string {
	switch level {
	case Easy:
		return Random
	case Hard:
		return Balanced
	default:
		return Proportional
	}
}

// Service picks moves for the platform's house agent. It satisfies the match
// package's Bot port. Each Pick uses a fresh, independently-seeded rng so it is
// safe to call concurrently from many in-flight matches.
type Service struct{}

// NewService constructs the house-agent move picker.
func NewService() *Service { return &Service{} }

// Pick returns a legal card for `seat` under the named policy. An unknown policy
// degrades to Proportional. The returned card is always in the seat's hand.
func (s *Service) Pick(state gs.State, seat int, policy string) int {
	pol, ok := Strategies[policy]
	if !ok {
		pol = Strategies[Proportional]
	}
	return pol(state, seat, newRand())
}

func newRand() *rand.Rand {
	var b [8]byte
	if _, err := crand.Read(b[:]); err != nil {
		return rand.New(rand.NewSource(1)) //nolint:gosec // fallback; house move, not security-sensitive
	}
	return rand.New(rand.NewSource(int64(binary.LittleEndian.Uint64(b[:])))) //nolint:gosec // house move RNG, not security-sensitive
}
