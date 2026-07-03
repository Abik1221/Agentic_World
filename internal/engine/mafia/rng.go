package mafia

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
)

// hashRand is a deterministic pseudo-random stream keyed by (seed, label) — the
// same construction the other engines use. Bots draw from it so a table replays
// identically from its seed.
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

// Intn returns a deterministic value in [0,n).
func (r *hashRand) Intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.next() % uint64(n))
}

// intn returns an UNBIASED deterministic value in [0,n) via rejection sampling
// over the 64-bit stream. Plain modulo (Intn) skews toward small values when n
// does not divide 2^64; that bias is acceptable for cosmetic bot choices but not
// for role assignment, which the spec requires to be fair and unpredictable.
func (r *hashRand) intn(n int) int {
	if n <= 0 {
		return 0
	}
	un := uint64(n)
	// thresh = 2^64 mod n. Reject draws below it so the accepted range is an
	// exact multiple of n, eliminating modulo bias.
	thresh := (-un) % un
	for {
		v := r.next()
		if v >= thresh {
			return int(v % un)
		}
	}
}
