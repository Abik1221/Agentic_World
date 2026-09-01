package bloom

import (
	"fmt"
	"testing"
)

// The whole safety argument for using this on a live availability check is "no false
// negatives". If that ever stops holding, the username field starts telling people a
// taken name is free — so it is the first thing pinned.
func TestNoFalseNegatives(t *testing.T) {
	f := New(5_000, 0.01)
	added := make([]string, 0, 5_000)
	for i := 0; i < 5_000; i++ {
		s := fmt.Sprintf("user_%d", i)
		f.Add(s)
		added = append(added, s)
	}
	for _, s := range added {
		if !f.Has(s) {
			t.Fatalf("Has(%q) = false for an item that was added — a false negative makes "+
				"the fast path unsound, because it would report a taken username as free", s)
		}
	}
}

// The false-positive rate has to be near what was asked for. Too high and every
// keystroke falls through to the database anyway, which defeats the point.
func TestFalsePositiveRateIsNearTheTarget(t *testing.T) {
	const n = 10_000
	const target = 0.01
	f := New(n, target)
	for i := 0; i < n; i++ {
		f.Add(fmt.Sprintf("taken_%d", i))
	}
	var fp int
	const probes = 50_000
	for i := 0; i < probes; i++ {
		if f.Has(fmt.Sprintf("free_%d", i)) {
			fp++
		}
	}
	rate := float64(fp) / probes
	// Generous ceiling: this asserts the sizing math is right, not that the hash is
	// cryptographic. A rate of 3× target would mean k or m is being computed wrongly.
	if rate > target*3 {
		t.Fatalf("false-positive rate %.4f, want <= %.4f (m=%d k=%d) — the sizing math is off",
			rate, target*3, f.Bits(), f.Hashes())
	}
}

// An empty filter must answer "absent" for everything, which is what makes a
// cold-start safe: before the first rebuild the check just falls through to the
// database rather than claiming names are taken.
func TestEmptyFilterSaysAbsent(t *testing.T) {
	f := New(1_000, 0.01)
	for _, s := range []string{"", "a", "someone", "usr_01H8"} {
		if f.Has(s) {
			t.Fatalf("empty filter reported %q as present", s)
		}
	}
}

// Degenerate sizing must not panic. This is built at boot from a row count that can
// legitimately be zero, and taking the process down over a cache would be worse than
// any cache miss.
func TestDegenerateSizingIsUsable(t *testing.T) {
	for _, tc := range []struct {
		n uint64
		p float64
	}{{0, 0.01}, {1, 0}, {1, 1}, {0, -5}, {1, 1.5}} {
		f := New(tc.n, tc.p)
		if f.Bits() == 0 || f.Hashes() == 0 {
			t.Fatalf("New(%d, %v) produced an unusable filter (m=%d k=%d)", tc.n, tc.p, f.Bits(), f.Hashes())
		}
		f.Add("x")
		if !f.Has("x") {
			t.Fatalf("New(%d, %v): added item not found", tc.n, tc.p)
		}
	}
}

// Short strings are the actual workload — usernames, three characters and up — so the
// two hashes must not degenerate into one for them.
func TestShortStringsSpreadAcrossPositions(t *testing.T) {
	f := New(1_000, 0.01)
	seen := map[uint64]bool{}
	for _, s := range []string{"abc", "abd", "abe", "abf", "bbc"} {
		for _, p := range f.positions(s) {
			seen[p] = true
		}
	}
	// Five short strings at k positions each should not collapse onto a handful of bits.
	if len(seen) < 10 {
		t.Fatalf("only %d distinct positions for 5 short strings — the second hash is not "+
			"independent enough for the sizes this filter actually holds", len(seen))
	}
}
