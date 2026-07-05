package monopoly

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// hashRand is a deterministic pseudo-random stream keyed by (seed, label). The
// same inputs always produce the same sequence — the foundation of reproducible,
// provably-fair matches. It is NOT a general-purpose CSPRNG; it is a stable,
// auditable derivation from the match seed, identical in spirit to the goofspiel
// engine's hashRand.
type hashRand struct {
	seed  []byte
	label string
	ctr   uint64
}

func newHashRand(seed []byte, label string) *hashRand {
	return &hashRand{seed: append([]byte(nil), seed...), label: label}
}

func (r *hashRand) next() uint64 {
	mac := hmac.New(sha256.New, r.seed)
	mac.Write([]byte(r.label))
	var c [8]byte
	binary.BigEndian.PutUint64(c[:], r.ctr)
	mac.Write(c[:])
	r.ctr++
	return binary.BigEndian.Uint64(mac.Sum(nil)[:8])
}

// Intn returns a deterministic value in [0,n). Modulo bias is negligible for the
// small ranges used here (dice faces, deck sizes).
func (r *hashRand) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// Rand is the minimal randomness the engine consumes. The engine never reaches
// for math/rand or the clock; all chance flows through a seed-derived Rand so
// every roll is reproducible in replay.
type Rand interface{ Intn(n int) int }

// rollDice returns two dice for roll number `seq`, derived only from the seed and
// the roll index. Replaying from the same seed reproduces every roll exactly,
// regardless of which player rolled or when.
func rollDice(seed []byte, seq int) (int, int) {
	r := newHashRand(seed, fmt.Sprintf("dice:%d", seq))
	return r.Intn(6) + 1, r.Intn(6) + 1
}

// derivedDeckOrder returns a seed-shuffled permutation of [0,n) for a named deck
// (e.g. "chance", "community_chest"). Committed via the seed and revealed at
// settlement, so anyone can verify the deck order was fixed before play.
func derivedDeckOrder(seed []byte, label string, n int) []int {
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	r := newHashRand(seed, "deck:"+label)
	for i := len(order) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	return order
}

// NewTimeoutRand returns a deterministic Rand for filling a missed decision, keyed
// by (seed, turn, seat). A forced move is therefore identical every time the match
// is replayed from its seed.
func NewTimeoutRand(seed []byte, turn, seat int) Rand {
	return newHashRand(seed, fmt.Sprintf("timeout:%d:%d", turn, seat))
}
