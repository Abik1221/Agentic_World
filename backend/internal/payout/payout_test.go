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
	withdrawable     int64
	owner            string
	connect          string
	primaryAgent     string
	wallet           string
	walletUnverified bool // when true, VerifiedWallet returns "" (linked but not proven)
	flagged          bool
	debt             int64
	requestedAt      time.Time
	walletVerifiedAt time.Time // when the destination wallet was verified (new-address cooldown)
	recentCount      int       // withdrawals in the velocity window (WithdrawnSince)
	recentCents      int64     // net cents withdrawn in the velocity window
	rows             map[string]*payout.Withdrawal
}

func newRepo() *fakeRepo {
	return &fakeRepo{owner: "usr_a", connect: "acct_1", withdrawable: 1000,
		primaryAgent: "ag_a", requestedAt: now.Add(-48 * time.Hour), rows: map[string]*payout.Withdrawal{}}
}

func (r *fakeRepo) Withdrawable(context.Context, string) (int64, error) { return r.withdrawable, nil }
func (r *fakeRepo) AgentOwner(context.Context, string) (string, string, error) {
	return r.owner, r.connect, nil
}
func (r *fakeRepo) PrimaryAgent(context.Context, string) (string, error)      { return r.primaryAgent, nil }
func (r *fakeRepo) DestinationWallet(context.Context, string) (string, error) { return r.wallet, nil }
func (r *fakeRepo) VerifiedWallet(context.Context, string) (string, error) {
	if r.walletUnverified {
		return "", nil
	}
	return r.wallet, nil // in the fake, a linked wallet is treated as verified
}
func (r *fakeRepo) VerifiedWalletAt(context.Context, string) (string, time.Time, error) {
	if r.walletUnverified {
		return "", time.Time{}, nil
	}
	return r.wallet, r.walletVerifiedAt, nil
}
func (r *fakeRepo) WithdrawnSince(context.Context, string, time.Time) (int, int64, error) {
	return r.recentCount, r.recentCents, nil
}
func (r *fakeRepo) AgentFlagged(context.Context, string) (bool, error)     { return r.flagged, nil }
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
func (r *fakeRepo) GetByTransferID(_ context.Context, transferID string) (payout.Withdrawal, error) {
	for _, w := range r.rows {
		if w.TransferID == transferID {
			return *w, nil
		}
	}
	return payout.Withdrawal{}, payout.ErrNotFound
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
func (r *fakeRepo) ListByStatus(_ context.Context, status string, _ int) ([]payout.Withdrawal, error) {
	var out []payout.Withdrawal
	for _, w := range r.rows {
		if w.Status == status {
			out = append(out, *w)
		}
	}
	return out, nil
}
func (r *fakeRepo) PendingByConnectAccount(_ context.Context, acct string) ([]payout.Withdrawal, error) {
	var out []payout.Withdrawal
	for _, w := range r.rows {
		if w.ConnectAccount == acct && w.Status == "requested" {
			out = append(out, *w)
		}
	}
	return out, nil
}

// WithOwnerLock in the fake just runs fn inline (single-threaded tests need no
// real serialization); the real serialization is exercised in the live store test.
func (r *fakeRepo) WithOwnerLock(_ context.Context, _ string, fn func() error) error { return fn() }

type fakeBank struct{ held, released, paid, reversed map[string]int64 }

func newBank() *fakeBank {
	return &fakeBank{held: map[string]int64{}, released: map[string]int64{}, paid: map[string]int64{}, reversed: map[string]int64{}}
}
func (b *fakeBank) Hold(_ context.Context, id, _ string, coins int64) error {
	b.held[id] = coins
	return nil
}
func (b *fakeBank) Release(_ context.Context, id, _ string, coins int64) error {
	b.released[id] = coins
	return nil
}
func (b *fakeBank) Payout(_ context.Context, id, _ string, coins, _ int64) error {
	b.paid[id] = coins
	return nil
}
func (b *fakeBank) ReversePayout(_ context.Context, id, _ string, coins, _ int64) error {
	b.reversed[id] = coins
	return nil
}

type fakeXfer struct {
	calls           int
	fail            bool
	ambiguousSig    string // non-empty → Transfer returns a BroadcastAmbiguousError with this sig
	payoutsDisabled bool   // zero value = payouts enabled, so existing tests are unaffected
}

func (x *fakeXfer) Transfer(context.Context, string, int64, string) (string, error) {
	x.calls++
	if x.ambiguousSig != "" {
		return "", &payout.BroadcastAmbiguousError{Signature: x.ambiguousSig}
	}
	if x.fail {
		return "", errors.New("stripe down")
	}
	return "tr_1", nil
}
func (x *fakeXfer) PayoutsEnabled(context.Context, string) (bool, error) {
	return !x.payoutsDisabled, nil
}

// TransferPreCommit mirrors the Solana rail: sign, invoke onSigned (record), then
// "send". A pre-sign failure (x.fail) errors BEFORE onSigned; an ambiguous send
// error records first (signature known) then fails — so escrow is never released.
func (x *fakeXfer) TransferPreCommit(ctx context.Context, dest string, cents int64, idem string, onSigned func(string) error) (string, error) {
	x.calls++
	if x.fail {
		return "", errors.New("solana down") // definitive pre-broadcast failure (not signed)
	}
	if x.ambiguousSig != "" {
		if onSigned != nil {
			if e := onSigned(x.ambiguousSig); e != nil {
				return "", e
			}
		}
		return "", &payout.BroadcastAmbiguousError{Signature: x.ambiguousSig}
	}
	if onSigned != nil {
		if e := onSigned("tr_1"); e != nil {
			return "", e
		}
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
	_, q, err := newSvc(repo, newBank(), &fakeXfer{}).Available(context.Background(), "usr_a", "ag_a", 0)
	if err != nil {
		t.Fatal(err)
	}
	// 1000 coins → gross 1000¢; 10% platform fee = 100 coins; flat Stripe fee 25¢.
	// net = (1000-100)*1 - 25 = 875.
	if q.GrossCents != 1000 || q.FeeCoins != 100 || q.StripeFeeCents != 25 || q.NetCents != 875 {
		t.Fatalf("quote = %+v, want gross1000 fee100 stripe25 net875", q)
	}
}

func TestAvailableDefaultsToPrimaryAgentForReadOnlyQuote(t *testing.T) {
	repo := newRepo()
	repo.withdrawable = 750
	avail, q, err := newSvc(repo, newBank(), &fakeXfer{}).Available(context.Background(), "usr_a", "", 500)
	if err != nil {
		t.Fatal(err)
	}
	if avail != 750 || q.Coins != 500 {
		t.Fatalf("avail/quote = %d/%+v, want avail 750 quote coins 500", avail, q)
	}

	noAgent := newRepo()
	noAgent.primaryAgent = ""
	avail, q, err = newSvc(noAgent, newBank(), &fakeXfer{}).Available(context.Background(), "usr_a", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if avail != 0 || q.Coins != 0 || q.NetCents != 0 {
		t.Fatalf("no-agent avail/quote = %d/%+v, want zero quote", avail, q)
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
	// IDOR guard (M6): reading another user's withdrawable balance is forbidden.
	if _, _, err := newSvc(newRepo(), newBank(), &fakeXfer{}).Available(ctx, "usr_other", "ag_a", 0); err != payout.ErrForbiddenSelf {
		t.Fatalf("Available non-owner = %v, want ErrForbiddenSelf", err)
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

// ── KYC capability gate + payout reversal reconciliation ───────────────────────

func TestApproveBlockedUntilPayoutsEnabled(t *testing.T) {
	repo, bank := newRepo(), newBank()
	xfer := &fakeXfer{payoutsDisabled: true} // Connect account exists but KYC incomplete
	svc := newSvc(repo, bank, xfer)
	ctx := context.Background()

	wd, _ := svc.Request(ctx, "usr_a", "ag_a", 600) // requestedAt = -48h (past clearing)
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != payout.ErrNoKYC {
		t.Fatalf("approve with payouts disabled = %v, want ErrNoKYC", err)
	}
	if xfer.calls != 0 {
		t.Fatal("must not attempt a transfer when payouts are not enabled")
	}
	if _, burned := bank.paid[wd.PublicID]; burned {
		t.Fatal("must not burn coins when payouts are not enabled")
	}
}

func TestReverseByTransferReCreditsExactlyOnce(t *testing.T) {
	repo, bank := newRepo(), newBank()
	svc := newSvc(repo, bank, &fakeXfer{})
	ctx := context.Background()

	// Pay a withdrawal so it is in "paid" with transfer id "tr_1".
	wd, _ := svc.Request(ctx, "usr_a", "ag_a", 600)
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if repo.rows[wd.PublicID].Status != "paid" {
		t.Fatalf("want paid, got %s", repo.rows[wd.PublicID].Status)
	}

	// Stripe reverses the transfer → re-credit the coins and mark reversed.
	if err := svc.ReverseByTransfer(ctx, "tr_1", "transfer.reversed"); err != nil {
		t.Fatalf("reverse: %v", err)
	}
	if bank.reversed[wd.PublicID] != 600 {
		t.Fatalf("re-credited %d, want 600", bank.reversed[wd.PublicID])
	}
	if repo.rows[wd.PublicID].Status != "reversed" {
		t.Fatalf("status = %s, want reversed", repo.rows[wd.PublicID].Status)
	}

	// Idempotent: a webhook redelivery re-credits nothing more.
	bank.reversed[wd.PublicID] = 0
	if err := svc.ReverseByTransfer(ctx, "tr_1", "transfer.reversed"); err != nil {
		t.Fatalf("reverse redelivery: %v", err)
	}
	if bank.reversed[wd.PublicID] != 0 {
		t.Fatal("already-reversed withdrawal must not re-credit again")
	}

	// An unknown transfer id is a safe no-op.
	if err := svc.ReverseByTransfer(ctx, "tr_unknown", "transfer.reversed"); err != nil {
		t.Fatalf("unknown transfer: %v", err)
	}
}

func TestOnAccountUpdatedAutoApprovesPending(t *testing.T) {
	ctx := context.Background()

	// KYC completes (payouts now enabled) → the pending withdrawal auto-clears.
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{}
	svc := newSvc(repo, bank, xfer)
	wd, _ := svc.Request(ctx, "usr_a", "ag_a", 600) // connect "acct_1", requested, past clearing
	if err := svc.OnAccountUpdated(ctx, "acct_1", true); err != nil {
		t.Fatalf("OnAccountUpdated: %v", err)
	}
	if xfer.calls != 1 {
		t.Fatalf("transfer calls = %d, want 1 (auto-approved)", xfer.calls)
	}
	if repo.rows[wd.PublicID].Status != "paid" || bank.paid[wd.PublicID] != 600 {
		t.Fatalf("want paid+burned 600, got status=%s burned=%d", repo.rows[wd.PublicID].Status, bank.paid[wd.PublicID])
	}

	// payouts_enabled=false is a no-op — nothing auto-approves.
	repo2, bank2, xfer2 := newRepo(), newBank(), &fakeXfer{}
	svc2 := newSvc(repo2, bank2, xfer2)
	wd2, _ := svc2.Request(ctx, "usr_a", "ag_a", 600)
	if err := svc2.OnAccountUpdated(ctx, "acct_1", false); err != nil {
		t.Fatalf("OnAccountUpdated(disabled): %v", err)
	}
	if xfer2.calls != 0 || repo2.rows[wd2.PublicID].Status != "requested" {
		t.Fatal("payouts-disabled account.updated must not approve anything")
	}
}
