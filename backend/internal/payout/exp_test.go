package payout

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

type capBal struct{ base int64 }

func (c capBal) TokenAccountBalance(context.Context, string) (int64, error) { return c.base, nil }

type capRepo struct{ cents int64 }

func (c capRepo) OutstandingLiabilityCents(context.Context) (int64, error) { return c.cents, nil }

// The cap must trip on the BALANCE, not on the balance net of liability: a compromised
// key drains the whole account regardless of what we happen to owe.
func TestExposureCapTripsOnBalanceNotNetOfLiability(t *testing.T) {
	// 500.00 USDC on chain = 50_000 cents = 500_000_000 base units (6dp).
	m := NewSolvencyMonitor(capRepo{cents: 49_000}, capBal{base: 500_000_000},
		"ata", slog.Default(), prometheus.NewRegistry())
	m.SetExposureCap(10_000) // $100 ceiling

	bal, _, err := m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if bal != 50_000 {
		t.Fatalf("balance cents = %d, want 50000", bal)
	}
	// Net of liability the wallet is only $10 "extra" — but $500 is what an attacker
	// walks away with, and that is what the cap is defending.
	got := testutilGauge(t, m)
	if got != 40_000 {
		t.Fatalf("excess exposure = %d cents, want 40000 (balance 50000 - cap 10000)", got)
	}
}

func TestExposureCapZeroDisablesAndUnderCapIsQuiet(t *testing.T) {
	// Under the cap: no excess.
	m := NewSolvencyMonitor(capRepo{cents: 0}, capBal{base: 50_000_000}, // $50
		"ata", slog.Default(), prometheus.NewRegistry())
	m.SetExposureCap(10_000) // $100
	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g := testutilGauge(t, m); g != 0 {
		t.Fatalf("under cap should report 0 excess, got %d", g)
	}

	// Cap 0 = disabled: a huge balance must not report excess.
	m2 := NewSolvencyMonitor(capRepo{cents: 0}, capBal{base: 900_000_000_000},
		"ata", slog.Default(), prometheus.NewRegistry())
	if _, _, err := m2.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if g := testutilGauge(t, m2); g != 0 {
		t.Fatalf("cap=0 must disable the check, got excess %d", g)
	}
}

func testutilGauge(t *testing.T, m *SolvencyMonitor) int64 {
	t.Helper()
	var pb dto.Metric
	if err := m.expGauge.Write(&pb); err != nil {
		t.Fatal(err)
	}
	return int64(pb.GetGauge().GetValue())
}
