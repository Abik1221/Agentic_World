package adminapi_test

import (
	"testing"

	"github.com/agent-arena/arena/internal/adminapi"
	"github.com/agent-arena/arena/internal/payout"
)

// The overview reaches the custody breakdown through a RUNTIME type assertion
// (`h.treasury.(TreasuryDetailReader)`), which is what keeps adminapi independent of the
// payout package and lets a deployment with no Solana rail simply not implement it.
//
// The cost of that indirection is that a drifted method signature does not break the
// build — it makes the assertion fail silently, and every custody field on the admin
// dashboard reads as zero: no vault, no sweep needed, no SOL, payouts_funded_ok false.
// That is indistinguishable, on the screen, from a platform holding nothing.
//
// So the contract is pinned here instead. This is a test-only import: production code in
// adminapi still never depends on payout.
func TestSolvencyMonitorSatisfiesTreasuryInterfaces(t *testing.T) {
	var m *payout.SolvencyMonitor

	var _ adminapi.TreasuryReader = m
	var _ adminapi.TreasuryDetailReader = m

	// Also assert it through the dynamic path the handler actually takes, so this fails
	// for the same reason the dashboard would.
	var r adminapi.TreasuryReader = m
	if _, ok := r.(adminapi.TreasuryDetailReader); !ok {
		t.Fatal("*payout.SolvencyMonitor no longer satisfies TreasuryDetailReader; the admin custody panel would silently render zeroes")
	}
}
