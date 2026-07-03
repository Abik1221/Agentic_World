package payout_test

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/payout"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

var now = time.Unix(1_700_000_000, 0).UTC()

type fakeRepo struct {
	withdrawable int64
	owner        string
	connect      string
	flagged      bool
	debt         int64
	requestedAt  time.Time
	rows         map[string]*payout.Withdrawal
}

func newRepo() *fakeRepo {
	return &fakeRepo{owner: "usr_a", connect: "acct_1", withdrawable: 1000,
		requestedAt: now.Add(-48 * time.Hour), rows: map[string]*payout.Withdrawal{}}
}

func (r *fakeRepo) Withdrawable(context.Context, string) (int64, error) { return r.withdrawable, nil }
func (r *fakeRepo) AgentOwner(context.Context, string) (string, string, error) {
	return r.owner, r.connect, nil
}
func (r *fakeRepo) AgentFlagged(context.Context, string) (bool, error)    { return r.flagged, nil }
func (r *fakeRepo) OutstandingDebt(context.Context, string) (int64, error) { return r.debt, nil }
func (r *fakeRepo) Create(_ context.Context, w payout.Withdrawal) error {
	w.RequestedAt = r.requestedAt
	cp := w
	r.rows[w.PublicID] = &cp
	return nil
}
func (r *fakeRepo) Get(_ context.Context, id string) (payout.Withdrawal, error) {
	w, ok := r.rows[id]
	if !ok {
		return payout.Withdrawal{}, payout.ErrNotFound
	}
	return *w, nil
}
func (r *fakeRepo) SetStatus(_ context.Context, id, from, to, transferID, _ string) (bool, error) {
	w, ok := r.rows[id]
	if !ok || w.Status != from {
		return false, nil
	}
	w.Status = to
	if transferID != "" {
		w.TransferID = transferID
	}
	return true, nil
}
func (r *fakeRepo) Audit(context.Context, string, string, string, []byte) error { return nil }
func (r *fakeRepo) ListByOwner(context.Context, string, int) ([]payout.Withdrawal, error) {
	return nil, nil
}
func (r *fakeRepo) ListByStatus(context.Context, string, int) ([]payout.Withdrawal, error) {
	return nil, nil
}

type fakeBank struct{ held, released, paid map[string]int64 }

func newBank() *fakeBank {
	return &fakeBank{held: map[string]int64{}, released: map[string]int64{}, paid: map[string]int64{}}
}
func (b *fakeBank) Hold(_ context.Context, id, _ string, coins int64) error { b.held[id] = coins; return nil }
func (b *fakeBank) Release(_ context.Context, id, _ string, coins int64) error {
	b.released[id] = coins
	return nil
}
func (b *fakeBank) Payout(_ context.Context, id, _ string, coins, _ int64) error {
	b.paid[id] = coins
	return nil
}

type fakeXfer struct {
	calls int
	fail  bool
}

func (x *fakeXfer) Transfer(context.Context, string, int64, string) (string, error) {
	x.calls++
	if x.fail {
		return "", errors.New("stripe down")
	}
	return "tr_1", nil
}

func newSvc(repo payout.Repo, bank payout.Bank, xfer payout.Transferrer) *payout.Service {
	return payout.New(repo, bank, xfer, platform.FixedClock{T: now},
		payout.Config{CoinCents: 1, SellFeePct: 10, StripeFeeFlatCents: 25, MinCoins: 500, Clearing: 24 * time.Hour},
		slog.Default(), prometheus.NewRegistry())
}

func TestQuoteAppliesFeesAndStripeFee(t *testing.T) {
	repo := newRepo()
	repo.withdrawable = 1000
	_, q, err := newSvc(repo, newBank(), &fakeXfer{}).Available(context.Background(), "ag_a", 0)
	if err != nil {
		t.Fatal(err)
	}
	// 1000 coins → gross 1000¢; 10% platform fee = 100 coins; flat Stripe fee 25¢.
	// net = (1000-100)*1 - 25 = 875.
	if q.GrossCents != 1000 || q.FeeCoins != 100 || q.StripeFeeCents != 25 || q.NetCents != 875 {
		t.Fatalf("quote = %+v, want gross1000 fee100 stripe25 net875", q)
	}
}

func TestRequestRejectsOverWinnings(t *testing.T) {
	repo := newRepo()
	repo.withdrawable = 600
	if _, err := newSvc(repo, newBank(), &fakeXfer{}).Request(context.Background(), "usr_a", "ag_a", 1000); err != payout.ErrInsufficient {
		t.Fatalf("over-winnings request = %v, want ErrInsufficient", err)
	}
}

func TestRequestRequiresKYCAndCleanAccount(t *testing.T) {
	ctx := context.Background()
	noKYC := newRepo()
	noKYC.connect = ""
	if _, err := newSvc(noKYC, newBank(), &fakeXfer{}).Request(ctx, "usr_a", "ag_a", 500); err != payout.ErrNoKYC {
		t.Fatalf("no-KYC = %v, want ErrNoKYC", err)
	}
	flagged := newRepo()
	flagged.flagged = true
	if _, err := newSvc(flagged, newBank(), &fakeXfer{}).Request(ctx, "usr_a", "ag_a", 500); err != payout.ErrFlagged {
		t.Fatalf("flagged = %v, want ErrFlagged", err)
	}
	notOwner := newRepo()
	if _, err := newSvc(notOwner, newBank(), &fakeXfer{}).Request(ctx, "usr_other", "ag_a", 500); err != payout.ErrForbiddenSelf {
		t.Fatalf("non-owner = %v, want ErrForbiddenSelf", err)
	}
	indebted := newRepo()
	indebted.debt = 200
	if _, err := newSvc(indebted, newBank(), &fakeXfer{}).Request(ctx, "usr_a", "ag_a", 500); err != payout.ErrDebt {
		t.Fatalf("indebted = %v, want ErrDebt", err)
	}
}

func TestRequestHoldsCoins(t *testing.T) {
	repo, bank := newRepo(), newBank()
	wd, err := newSvc(repo, bank, &fakeXfer{}).Request(context.Background(), "usr_a", "ag_a", 600)
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if bank.held[wd.PublicID] != 600 {
		t.Fatalf("held = %d, want 600", bank.held[wd.PublicID])
	}
	if wd.Status != "requested" || wd.NetCents != (600-60)-25 {
		t.Fatalf("withdrawal = %+v", wd)
	}
}

func TestApproveRespectsClearingThenPaysOnce(t *testing.T) {
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{}
	svc := newSvc(repo, bank, xfer)
	ctx := context.Background()

	// Fresh request (requested just now) → still clearing.
	repo.requestedAt = now
	wd, _ := svc.Request(ctx, "usr_a", "ag_a", 600)
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != payout.ErrClearing {
		t.Fatalf("approve during clearing = %v, want ErrClearing", err)
	}
	if xfer.calls != 0 {
		t.Fatal("no transfer should happen during the clearing window")
	}

	// Age it past the window → pays exactly once, idempotently.
	repo.rows[wd.PublicID].RequestedAt = now.Add(-48 * time.Hour)
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != nil {
		t.Fatalf("approve after clearing: %v", err)
	}
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if xfer.calls != 1 {
		t.Fatalf("transfers = %d, want exactly 1 (idempotent)", xfer.calls)
	}
	if bank.paid[wd.PublicID] != 600 {
		t.Fatalf("payout burn = %d, want 600", bank.paid[wd.PublicID])
	}
}

func TestApproveFailureReleasesCoins(t *testing.T) {
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{fail: true}
	svc := newSvc(repo, bank, xfer)
	ctx := context.Background()
	wd, _ := svc.Request(ctx, "usr_a", "ag_a", 600) // requestedAt = -48h (past clearing)

	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err == nil {
		t.Fatal("expected transfer failure to surface")
	}
	if bank.released[wd.PublicID] != 600 {
		t.Fatal("a failed payout must release the held coins")
	}
}

func TestRejectReleasesCoins(t *testing.T) {
	repo, bank := newRepo(), newBank()
	svc := newSvc(repo, bank, &fakeXfer{})
	ctx := context.Background()
	wd, _ := svc.Request(ctx, "usr_a", "ag_a", 600)

	if err := svc.Reject(ctx, "usr_admin", wd.PublicID, "looks fishy"); err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if bank.released[wd.PublicID] != 600 {
		t.Fatal("reject must release the held coins")
	}
	// Idempotent second reject.
	if err := svc.Reject(ctx, "usr_admin", wd.PublicID, ""); err != nil {
		t.Fatalf("re-reject: %v", err)
	}
}
