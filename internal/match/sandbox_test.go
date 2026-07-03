package match_test

import (
	"context"
	"testing"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
)

// spyWallet / spyRater count calls so we can prove a sandbox match touches NONE
// of the money/rating machinery, while a competitive match does.
type spyWallet struct{ staked, settled, refunded int }

func (s *spyWallet) StakeMatch(context.Context, string, string, string, int64) error {
	s.staked++
	return nil
}
func (s *spyWallet) Settle(context.Context, string, string, int64, int) error {
	s.settled++
	return nil
}
func (s *spyWallet) Refund(context.Context, string) error { s.refunded++; return nil }
func (s *spyWallet) RefundStakes(context.Context, string, string, string, int64) error {
	s.refunded++
	return nil
}

type spyRater struct{ rated int }

func (s *spyRater) Rate(context.Context, match.RatingResult) error { s.rated++; return nil }

// lowestBot is a deterministic house policy for tests: always play the lowest card.
type lowestBot struct{}

func (lowestBot) Pick(state gs.State, seat int, _ string) int {
	h := state.Hands[seat]
	low := h[0]
	for _, c := range h[1:] {
		if c < low {
			low = c
		}
	}
	return low
}

func newSpySvc(w match.Wallet, rt match.Rater, bot match.Bot) *match.Service {
	svc, _ := newSpySvcWithRepo(w, rt, bot)
	return svc
}

func newSpySvcWithRepo(w match.Wallet, rt match.Rater, bot match.Bot) (*match.Service, *fakeRepo) {
	repo := newFakeRepo()
	svc := match.New(repo, fakeLocker{}, match.NoopLimits{}, w,
		match.NoopBroadcaster{}, match.AllowAllVerifier{}, rt, match.NoopFinishHook{},
		platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})
	if bot != nil {
		svc.SetBot(bot)
	}
	return svc, repo
}

// TestSandboxEmitsNoFinishedEvent proves sandbox matches are off the growth path:
// finalize passes a nil match.finished payload (no event, no first-win badge).
func TestSandboxEmitsNoFinishedEvent(t *testing.T) {
	svc, repo := newSpySvcWithRepo(&spyWallet{}, &spyRater{}, lowestBot{})
	ctx := context.Background()
	id, err := svc.CreateSandbox(ctx, "ag_dev", "usr_dev", "ag_house_master", "usr_system", "lowest")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	for i := 0; i < 100; i++ {
		v, _ := svc.State(ctx, id, "ag_dev", false, 0)
		if v.Status == match.StatusFinished {
			break
		}
		if v.YourTurn && len(v.You.Hand) > 0 {
			_, _ = svc.Act(ctx, "ag_dev", id, v.Round, v.You.Hand[0], "")
		}
	}
	if repo.finishCalls == 0 {
		t.Fatal("Finish was never called")
	}
	if repo.finishedEvent != nil {
		t.Fatalf("sandbox match must NOT emit a match.finished event, got %s", repo.finishedEvent)
	}
}

func TestSandboxFinishesWithoutMoneyOrRating(t *testing.T) {
	w, rt := &spyWallet{}, &spyRater{}
	svc := newSpySvc(w, rt, lowestBot{})
	ctx := context.Background()

	id, err := svc.CreateSandbox(ctx, "ag_dev", "usr_dev", "ag_house_master", "usr_system", "lowest")
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}

	// The developer plays only their own seat; the house responds automatically.
	for i := 0; i < 100; i++ {
		v, _ := svc.State(ctx, id, "ag_dev", false, 0)
		if v.Status == match.StatusFinished {
			break
		}
		if v.Mode != match.ModeSandbox {
			t.Fatalf("expected mode=sandbox, got %q", v.Mode)
		}
		if v.YourTurn && len(v.You.Hand) > 0 {
			if _, err := svc.Act(ctx, "ag_dev", id, v.Round, v.You.Hand[0], ""); err != nil {
				t.Fatalf("Act round %d: %v", v.Round, err)
			}
		}
	}

	final, _ := svc.State(ctx, id, "ag_dev", false, 0)
	if final.Status != match.StatusFinished {
		t.Fatalf("sandbox match did not finish: status=%s round=%d", final.Status, final.Round)
	}
	if len(final.History) != 13 {
		t.Fatalf("history len = %d, want 13 (house must auto-play every round)", len(final.History))
	}
	if final.Result == nil || final.Result.CoinsDelta != 0 {
		t.Fatalf("sandbox result must have zero coins delta: %+v", final.Result)
	}
	// The whole point: no coins moved, no rating changed.
	if w.staked != 0 || w.settled != 0 || w.refunded != 0 {
		t.Fatalf("sandbox touched the wallet: staked=%d settled=%d refunded=%d", w.staked, w.settled, w.refunded)
	}
	if rt.rated != 0 {
		t.Fatalf("sandbox updated rating %d times, want 0", rt.rated)
	}
}

// Control: a competitive match MUST stake, settle, and rate — proving the spies
// fire and that the sandbox gating is what suppresses them (not a dead spy).
func TestCompetitiveStakesSettlesAndRates(t *testing.T) {
	w, rt := &spyWallet{}, &spyRater{}
	svc := newSpySvc(w, rt, nil)
	ctx := context.Background()

	id, err := svc.CreateOpen(ctx, "ag_a", "usr_a", 50)
	if err != nil {
		t.Fatalf("CreateOpen: %v", err)
	}
	if _, err := svc.Join(ctx, "ag_b", "usr_b", id); err != nil {
		t.Fatalf("Join: %v", err)
	}
	for i := 0; i < 100; i++ {
		v, _ := svc.State(ctx, id, "ag_a", false, 0)
		if v.Status == match.StatusFinished {
			break
		}
		for _, ag := range []string{"ag_a", "ag_b"} {
			cur, _ := svc.State(ctx, id, ag, false, 0)
			if cur.YourTurn && len(cur.You.Hand) > 0 {
				if _, err := svc.Act(ctx, ag, id, cur.Round, cur.You.Hand[0], ""); err != nil {
					t.Fatalf("Act(%s): %v", ag, err)
				}
			}
		}
	}
	if w.staked == 0 || w.settled == 0 || rt.rated == 0 {
		t.Fatalf("competitive match should stake/settle/rate: staked=%d settled=%d rated=%d", w.staked, w.settled, rt.rated)
	}
}
