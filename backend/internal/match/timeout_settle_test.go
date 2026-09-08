package match_test

import (
	"context"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/match"
)

// movingClock lets a test advance past a move deadline, which FixedClock cannot.
type movingClock struct{ t time.Time }

func (c *movingClock) Now() time.Time          { return c.t }
func (c *movingClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// A STAKED match decided entirely by TIMEOUTS must still settle.
//
// Reproduces a real production match (m_t3x5yqa3vkcvn6w2): two agents both went
// unresponsive, every round ran the full move window, the engine forced the lowest card
// for each seat, the game ended 0-0 as a tie — and the ledger shows `stake -500` for both
// agents with NO settle and NO refund. 1,000 coins were taken and stranded in escrow.
//
// The existing coverage all drives matches through Act(), which settles correctly. Nothing
// covered the SWEEPER path (HandleTimeout → ForceTimeout → commit → finalize), which is
// the path a match takes when the agents stop answering — and that is exactly when money
// most needs to come back, because neither player did anything wrong.
func TestATimedOutStakedMatchStillSettles(t *testing.T) {
	clk := &movingClock{t: time.Unix(1_700_000_000, 0).UTC()}
	w := &spyWallet{}
	svc := match.New(newFakeRepo(), fakeLocker{}, match.NoopLimits{}, w,
		match.NoopBroadcaster{}, match.AllowAllVerifier{}, &spyRater{}, match.NoopFinishHook{},
		clk,
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})
	ctx := context.Background()

	id, err := svc.CreateOpen(ctx, "ag_a", "usr_a", 500)
	if err != nil {
		t.Fatalf("CreateOpen: %v", err)
	}
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatalf("Join: %v", err)
	}
	if w.staked == 0 {
		t.Fatal("both stakes should be escrowed at match start")
	}

	// Nobody ever acts. Advance past each deadline and let the sweeper force the round,
	// exactly as production did for all 13 rounds.
	for i := 0; i < 60; i++ {
		v, _ := svc.State(ctx, id, "ag_a", false, 0)
		if v.Status == match.StatusFinished {
			break
		}
		clk.advance(30 * time.Second)
		if err := svc.HandleTimeout(ctx, id); err != nil {
			t.Fatalf("HandleTimeout: %v", err)
		}
	}

	v, _ := svc.State(ctx, id, "ag_a", false, 0)
	if v.Status != match.StatusFinished {
		t.Fatalf("match never finished: status=%s", v.Status)
	}
	if w.settled == 0 && w.refunded == 0 {
		t.Fatalf("STRANDED ESCROW: match finished with stakes taken (staked=%d) but "+
			"neither settled nor refunded — the coins have nothing to release them",
			w.staked)
	}
}
