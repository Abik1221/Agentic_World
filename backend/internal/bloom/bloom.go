// Package bloom is a small, immutable-after-build Bloom filter for answering
// "definitely not present" without touching the database.
//
// # The only property that matters
//
// A Bloom filter has NO FALSE NEGATIVES. If Has reports false, the item was never
// added — that answer is exact. If it reports true the item is *probably* present,
// and the caller must confirm against the real store.
//
// That asymmetry is what makes it safe for a live availability check. "This
// username is free" is the common case while somebody types, and it is precisely
// the case the filter can answer authoritatively and for free. The rare "probably
// taken" pays for one indexed lookup.
//
// Using it the other way round — trusting a positive — would be a correctness bug,
// so Has is deliberately named for what it can promise rather than for what a
// caller might wish it promised.
package bloom

import (
	"hash/fnv"
	"math"
)

// Filter is a fixed-size Bloom filter. Safe for concurrent READS once built; Add is
// not synchronised, so build a filter (or write-through under the owner's lock) and
// publish it as a whole. Callers that swap filters should swap the pointer rather
// than mutate a live one.
type Filter struct {
	bits []uint64
	m    uint64 // bit count
	k    uint64 // hashes per item
	n    uint64 // items added, for diagnostics
}

// New sizes a filter for n expected items at the given false-positive rate.
//
// The classic sizing: m = -n·ln(p)/ln(2)², k = (m/n)·ln 2. Both are clamped so a
// degenerate input (no items, an impossible rate) still yields a usable filter
// rather than a division by zero — this runs at boot, and a panic there would take
// the process down over a cache.
func New(n uint64, p float64) *Filter {
	if n < 1 {
		n = 1
	}
	if p <= 0 || p >= 1 {
		p = 0.01
	}
	ln2 := math.Ln2
	m := uint64(math.Ceil(-float64(n) * math.Log(p) / (ln2 * ln2)))
	if m < 64 {
		m = 64
	}
	k := uint64(math.Round(float64(m) / float64(n) * ln2))
	if k < 1 {
		k = 1
	}
	if k > 16 {
		k = 16 // beyond this the extra hashes cost more than the accuracy they buy
	}
	return &Filter{bits: make([]uint64, (m+63)/64), m: m, k: k}
}

// hashes derives k index positions from two independent 64-bit hashes.
//
// Kirsch-Mitzenmacher: h_i = h1 + i·h2. It gives k effectively-independent
// positions from two hash computations instead of k, which is the difference
// between this being free and being worth thinking about on a hot path.
func (f *Filter) positions(s string) []uint64 {
	a := fnv.New64a()
	_, _ = a.Write([]byte(s))
	h1 := a.Sum64()

	b := fnv.New64()
	_, _ = b.Write([]byte(s))
	// Salted so the second hash is not a near-duplicate of the first for short
	// strings, which usernames are.
	_, _ = b.Write([]byte{0x9e, 0x37, 0x79, 0xb9})
	h2 := b.Sum64()
	if h2 == 0 {
		h2 = 0x9e3779b97f4a7c15 // a zero step would make every position identical
	}

	out := make([]uint64, f.k)
	for i := uint64(0); i < f.k; i++ {
		out[i] = (h1 + i*h2) % f.m
	}
	return out
}

// Add records an item. Not safe to call concurrently with itself.
func (f *Filter) Add(s string) {
	for _, p := range f.positions(s) {
		f.bits[p/64] |= 1 << (p % 64)
	}
	f.n++
}

// Has reports whether the item MIGHT be present.
//
//	false → definitely absent (exact)
//	true  → probably present; confirm against the real store
func (f *Filter) Has(s string) bool {
	for _, p := range f.positions(s) {
		if f.bits[p/64]&(1<<(p%64)) == 0 {
			return false
		}
	}
	return true
}

// Len is how many items were added. Diagnostics only — a filter cannot enumerate
// or count its members, and this is just the Add tally.
func (f *Filter) Len() uint64 { return f.n }

// Bits is the filter's size in bits, for logging how much memory the cache costs.
func (f *Filter) Bits() uint64 { return f.m }

// Hashes is k, the number of positions per item.
func (f *Filter) Hashes() uint64 { return f.k }
