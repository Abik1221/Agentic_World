package walletadmin_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/walletadmin"
)

type fakeRepo struct {
	s       walletadmin.Settings
	frozen  map[string]bool
	missing bool // SetFrozen returns changed=false
}

func newRepo() *fakeRepo {
	return &fakeRepo{
		s: walletadmin.Settings{
			DepositsEnabled: true, WithdrawalsEnabled: true,
			MinDepositBase: 1_000_000, MinWithdrawCoins: 500, MaxWithdrawCoins: 0, WithdrawFeePct: 10, Confirmations: 1,
		},
		frozen: map[string]bool{},
	}
}

func (r *fakeRepo) GetSettings(context.Context) (walletadmin.Settings, error) { return r.s, nil }
func (r *fakeRepo) UpdateSettings(_ context.Context, s walletadmin.Settings) error {
	r.s = s
	return nil
}
func (r *fakeRepo) SetFrozen(_ context.Context, user string, frozen bool) (bool, error) {
	if r.missing {
		return false, nil
	}
	r.frozen[user] = frozen
	return true, nil
}
func (r *fakeRepo) IsFrozen(_ context.Context, user string) (bool, error)       { return r.frozen[user], nil }
func (r *fakeRepo) Audit(context.Context, string, string, string, []byte) error { return nil }

type fakeAdjuster struct {
	calls int
	last  int64
	fail  bool
	seen  map[string]bool // models the ledger's idempotency-key dedup
}

func (a *fakeAdjuster) AdminAdjust(_ context.Context, _ string, coins int64, idem, _ string) error {
	if a.fail {
		return errors.New("ledger error")
	}
	if a.seen == nil {
		a.seen = map[string]bool{}
	}
	if a.seen[idem] {
		return nil // idempotent replay of the same key — no double-apply
	}
	a.seen[idem] = true
	a.calls++
	a.last = coins
	return nil
}

func newSvc(repo walletadmin.Repo, adj walletadmin.Adjuster) *walletadmin.Service {
	return walletadmin.New(repo, adj, platform.FixedClock{T: time.Unix(1_700_000_000, 0)},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestCheckDepositGate(t *testing.T) {
	ctx := context.Background()
	repo := newRepo()
	svc := newSvc(repo, nil)

	if err := svc.CheckDeposit(ctx, "usr_a", 5_000_000); err != nil {
		t.Fatalf("allowed deposit rejected: %v", err)
	}
	if err := svc.CheckDeposit(ctx, "usr_a", 500_000); err != walletadmin.ErrBelowMin {
		t.Fatalf("below-min: got %v", err)
	}
	// Frozen wallet.
	repo.frozen["usr_a"] = true
	if err := svc.CheckDeposit(ctx, "usr_a", 5_000_000); err != walletadmin.ErrFrozen {
		t.Fatalf("frozen: got %v", err)
	}
}

func TestCheckDepositDisabledAndMaintenance(t *testing.T) {
	ctx := context.Background()

	repo := newRepo()
	repo.s.DepositsEnabled = false
	if err := newSvc(repo, nil).CheckDeposit(ctx, "usr_a", 5_000_000); err != walletadmin.ErrDepositsDisabled {
		t.Fatalf("disabled: got %v", err)
	}

	repo2 := newRepo()
	repo2.s.MaintenanceMode = true
	if err := newSvc(repo2, nil).CheckDeposit(ctx, "usr_a", 5_000_000); err != walletadmin.ErrMaintenance {
		t.Fatalf("maintenance: got %v", err)
	}
}

func TestCheckWithdrawBounds(t *testing.T) {
	ctx := context.Background()
	repo := newRepo()
	repo.s.MinWithdrawCoins = 500
	repo.s.MaxWithdrawCoins = 10_000
	svc := newSvc(repo, nil)

	if err := svc.CheckWithdraw(ctx, "usr_a", 1000); err != nil {
		t.Fatalf("in-bounds rejected: %v", err)
	}
	if err := svc.CheckWithdraw(ctx, "usr_a", 100); err != walletadmin.ErrBelowMin {
		t.Fatalf("below-min: got %v", err)
	}
	if err := svc.CheckWithdraw(ctx, "usr_a", 20_000); err != walletadmin.ErrAboveMax {
		t.Fatalf("above-max: got %v", err)
	}
	// Frozen wallet is blocked regardless of bounds.
	repo.frozen["usr_a"] = true
	if err := svc.CheckWithdraw(ctx, "usr_a", 1000); err != walletadmin.ErrFrozen {
		t.Fatalf("frozen: got %v", err)
	}
}

func TestUpdateSettingsValidationAndCache(t *testing.T) {
	ctx := context.Background()
	svc := newSvc(newRepo(), nil)

	// max below min is rejected.
	bad := walletadmin.Settings{DepositsEnabled: true, WithdrawalsEnabled: true, MinWithdrawCoins: 1000, MaxWithdrawCoins: 500}
	if _, err := svc.UpdateSettings(ctx, "admin", bad); err == nil {
		t.Fatal("expected validation error for max<min")
	}
	// A valid update takes effect immediately (cache invalidated).
	good := walletadmin.Settings{DepositsEnabled: true, WithdrawalsEnabled: false, MinWithdrawCoins: 500, WithdrawFeePct: 10}
	if _, err := svc.UpdateSettings(ctx, "admin", good); err != nil {
		t.Fatalf("valid update rejected: %v", err)
	}
	if err := svc.CheckWithdraw(ctx, "usr_a", 1000); err != walletadmin.ErrWithdrawalsDisabled {
		t.Fatalf("update not reflected: got %v", err)
	}
}

func TestSetFrozenNotFound(t *testing.T) {
	repo := newRepo()
	repo.missing = true
	if err := newSvc(repo, nil).SetFrozen(context.Background(), "admin", "usr_x", true); err != walletadmin.ErrUserNotFound {
		t.Fatalf("expected ErrUserNotFound, got %v", err)
	}
}

func TestAdjust(t *testing.T) {
	ctx := context.Background()
	adj := &fakeAdjuster{}
	svc := newSvc(newRepo(), adj)

	if err := svc.Adjust(ctx, "admin", "usr_a", 0, "noop", "k1"); err == nil {
		t.Fatal("expected error for zero adjustment")
	}
	// A missing idempotency key is rejected (M4: prevents double-apply on retry).
	if err := svc.Adjust(ctx, "admin", "usr_a", 250, "bonus", ""); err == nil {
		t.Fatal("expected error when idempotency_key is missing")
	}
	if err := svc.Adjust(ctx, "admin", "usr_a", 250, "bonus", "k1"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	// A retry that reuses the same key must NOT double-apply.
	if err := svc.Adjust(ctx, "admin", "usr_a", 250, "bonus", "k1"); err != nil {
		t.Fatalf("adjust retry: %v", err)
	}
	if adj.calls != 1 || adj.last != 250 {
		t.Fatalf("same key double-applied: calls=%d last=%d", adj.calls, adj.last)
	}
	// A distinct key is a genuinely new adjustment.
	if err := svc.Adjust(ctx, "admin", "usr_a", 100, "bonus2", "k2"); err != nil {
		t.Fatalf("adjust k2: %v", err)
	}
	if adj.calls != 2 {
		t.Fatalf("distinct key not applied: calls=%d", adj.calls)
	}
	// nil adjuster ⇒ unavailable.
	if err := newSvc(newRepo(), nil).Adjust(ctx, "admin", "usr_a", 100, "x", "k3"); err == nil {
		t.Fatal("expected error when adjuster is nil")
	}
}
