package payout_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/payout"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// fakeConfirmer drives the on-chain confirmation result in tests.
type fakeConfirmer struct {
	finalized bool
	success   bool
}

func (c *fakeConfirmer) Confirm(context.Context, string) (bool, bool, error) {
	return c.finalized, c.success, nil
}

func newSolanaSvc(repo payout.Repo, bank payout.Bank, xfer payout.Transferrer, conf payout.Confirmer) *payout.Service {
	s := payout.New(repo, bank, xfer, platform.FixedClock{T: now},
		payout.Config{CoinCents: 1, SellFeePct: 10, StripeFeeFlatCents: 0, MinCoins: 500, Clearing: 24 * time.Hour, Chain: payout.ChainSolana},
		slog.Default(), prometheus.NewRegistry())
	if conf != nil {
		s.SetConfirmer(conf)
	}
	return s
}

func TestSolanaRequestNeedsWallet(t *testing.T) {
	repo := newRepo()
	repo.connect = "" // no Stripe connect (Privy user)
	repo.wallet = ""  // and no wallet linked yet
	svc := newSolanaSvc(repo, newBank(), &fakeXfer{}, nil)
	if _, err := svc.Request(context.Background(), "usr_a", "ag_a", 600); err != payout.ErrNoWallet {
		t.Fatalf("err = %v, want ErrNoWallet", err)
	}
	// With a wallet linked, the request succeeds and is stamped solana.
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	w, err := svc.Request(context.Background(), "usr_a", "ag_a", 600)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if w.Chain != payout.ChainSolana || w.DestWallet != repo.wallet {
		t.Fatalf("withdrawal not stamped solana: %+v", w)
	}
}

// W2: a linked-but-unverified wallet cannot receive a payout.
func TestSolanaRequestRequiresVerifiedWallet(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	repo.walletUnverified = true // linked, but ownership never proven
	svc := newSolanaSvc(repo, newBank(), &fakeXfer{}, nil)

	if _, err := svc.Request(context.Background(), "usr_a", "ag_a", 600); err != payout.ErrWalletNotVerified {
		t.Fatalf("err = %v, want ErrWalletNotVerified", err)
	}
	// Once verified, the request succeeds.
	repo.walletUnverified = false
	if _, err := svc.Request(context.Background(), "usr_a", "ag_a", 600); err != nil {
		t.Fatalf("verified request: %v", err)
	}
}

// M2: an admin who is also the withdrawal's owner cannot approve it (separation of
// duties). A different admin — or a Platform token (empty user id) — still can.
func TestApproveForbidsSelfApproval(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	bank, xfer := newBank(), &fakeXfer{}
	svc := newSolanaSvc(repo, bank, xfer, &fakeConfirmer{})

	w, _ := svc.Request(context.Background(), "usr_a", "ag_a", 600)

	// The owner (usr_a) approving their own withdrawal is rejected, and nothing is broadcast.
	if err := svc.Approve(context.Background(), "usr_a", w.PublicID); err != payout.ErrSelfApproval {
		t.Fatalf("self-approval err = %v, want ErrSelfApproval", err)
	}
	if xfer.calls != 0 {
		t.Fatalf("self-approval broadcast a payout: calls=%d", xfer.calls)
	}
	// A Platform-token admin (empty user id) is a distinct approver and succeeds.
	if err := svc.Approve(context.Background(), "", w.PublicID); err != nil {
		t.Fatalf("platform-token approve: %v", err)
	}
	if got, _ := repo.Get(context.Background(), w.PublicID); got.Status != "broadcasted" {
		t.Fatalf("status = %q, want broadcasted", got.Status)
	}
}

// Anti-drain velocity cap: at the per-window count limit, a new request is refused.
func TestWithdrawVelocityCountCap(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	repo.recentCount = 5 // already 5 withdrawals in the window
	svc := payout.New(repo, newBank(), &fakeXfer{}, platform.FixedClock{T: now},
		payout.Config{CoinCents: 1, SellFeePct: 10, MinCoins: 500, Clearing: 24 * time.Hour, Chain: payout.ChainSolana, MaxPerWindow: 5},
		slog.Default(), prometheus.NewRegistry())
	if _, err := svc.Request(context.Background(), "usr_a", "ag_a", 600); err != payout.ErrVelocity {
		t.Fatalf("err = %v, want ErrVelocity", err)
	}
	// Under the cap, it goes through.
	repo.recentCount = 4
	if _, err := svc.Request(context.Background(), "usr_a", "ag_a", 600); err != nil {
		t.Fatalf("under-cap request: %v", err)
	}
}

// New-address cooldown: a freshly-verified wallet is frozen for the cooldown window.
func TestWithdrawNewAddressCooldown(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	repo.walletVerifiedAt = now.Add(-1 * time.Hour) // verified 1h ago
	svc := payout.New(repo, newBank(), &fakeXfer{}, platform.FixedClock{T: now},
		payout.Config{CoinCents: 1, SellFeePct: 10, MinCoins: 500, Clearing: 24 * time.Hour, Chain: payout.ChainSolana, NewAddressCooldown: 24 * time.Hour},
		slog.Default(), prometheus.NewRegistry())
	if _, err := svc.Request(context.Background(), "usr_a", "ag_a", 600); err != payout.ErrAddressCooldown {
		t.Fatalf("err = %v, want ErrAddressCooldown", err)
	}
	// Once the wallet has aged past the cooldown, withdrawals are allowed.
	repo.walletVerifiedAt = now.Add(-25 * time.Hour)
	if _, err := svc.Request(context.Background(), "usr_a", "ag_a", 600); err != nil {
		t.Fatalf("post-cooldown request: %v", err)
	}
}

// M10: a withdrawal stranded in 'processing' past the grace is provably pre-broadcast
// (a send only happens after the row leaves 'processing'), so the reconciliation sweep
// safely releases its escrow. A recently-claimed 'processing' row is left alone.
func TestReconcileStuckProcessingReleases(t *testing.T) {
	repo := newRepo()
	bank := newBank()
	svc := newSolanaSvc(repo, bank, &fakeXfer{}, &fakeConfirmer{})

	repo.rows["wd_stuck"] = &payout.Withdrawal{
		PublicID: "wd_stuck", Agent: "ag_a", Owner: "usr_a", Coins: 600,
		Status: "processing", StatusChangedAt: now.Add(-5 * time.Minute),
	}
	repo.rows["wd_fresh"] = &payout.Withdrawal{
		PublicID: "wd_fresh", Agent: "ag_a", Owner: "usr_a", Coins: 600,
		Status: "processing", StatusChangedAt: now.Add(-10 * time.Second),
	}

	n, err := svc.ReconcileStuckProcessing(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("recovered = %d, err = %v; want exactly 1", n, err)
	}
	if repo.rows["wd_stuck"].Status != "failed" {
		t.Fatalf("stuck row status = %q, want failed", repo.rows["wd_stuck"].Status)
	}
	if bank.released["wd_stuck"] != 600 {
		t.Fatalf("stuck escrow not released: %+v", bank.released)
	}
	if repo.rows["wd_fresh"].Status != "processing" {
		t.Fatalf("fresh row wrongly touched: %q", repo.rows["wd_fresh"].Status)
	}
}

// M10: a 'broadcasted' withdrawal that never finalizes past the dead-broadcast
// window (blockhash lapsed → tx can never land) is safely released; a recent one
// keeps waiting.
func TestConfirmReleasesExpiredBroadcast(t *testing.T) {
	repo := newRepo()
	bank := newBank()
	// confirmer reports NOT finalized (tx never landed / not found).
	svc := newSolanaSvc(repo, bank, &fakeXfer{}, &fakeConfirmer{finalized: false})

	// stale broadcasted row (became broadcasted 5m ago) → released.
	repo.rows["wd_dead"] = &payout.Withdrawal{
		PublicID: "wd_dead", Agent: "ag_a", Owner: "usr_a", Coins: 600, NetCents: 540,
		Status: "broadcasted", TransferID: "sigDead", StatusChangedAt: now.Add(-5 * time.Minute),
	}
	// fresh broadcasted row (30s ago) → must keep waiting.
	repo.rows["wd_fresh"] = &payout.Withdrawal{
		PublicID: "wd_fresh", Agent: "ag_b", Owner: "usr_b", Coins: 600, NetCents: 540,
		Status: "broadcasted", TransferID: "sigFresh", StatusChangedAt: now.Add(-30 * time.Second),
	}

	if _, err := svc.ConfirmBroadcasted(context.Background()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if repo.rows["wd_dead"].Status != "failed" || bank.released["wd_dead"] != 600 {
		t.Fatalf("expired broadcast not released: status=%s released=%v", repo.rows["wd_dead"].Status, bank.released)
	}
	if repo.rows["wd_fresh"].Status != "broadcasted" {
		t.Fatalf("fresh broadcast wrongly released: %s", repo.rows["wd_fresh"].Status)
	}
}

func TestSolanaApproveBroadcastsWithoutBurning(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	bank, xfer := newBank(), &fakeXfer{}
	svc := newSolanaSvc(repo, bank, xfer, &fakeConfirmer{})

	w, _ := svc.Request(context.Background(), "usr_a", "ag_a", 600)
	if err := svc.Approve(context.Background(), "admin", w.PublicID); err != nil {
		t.Fatalf("approve: %v", err)
	}
	got, _ := repo.Get(context.Background(), w.PublicID)
	if got.Status != "broadcasted" {
		t.Fatalf("status = %q, want broadcasted", got.Status)
	}
	if got.TransferID == "" {
		t.Fatal("expected a recorded tx signature")
	}
	if len(bank.paid) != 0 {
		t.Fatalf("coins burned on broadcast (must wait for confirm): %+v", bank.paid)
	}
	if bank.held[w.PublicID] != 600 {
		t.Fatalf("coins not held: %+v", bank.held)
	}

	// Re-approving a broadcasted withdrawal must NOT broadcast again (double-spend).
	callsBefore := xfer.calls
	if err := svc.Approve(context.Background(), "admin", w.PublicID); err != nil {
		t.Fatalf("re-approve: %v", err)
	}
	if xfer.calls != callsBefore {
		t.Fatalf("re-approve re-broadcast: calls %d → %d", callsBefore, xfer.calls)
	}
}

func TestSolanaConfirmSuccessBurnsAndPays(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	bank := newBank()
	conf := &fakeConfirmer{}
	svc := newSolanaSvc(repo, bank, &fakeXfer{}, conf)

	w, _ := svc.Request(context.Background(), "usr_a", "ag_a", 600)
	_ = svc.Approve(context.Background(), "admin", w.PublicID)

	// Not finalized yet → no change.
	n, _ := svc.ConfirmBroadcasted(context.Background())
	if n != 0 || len(bank.paid) != 0 {
		t.Fatalf("settled while pending: n=%d paid=%+v", n, bank.paid)
	}

	// Finalized OK → burn + paid.
	conf.finalized, conf.success = true, true
	n, _ = svc.ConfirmBroadcasted(context.Background())
	got, _ := repo.Get(context.Background(), w.PublicID)
	if n != 1 || got.Status != "paid" || bank.paid[w.PublicID] != 600 {
		t.Fatalf("confirm-success wrong: n=%d status=%q paid=%+v", n, got.Status, bank.paid)
	}
}

func TestSolanaConfirmFailureReleasesEscrow(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	bank := newBank()
	conf := &fakeConfirmer{}
	svc := newSolanaSvc(repo, bank, &fakeXfer{}, conf)

	w, _ := svc.Request(context.Background(), "usr_a", "ag_a", 600)
	_ = svc.Approve(context.Background(), "admin", w.PublicID)

	// Finalized but failed on-chain → release + failed, no burn.
	conf.finalized, conf.success = true, false
	svc.ConfirmBroadcasted(context.Background())
	got, _ := repo.Get(context.Background(), w.PublicID)
	if got.Status != "failed" || bank.released[w.PublicID] != 600 || len(bank.paid) != 0 {
		t.Fatalf("confirm-failure wrong: status=%q released=%+v paid=%+v", got.Status, bank.released, bank.paid)
	}
}

// A DEFINITIVE (pre-broadcast) failure — nothing reached the network — releases the
// hold and fails the withdrawal.
func TestSolanaApproveReleasesOnDefinitiveError(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	bank, xfer := newBank(), &fakeXfer{fail: true}
	svc := newSolanaSvc(repo, bank, xfer, &fakeConfirmer{})

	w, _ := svc.Request(context.Background(), "usr_a", "ag_a", 600)
	if err := svc.Approve(context.Background(), "admin", w.PublicID); err == nil {
		t.Fatal("expected broadcast error")
	}
	got, _ := repo.Get(context.Background(), w.PublicID)
	if got.Status != "failed" || bank.released[w.PublicID] != 600 {
		t.Fatalf("definitive broadcast error not released: status=%q released=%+v", got.Status, bank.released)
	}
}

// M3: an AMBIGUOUS broadcast error (send RPC failed but the signed tx may have
// landed) must NEVER release escrow — that would double-pay if the tx is on-chain.
// The withdrawal stays 'broadcasted' with the signature recorded so the confirm
// watcher settles it from the chain.
func TestSolanaAmbiguousBroadcastHoldsEscrow(t *testing.T) {
	repo := newRepo()
	repo.connect = ""
	repo.wallet = "9WzDXwBbmkg8ZTbNMqUxvQRAyrZzDsGYdLVL9zYtAWWM"
	bank, xfer := newBank(), &fakeXfer{ambiguousSig: "sig_amb"}
	svc := newSolanaSvc(repo, bank, xfer, &fakeConfirmer{})

	w, _ := svc.Request(context.Background(), "usr_a", "ag_a", 600)
	if err := svc.Approve(context.Background(), "admin", w.PublicID); err == nil {
		t.Fatal("expected the ambiguous broadcast error to surface")
	}
	got, _ := repo.Get(context.Background(), w.PublicID)
	if got.Status != "broadcasted" {
		t.Fatalf("status = %q, want broadcasted (held for confirmation, not released)", got.Status)
	}
	if got.TransferID != "sig_amb" {
		t.Fatalf("signature not recorded for reconciliation: %q", got.TransferID)
	}
	if len(bank.released) != 0 {
		t.Fatalf("ambiguous broadcast must NOT release escrow: %+v", bank.released)
	}
	if bank.held[w.PublicID] != 600 || len(bank.paid) != 0 {
		t.Fatalf("coins must stay held (not paid/released): held=%+v paid=%+v", bank.held, bank.paid)
	}

	// The confirm watcher then settles it exactly once: finalized-success → burn+paid.
	conf := &fakeConfirmer{finalized: true, success: true}
	svc2 := newSolanaSvc(repo, bank, xfer, conf)
	if _, err := svc2.ConfirmBroadcasted(context.Background()); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	got, _ = repo.Get(context.Background(), w.PublicID)
	if got.Status != "paid" || bank.paid[w.PublicID] != 600 {
		t.Fatalf("confirm did not settle the held tx: status=%q paid=%+v", got.Status, bank.paid)
	}
}
