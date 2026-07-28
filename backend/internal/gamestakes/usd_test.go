package gamestakes_test

import (
	"context"
	"testing"

	"github.com/agent-arena/arena/internal/gamestakes"
)

// usdSvc is a service with an explicit coin peg, for the dollar-facing admin surface.
func usdSvc(coinCents int64) (*gamestakes.Service, *fakeRepo) {
	r := &fakeRepo{}
	s := newSvc(r)
	s.SetCoinCents(coinCents)
	return s, r
}

// An operator sets $5 / $20 / $50; the ledger stores coins. At a 1¢ peg that is
// 500 / 2000 / 5000.
func TestDollarsAreConvertedToCoinsOnWrite(t *testing.T) {
	s, repo := usdSvc(1)
	err := s.AdminPut(context.Background(), "admin", "mafia", []gamestakes.Tier{
		{Key: "low", USDCents: 500, Ordering: 0, Enabled: true},
		{Key: "mid", USDCents: 2000, Ordering: 1, Enabled: true},
		{Key: "high", USDCents: 5000, Ordering: 2, Enabled: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{500, 2000, 5000} {
		if got := repo.tiers["mafia"][i].Coins; got != want {
			t.Fatalf("tier %d stored %d coins, want %d", i, got, want)
		}
	}
}

// The peg is not always 1¢. At 5¢/coin, $5 is 100 coins — the stored value must
// reflect the peg, not the number the operator typed.
func TestConversionHonoursTheCoinPeg(t *testing.T) {
	s, repo := usdSvc(5)
	if err := s.AdminPut(context.Background(), "admin", "mafia", []gamestakes.Tier{
		{Key: "low", USDCents: 500, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	if got := repo.tiers["mafia"][0].Coins; got != 100 {
		t.Fatalf("stored %d coins, want 100 ($5 at 5¢/coin)", got)
	}
}

// Money must not change silently under an operator who typed a specific number.
// $5.02 is not representable at a 5¢ peg, so it is refused rather than rounded.
func TestNonRepresentableAmountIsRefusedNotRounded(t *testing.T) {
	s, _ := usdSvc(5)
	err := s.AdminPut(context.Background(), "admin", "mafia", []gamestakes.Tier{
		{Key: "low", USDCents: 502, Enabled: true},
	})
	if err == nil {
		t.Fatal("an amount that cannot be expressed in whole coins was accepted")
	}
}

// The $5 floor holds, and cannot be dodged by submitting coins instead of dollars.
func TestMinimumStakeIsEnforcedInBothUnits(t *testing.T) {
	s, _ := usdSvc(1)
	if err := s.AdminPut(context.Background(), "admin", "mafia", []gamestakes.Tier{
		{Key: "low", USDCents: 100, Enabled: true},
	}); err == nil {
		t.Fatal("a $1 tier was accepted below the $5 floor")
	}
	// The same amount in coins — the floor is checked on the RESOLVED value, so the
	// unit the caller chose cannot be used to slip under it.
	if err := s.AdminPut(context.Background(), "admin", "mafia", []gamestakes.Tier{
		{Key: "low", Coins: 100, Enabled: true},
	}); err == nil {
		t.Fatal("the floor was bypassed by submitting coins instead of dollars")
	}
}

// A sandbox deployment may legitimately run without a floor, and turning it off must
// actually turn it off.
func TestFloorCanBeDisabled(t *testing.T) {
	s, _ := usdSvc(1)
	s.SetMinStakeUSDCents(0)
	if err := s.AdminPut(context.Background(), "admin", "mafia", []gamestakes.Tier{
		{Key: "low", Coins: 10, Enabled: true},
	}); err != nil {
		t.Fatalf("floor disabled but a small tier was still refused: %v", err)
	}
}

// Reads carry the dollar price so a client never has to reimplement the peg.
func TestReadsArePricedInDollars(t *testing.T) {
	s, _ := usdSvc(5)
	if err := s.AdminPut(context.Background(), "admin", "mafia", []gamestakes.Tier{
		{Key: "low", Coins: 100, Enabled: true},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(context.Background(), "mafia")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].USDCents != 500 {
		t.Fatalf("public tier priced at %v, want 500 cents", got)
	}
	admin, err := s.AdminGet(context.Background(), "mafia")
	if err != nil {
		t.Fatal(err)
	}
	if admin.Tiers[0].USDCents != 500 {
		t.Fatalf("admin tier priced at %d cents, want 500", admin.Tiers[0].USDCents)
	}
}
