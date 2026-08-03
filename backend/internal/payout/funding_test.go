package payout_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/payout"
)

// stubFunding stands in for the solvency monitor's CanPay.
type stubFunding struct {
	ok     bool
	reason string
	asked  []int64
}

func (s *stubFunding) CanPay(amountCents int64) (bool, string) {
	s.asked = append(s.asked, amountCents)
	return s.ok, s.reason
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	var ae *httpx.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v is not an APIError; the admin UI needs a code to branch on", err)
	}
	return ae.Code
}

// The point of the gate: an approval the payout wallet cannot settle is refused BEFORE
// the transfer is built.
//
// Without it the approval succeeds, the broadcast fails, and the user is told their
// withdrawal FAILED for what was purely an operational shortfall on our side — recorded
// against their payout history and needing a manual retry.
func TestApproveRefusedWhenPayoutWalletCannotSettle(t *testing.T) {
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{}
	svc := newSvc(repo, bank, xfer)
	fund := &stubFunding{ok: false, reason: "the payout wallet holds $1.00 and this cash-out needs $5.40 — top up the float from cold storage, then approve"}
	svc.SetFunding(fund)
	ctx := context.Background()

	wd, err := svc.Request(ctx, "usr_a", "ag_a", 600) // requestedAt = -48h, past clearing
	if err != nil {
		t.Fatal(err)
	}

	err = svc.Approve(ctx, "usr_admin", wd.PublicID)
	if err == nil {
		t.Fatal("approve succeeded with an unfunded payout wallet")
	}
	if code := apiCode(t, err); code != "treasury_unfunded" {
		t.Fatalf("error code = %q, want treasury_unfunded", code)
	}
	// The operator must be told WHICH asset to move; a generic message sends them to
	// move the wrong one.
	if !strings.Contains(err.Error(), "top up the float") {
		t.Fatalf("message = %q; it must carry the actionable reason", err.Error())
	}
	// Nothing was broadcast.
	if xfer.calls != 0 {
		t.Fatalf("transfers = %d; a refused approval must not build a transfer", xfer.calls)
	}
	// It was asked about the NET amount — what actually leaves the wallet, not the gross.
	if len(fund.asked) != 1 || fund.asked[0] != wd.NetCents {
		t.Fatalf("funding asked about %v, want one call for net %d", fund.asked, wd.NetCents)
	}
}

// The refusal must leave the withdrawal exactly as it was, so it is still approvable the
// moment the float is topped up. A gate that released the coins or moved the row to a
// terminal state would turn a treasury task into a support ticket.
func TestRefusedApprovalLeavesWithdrawalApprovable(t *testing.T) {
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{}
	svc := newSvc(repo, bank, xfer)
	fund := &stubFunding{ok: false, reason: "not enough float"}
	svc.SetFunding(fund)
	ctx := context.Background()

	wd, err := svc.Request(ctx, "usr_a", "ag_a", 600)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err == nil {
		t.Fatal("expected the unfunded refusal")
	}
	// Coins stay held: not released (which would hand them back as spendable) and not
	// burned (which would pay a withdrawal that never went out).
	if n := bank.released[wd.PublicID]; n != 0 {
		t.Fatalf("released %d coins on a refused approval; the hold must stand", n)
	}
	if n := bank.paid[wd.PublicID]; n != 0 {
		t.Fatalf("burned %d coins without a transfer", n)
	}

	// Top up the treasury and approve again — it must now go through, with no
	// intervention on the row itself.
	fund.ok, fund.reason = true, ""
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != nil {
		t.Fatalf("approve after top-up: %v", err)
	}
	if xfer.calls != 1 {
		t.Fatalf("transfers = %d, want exactly 1 after the top-up", xfer.calls)
	}
	if bank.paid[wd.PublicID] != 600 {
		t.Fatalf("payout burn = %d, want 600", bank.paid[wd.PublicID])
	}
}

// A funded wallet must not change anything about the existing flow.
func TestApproveProceedsWhenFunded(t *testing.T) {
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{}
	svc := newSvc(repo, bank, xfer)
	svc.SetFunding(&stubFunding{ok: true})
	ctx := context.Background()

	wd, err := svc.Request(ctx, "usr_a", "ag_a", 600)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != nil {
		t.Fatalf("approve with a funded wallet: %v", err)
	}
	if xfer.calls != 1 {
		t.Fatalf("transfers = %d, want 1", xfer.calls)
	}
}

// No funding source wired — the Stripe/Dev rails, and every existing deployment that has
// not opted in — must behave exactly as before. This is the guarantee that adding the
// gate cannot disturb the devnet setup already running.
func TestNoFundingSourceLeavesApprovalUnchanged(t *testing.T) {
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{}
	svc := newSvc(repo, bank, xfer) // SetFunding never called
	ctx := context.Background()

	wd, err := svc.Request(ctx, "usr_a", "ag_a", 600)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != nil {
		t.Fatalf("approve with no funding source: %v", err)
	}
	if xfer.calls != 1 {
		t.Fatalf("transfers = %d, want 1", xfer.calls)
	}
}

// Policy refusals must win over the funding refusal. "We won't pay this" is a truer and
// more final answer than "we can't pay this yet", and an operator who sees the second for
// a flagged or still-clearing withdrawal would top up the treasury for nothing.
func TestPolicyRefusalsTakePrecedenceOverFunding(t *testing.T) {
	repo, bank, xfer := newRepo(), newBank(), &fakeXfer{}
	svc := newSvc(repo, bank, xfer)
	fund := &stubFunding{ok: false, reason: "not enough float"}
	svc.SetFunding(fund)
	ctx := context.Background()

	// Still inside the clearing window.
	repo.requestedAt = now
	wd, err := svc.Request(ctx, "usr_a", "ag_a", 600)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Approve(ctx, "usr_admin", wd.PublicID); err != payout.ErrClearing {
		t.Fatalf("approve = %v, want ErrClearing to win over the funding refusal", err)
	}
	if len(fund.asked) != 0 {
		t.Fatal("the funding check ran on a withdrawal that was going to be refused on policy anyway")
	}

	// Self-approval is likewise a policy answer, not a treasury one.
	repo.rows[wd.PublicID].RequestedAt = now.Add(-48 * time.Hour)
	if err := svc.Approve(ctx, "usr_a", wd.PublicID); err != payout.ErrSelfApproval {
		t.Fatalf("approve = %v, want ErrSelfApproval", err)
	}
}
