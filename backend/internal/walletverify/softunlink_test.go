package walletverify_test

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/walletverify"
)

// Removing a payout wallet must look GONE to the developer and still BE there for audit.
//
// It used to be a destructive UPDATE — verified_wallet_address, wallet_address and
// wallet_verified_at all set to NULL — so the address a developer had proven ownership of,
// and had been paid to, simply vanished along with any record that it was ever attached.
// A payout destination is exactly what a support ticket or a fraud review turns on months
// later, and nothing in the schema remembered.
//
// These drive the real Service. The SQL that writes the history row in the same
// transaction as the clear is NOT covered here — that needs Postgres.

type auditRepo struct {
	verified map[string]string
	hist     []histEntry
	clearErr error
}

type histEntry struct{ user, action, actor string }

func newAuditRepo() *auditRepo { return &auditRepo{verified: map[string]string{}} }

func (r *auditRepo) SaveChallenge(context.Context, string, string, string, time.Time) error {
	return nil
}
func (r *auditRepo) GetChallenge(context.Context, string) (walletverify.Challenge, bool, error) {
	return walletverify.Challenge{}, false, nil
}
func (r *auditRepo) MarkVerified(_ context.Context, user, wallet string, _ time.Time) error {
	r.verified[user] = wallet
	return nil
}
func (r *auditRepo) ClearChallenge(context.Context, string) error { return nil }

func (r *auditRepo) ClearVerified(_ context.Context, user, actor string) error {
	if r.clearErr != nil {
		return r.clearErr
	}
	if _, ok := r.verified[user]; ok {
		r.hist = append(r.hist, histEntry{user, "unlinked", actor})
	}
	delete(r.verified, user)
	return nil
}

func (r *auditRepo) RecordLinked(_ context.Context, user, actor string) error {
	r.hist = append(r.hist, histEntry{user, "linked", actor})
	return nil
}

func TestUnlinkRemovesTheWalletFromTheAccount(t *testing.T) {
	// The developer's half: after removal the account has no wallet, and every read that
	// decides where money may be sent sees exactly that. No is_deleted flag for a later
	// query to forget — which is the usual way a soft delete leaks back into a payout path.
	r := newAuditRepo()
	r.verified["usr_1"] = "Wa11etAddr"
	if err := walletverify.New(r, platform.FixedClock{T: time.Unix(0, 0)}).Unlink(context.Background(), "usr_1", "usr_1"); err != nil {
		t.Fatal(err)
	}
	if _, still := r.verified["usr_1"]; still {
		t.Fatal("the wallet is still on the account after removal")
	}
}

func TestUnlinkRecordsWhoRemovedIt(t *testing.T) {
	// Support removing a wallet on a developer's behalf and the developer removing their
	// own are different facts, and the history row is the only place that survives. The
	// actor must reach the repo unchanged rather than being assumed to be the user.
	r := newAuditRepo()
	r.verified["usr_1"] = "Wa11etAddr"
	if err := walletverify.New(r, platform.FixedClock{T: time.Unix(0, 0)}).Unlink(context.Background(), "usr_1", "usr_admin"); err != nil {
		t.Fatal(err)
	}
	if len(r.hist) != 1 {
		t.Fatalf("no audit row was written: %+v", r.hist)
	}
	if r.hist[0].actor != "usr_admin" {
		t.Fatalf("actor = %q — an operator removal must not read as the developer's own", r.hist[0].actor)
	}
	if r.hist[0].action != "unlinked" {
		t.Fatalf("action = %q, want unlinked", r.hist[0].action)
	}
}

func TestUnlinkOfNothingRecordsNothing(t *testing.T) {
	// Clicking remove twice must not write a second row claiming a wallet was removed.
	r := newAuditRepo()
	if err := walletverify.New(r, platform.FixedClock{T: time.Unix(0, 0)}).Unlink(context.Background(), "usr_1", "usr_1"); err != nil {
		t.Fatal(err)
	}
	if len(r.hist) != 0 {
		t.Fatalf("an account with no wallet recorded a removal: %+v", r.hist)
	}
}

func TestAFailedClearIsReportedNotSwallowed(t *testing.T) {
	// If the clear fails the developer must be told. Answering success would leave them
	// believing a wallet was removed that is still able to receive their money.
	r := newAuditRepo()
	r.clearErr = context.DeadlineExceeded
	if err := walletverify.New(r, platform.FixedClock{T: time.Unix(0, 0)}).Unlink(context.Background(), "usr_1", "usr_1"); err == nil {
		t.Fatal("a failed removal reported success")
	}
}
