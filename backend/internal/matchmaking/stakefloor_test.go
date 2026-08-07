package matchmaking

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeFloor struct {
	ok     bool
	lowest int64
	err    error
	// what the floor was actually asked, so a test can prove the queue passes through the real
	// game and amount rather than a placeholder that happens to validate.
	gotGame  string
	gotCoins int64
	calls    int
}

func (f *fakeFloor) ValidStake(_ context.Context, game string, coins int64) (bool, int64, error) {
	f.calls++
	f.gotGame, f.gotCoins = game, coins
	return f.ok, f.lowest, f.err
}

// A stake the game does not offer must be REJECTED at the queue, not silently escrowed.
//
// The regression: tier validation lived only in the HTTP handler, while autoplay and the pairing
// driver call Enqueue directly. 870 live matches were staked at 50 and 100 coins against a
// configured floor of 500, beginning two seconds after the tiers were seeded and continuing for
// two days without producing a single error — because nothing on that path ever asked.
func TestEnqueueRejectsAStakeTheGameDoesNotOffer(t *testing.T) {
	s := &Service{}
	s.SetStakeFloor(&fakeFloor{ok: false, lowest: 500})

	_, err := s.Enqueue(context.Background(), "ag_x", "us_x", 50)
	if err == nil {
		t.Fatal("a 50-coin bid was accepted against a 500-coin floor")
	}
	if !strings.Contains(err.Error(), "stake_not_offered") {
		t.Fatalf("err = %v, want stake_not_offered", err)
	}
	// The message must name the usable stake. "Invalid stake" leaves a developer guessing at a
	// number the server already knows.
	if !strings.Contains(err.Error(), "500") {
		t.Fatalf("error does not tell the caller what to use instead: %v", err)
	}
}

// An unreadable tier table must FAIL CLOSED.
//
// Failing open is precisely how sub-floor matches ran for two days: the absence of a check reads
// exactly like permission. A queue that cannot verify a stake must not escrow one.
func TestEnqueueFailsClosedWhenTiersCannotBeRead(t *testing.T) {
	s := &Service{}
	s.SetStakeFloor(&fakeFloor{err: errors.New("db down")})

	_, err := s.Enqueue(context.Background(), "ag_x", "us_x", 500)
	if err == nil {
		t.Fatal("a stake was accepted while the tier table was unreadable")
	}
	if !strings.Contains(err.Error(), "stakes_unavailable") {
		t.Fatalf("err = %v, want stakes_unavailable", err)
	}
}

// The queue must ask about the REAL game and the REAL amount.
//
// A guard that validates a placeholder would pass every test here and protect nothing in
// production — the check has to be wired to the values actually being escrowed.
func TestEnqueueAsksTheFloorAboutTheRealGameAndAmount(t *testing.T) {
	f := &fakeFloor{ok: true, lowest: 500}
	s := &Service{}
	s.SetStakeFloor(f)

	// Enqueue continues into gates this bare Service has no ports for; the floor has already run
	// by then, which is the whole of what this test observes.
	func() {
		defer func() { _ = recover() }()
		_, _ = s.Enqueue(context.Background(), "ag_x", "us_x", 500)
	}()

	if f.calls != 1 {
		t.Fatalf("floor consulted %d times, want exactly 1", f.calls)
	}
	if f.gotGame != "goofspiel" || f.gotCoins != 500 {
		t.Fatalf("floor asked about (%q, %d), want (\"goofspiel\", 500)", f.gotGame, f.gotCoins)
	}
}
