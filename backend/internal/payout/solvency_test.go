package payout_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/payout"
	"github.com/prometheus/client_golang/prometheus"
)

type fakeBalances struct{ base int64 }

func (f fakeBalances) TokenAccountBalance(context.Context, string) (int64, error) { return f.base, nil }

type fakeLiability struct{ cents int64 }

func (f fakeLiability) OutstandingLiabilityCents(context.Context) (int64, error) { return f.cents, nil }

func TestSolvencyCheck(t *testing.T) {
	// 11 USDC on-chain = 11_000_000 base = 1100 cents; liability 45 cents ⇒ solvent.
	m := payout.NewSolvencyMonitor(fakeLiability{cents: 45}, fakeBalances{base: 11_000_000}, "ata", slog.Default(), prometheus.NewRegistry())
	bal, lia, err := m.Check(context.Background())
	if err != nil || bal != 1100 || lia != 45 {
		t.Fatalf("bal=%d lia=%d err=%v; want 1100/45", bal, lia, err)
	}
	// Shortfall case: 10 cents on-chain, 45 owed.
	m2 := payout.NewSolvencyMonitor(fakeLiability{cents: 45}, fakeBalances{base: 100_000}, "ata", slog.Default(), prometheus.NewRegistry())
	bal2, lia2, _ := m2.Check(context.Background())
	if bal2 != 10 || lia2 != 45 {
		t.Fatalf("shortfall bal=%d lia=%d; want 10/45", bal2, lia2)
	}
}
