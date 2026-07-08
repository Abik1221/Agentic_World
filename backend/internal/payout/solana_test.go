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

func TestSolanaApproveReleasesOnBroadcastError(t *testing.T) {
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
		t.Fatalf("broadcast error not released: status=%q released=%+v", got.Status, bank.released)
	}
}
