package agentgw

import "testing"

// TestConnectionCaps guards M2: the accept-slot counters enforce a per-IP and a
// global cap, and releasing frees the slot.
func TestConnectionCaps(t *testing.T) {
	g := New(nil, Options{MaxConns: 3, MaxConnsPerIP: 2}, nil)

	// Per-IP cap: 2 from the same IP succeed, the 3rd is rejected. Bind each
	// acquire to its own variable so BOTH run (|| short-circuits) and the two
	// expressions aren't identical (SA4000).
	first := g.acquireSlot("1.1.1.1")
	second := g.acquireSlot("1.1.1.1")
	if !first || !second {
		t.Fatal("first two slots for an IP should be granted")
	}
	if g.acquireSlot("1.1.1.1") {
		t.Fatal("third slot for the same IP must be rejected (per-IP cap)")
	}

	// A different IP can still connect until the global cap (3) is hit.
	if !g.acquireSlot("2.2.2.2") {
		t.Fatal("a fresh IP should get a slot under the global cap")
	}
	if g.acquireSlot("2.2.2.2") {
		t.Fatal("global cap (3) reached — further slots must be rejected")
	}

	// Releasing one frees a global slot and the per-IP slot.
	g.releaseSlot("1.1.1.1")
	if !g.acquireSlot("2.2.2.2") {
		t.Fatal("after a release, a new slot should be grantable")
	}
}
