package payout

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func testLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type stubBalances struct{ base int64 }

func (s stubBalances) TokenAccountBalance(context.Context, string) (int64, error) {
	return s.base, nil
}

type stubLiability struct{ cents int64 }

func (s stubLiability) OutstandingLiabilityCents(context.Context) (int64, error) {
	return s.cents, nil
}

// Before any reconciliation lands, the reading must report NOT-OK rather than a zero
// balance. An operator reading "$0 in the treasury" during an incident would act very
// differently than one reading "we haven't checked yet", and the dashboard has no way
// to tell them apart unless this flag is honest.
func TestNoReadingIsUnknownNotZero(t *testing.T) {
	m := NewSolvencyMonitor(stubLiability{}, stubBalances{}, "ata", testLog(), prometheus.NewRegistry())
	bal, lia, at, ok := m.LastReading()
	if ok {
		t.Fatal("reported a reading before any reconciliation ran")
	}
	if bal != 0 || lia != 0 || !at.IsZero() {
		t.Fatalf("unread monitor returned %d/%d at %v; want zero values with ok=false", bal, lia, at)
	}
}

// After a check, the reading is available and in CENTS — USDC carries 6 decimals, so
// the conversion is 10^4 base units per cent and getting it wrong misreports the
// treasury by four orders of magnitude.
func TestReadingIsRecordedInCents(t *testing.T) {
	const oneDollarInBaseUnits = 1_000_000 // $1.00 of USDC
	m := NewSolvencyMonitor(
		stubLiability{cents: 250},
		stubBalances{base: oneDollarInBaseUnits},
		"ata", testLog(), prometheus.NewRegistry(),
	)
	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	bal, lia, at, ok := m.LastReading()
	if !ok {
		t.Fatal("no reading after a successful check")
	}
	if bal != 100 {
		t.Fatalf("balance = %d cents, want 100 ($1 of USDC)", bal)
	}
	if lia != 250 {
		t.Fatalf("liability = %d cents, want 250", lia)
	}
	if time.Since(at) > time.Minute {
		t.Fatalf("observedAt is stale on a fresh reading: %v", at)
	}
}
