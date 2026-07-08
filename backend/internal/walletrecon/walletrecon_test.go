package walletrecon_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/walletrecon"
)

type fakeRepo struct {
	count, coins, orphans, ledgerCoins int64
	stuck                              map[string]int64
}

func (r *fakeRepo) DepositTotals(context.Context) (int64, int64, int64, error) {
	return r.count, r.coins, r.orphans, nil
}
func (r *fakeRepo) LedgerSolanaCoins(context.Context) (int64, error) { return r.ledgerCoins, nil }
func (r *fakeRepo) StuckWithdrawals(_ context.Context, status string, _ time.Duration) (int64, error) {
	return r.stuck[status], nil
}

func newSvc(r walletrecon.Repo) *walletrecon.Service {
	return walletrecon.New(r, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
}

func TestReconcileClean(t *testing.T) {
	r := &fakeRepo{count: 3, coins: 1500, ledgerCoins: 1500, stuck: map[string]int64{}}
	rep, err := newSvc(r).Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Healthy() {
		t.Fatalf("expected healthy, got %+v", rep)
	}
	if rep.CoinDrift != 0 || rep.OrphanDeposits != 0 {
		t.Fatalf("unexpected drift: %+v", rep)
	}
}

func TestReconcileDetectsCoinDrift(t *testing.T) {
	// Ledger credited fewer coins than the on-chain deposits recorded.
	r := &fakeRepo{count: 3, coins: 1500, ledgerCoins: 1400, stuck: map[string]int64{}}
	rep, _ := newSvc(r).Reconcile(context.Background())
	if rep.Healthy() {
		t.Fatal("expected drift to be flagged")
	}
	if rep.CoinDrift != 100 {
		t.Fatalf("CoinDrift = %d, want 100", rep.CoinDrift)
	}
}

func TestReconcileDetectsOrphanAndStuck(t *testing.T) {
	r := &fakeRepo{
		count: 2, coins: 1000, ledgerCoins: 1000, orphans: 1,
		stuck: map[string]int64{"broadcasted": 2, "processing": 1},
	}
	rep, _ := newSvc(r).Reconcile(context.Background())
	if rep.Healthy() {
		t.Fatal("expected unhealthy")
	}
	if rep.OrphanDeposits != 1 || rep.StuckBroadcasted != 2 || rep.StuckProcessing != 1 {
		t.Fatalf("unexpected report: %+v", rep)
	}
}
