package matchmaking

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/antifraud"
)

type fakeLinks struct {
	groups []antifraud.LinkedGroup
	err    error
	calls  int
}

func (f *fakeLinks) BeneficiaryLinks(context.Context) ([]antifraud.LinkedGroup, error) {
	f.calls++
	return f.groups, f.err
}

func testLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func svcWithLinks(t *testing.T, l BeneficiaryLinks) *Service {
	t.Helper()
	s := &Service{log: testLogger()}
	s.SetBeneficiaryLinks(l)
	return s
}

// TestTwoAccountsOnePayoutWalletAreNotPaired — the hole this closes. The old rule compared
// owner ids, so registering twice let a ring be seated against itself.
func TestTwoAccountsOnePayoutWalletAreNotPaired(t *testing.T) {
	s := svcWithLinks(t, &fakeLinks{groups: []antifraud.LinkedGroup{
		{Owners: []string{"usr_a", "usr_b"},
			Via: antifraud.Beneficiary{Kind: antifraud.LinkPayoutWallet, Value: "sol1"}},
	}})
	if err := s.RefreshLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.linked("usr_a", "usr_b") {
		t.Fatal("two accounts withdrawing to one wallet would still be paired")
	}
	if s.linked("usr_a", "usr_stranger") {
		t.Fatal("an unrelated owner was refused a pairing")
	}
}

// TestOwnerIdRuleSurvivesWithoutAnIndex. The new predicate must SUBSUME the old one, so a
// platform that has not wired the lookup — or whose refresh has never run — still refuses to
// seat an owner against themselves.
func TestOwnerIdRuleSurvivesWithoutAnIndex(t *testing.T) {
	bare := &Service{log: testLogger()}
	if !bare.linked("usr_a", "usr_a") {
		t.Fatal("an owner could be paired against itself with no index installed")
	}
	if bare.linked("usr_a", "usr_b") {
		t.Fatal("two unrelated owners were refused with no index installed")
	}
	if err := bare.RefreshLinks(context.Background()); err != nil {
		t.Fatalf("refresh with no source should be a no-op: %v", err)
	}
}

// TestRefreshFailureKeepsThePreviousIndex.
//
// Dropping to "nobody is linked" on a transient database error is the wrong direction to
// fail: it re-opens the multi-account hole at exactly the moment the database is unhappy,
// silently, and matchmaking would keep running as if the control were on.
func TestRefreshFailureKeepsThePreviousIndex(t *testing.T) {
	f := &fakeLinks{groups: []antifraud.LinkedGroup{
		{Owners: []string{"usr_a", "usr_b"},
			Via: antifraud.Beneficiary{Kind: antifraud.LinkPayoutWallet, Value: "w"}},
	}}
	s := svcWithLinks(t, f)
	if err := s.RefreshLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.err = errors.New("database unavailable")
	if err := s.RefreshLinks(context.Background()); err == nil {
		t.Fatal("a refresh failure was swallowed")
	}
	if !s.linked("usr_a", "usr_b") {
		t.Fatal("a failed refresh cleared the index and re-opened the multi-account hole")
	}
}

// TestLinkageIsTransitiveThroughTheService. A ring can otherwise launder the relationship
// through a middle account.
func TestLinkageIsTransitiveThroughTheService(t *testing.T) {
	s := svcWithLinks(t, &fakeLinks{groups: []antifraud.LinkedGroup{
		{Owners: []string{"a", "b"}, Via: antifraud.Beneficiary{Kind: antifraud.LinkPayoutWallet, Value: "w1"}},
		{Owners: []string{"b", "c"}, Via: antifraud.Beneficiary{Kind: antifraud.LinkStripeConnect, Value: "acct"}},
	}})
	if err := s.RefreshLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !s.linked("a", "c") {
		t.Fatal("a and c are one beneficiary through b but would still be paired")
	}
}

// TestEmptyOwnerIsNotLinked. A failed lookup must not refuse to seat everyone — matchmaking
// is on the hot path of every queued match.
func TestEmptyOwnerIsNotLinked(t *testing.T) {
	s := svcWithLinks(t, &fakeLinks{})
	if err := s.RefreshLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.linked("", "usr_a") || s.linked("usr_a", "") {
		t.Fatal("an unidentified owner was treated as linked; a lookup glitch would halt matchmaking")
	}
}
