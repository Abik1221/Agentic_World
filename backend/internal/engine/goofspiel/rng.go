package goofspiel

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

// hashRand is a deterministic pseudo-random stream keyed by (seed, label). The
// same inputs always produce the same sequence — the foundation of reproducible,
// provably-fair matches. It is NOT a general-purpose CSPRNG; it is a stable,
// auditable derivation from the match seed.
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
// small ranges used here (hand sizes ≤ 13).
func (r *hashRand) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// derivePrizeOrder returns the prize sequence. Open mode uses the fixed Cards
// order (no hidden information); shuffled mode applies a seed-derived
// Fisher–Yates permutation — committed before play, verifiable after.
func derivePrizeOrder(cfg Config, seed []byte) []int {
	order := append([]int(nil), cfg.Cards...)
	if cfg.FairnessMode == FairnessOpen {
		return order
	}
	r := newHashRand(seed, "prize-order")
	for i := len(order) - 1; i > 0; i-- {
		j := r.Intn(i + 1)
		order[i], order[j] = order[j], order[i]
	}
	return order
}
