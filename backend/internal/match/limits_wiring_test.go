package match_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
)

// errLimit is what a real guardrail block looks like to the caller.
var errLimit = errors.New("limit_daily_loss_limit: daily loss reached")

// blockingLimits refuses every join and records that it was consulted at all.
type blockingLimits struct{ asked, concurrencyAsked int }

func (b *blockingLimits) CheckJoin(context.Context, string, int64) error {
	b.asked++
	return errLimit
}

func (b *blockingLimits) CheckConcurrency(context.Context, string) error {
	b.concurrencyAsked++
	return errLimit
}

func svcWithLimits(l match.Limits) *match.Service {
	return match.New(newFakeRepo(), fakeLocker{}, l, match.NoopWallet{},
		match.NoopBroadcaster{}, match.AllowAllVerifier{}, match.NoopRater{}, match.NoopFinishHook{},
		platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})
}

// The guardrails are unit-tested in internal/wallet, which proves CheckJoin returns
// the right error. Nothing proved the GAME asks it — so a refactor that dropped the
// call would silently disable every stop-loss on the platform while the whole suite
// stayed green. This is that missing link.
func TestCreateOpenRefusesWhenAGuardrailBlocks(t *testing.T) {
	lim := &blockingLimits{}
	_, err := svcWithLimits(lim).CreateOpen(context.Background(), "ag_a", "usr_a", 100)

	if lim.asked == 0 {
		t.Fatal("staking a match never consulted the spending limits")
	}
	if !errors.Is(err, errLimit) {
		t.Fatalf("a blocked guardrail did not stop the join: err=%v", err)
	}
}

// The limit must be checked BEFORE anything is escrowed. Checking after would leave
// a blocked agent's coins in escrow for a match that was refused.
func TestGuardrailIsCheckedBeforeAnyCoinsMove(t *testing.T) {
	lim := &blockingLimits{}
	w := &spyWallet{}
	svc := match.New(newFakeRepo(), fakeLocker{}, lim, w,
		match.NoopBroadcaster{}, match.AllowAllVerifier{}, match.NoopRater{}, match.NoopFinishHook{},
		platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		match.Config{MoveWindow: 20 * time.Second, RakePct: 5, Rounds: 13, LockTTL: 5 * time.Second})

	_, _ = svc.CreateOpen(context.Background(), "ag_a", "usr_a", 100)

	if w.staked > 0 {
		t.Fatalf("escrowed %d stake(s) despite the guardrail refusing the join", w.staked)
	}
}

// A permissive limiter must still let a legitimate join through — a guardrail that
// blocks everything is not a working guardrail.
func TestAllowedJoinStillWorks(t *testing.T) {
	if _, err := svcWithLimits(match.NoopLimits{}).CreateOpen(context.Background(), "ag_a", "usr_a", 100); err != nil {
		t.Fatalf("a permitted join was refused: %v", err)
	}
}

// Sandbox stakes nothing, so it skips the money limits — but it must still honour
// max_concurrent_matches. Ignoring it seated several tables at once, multiplying an
// LLM agent's inference bill by the concurrency factor with no warning and pushing a
// tester's provider account into continuous rate-limiting.
func TestSandboxHonoursTheConcurrencyLimit(t *testing.T) {
	lim := &blockingLimits{}
	_, err := svcWithLimits(lim).CreateSandbox(context.Background(), "ag_a", "usr_a", "ag_house", "usr_house", "balanced")

	if lim.concurrencyAsked == 0 {
		t.Fatal("starting a sandbox match never consulted max_concurrent_matches")
	}
	if !errors.Is(err, errLimit) {
		t.Fatalf("a concurrency block did not stop the sandbox match: err=%v", err)
	}
	// And it must NOT have applied the money limits — balance and loss caps are
	// meaningless on a zero-stake table and would block practice for a broke agent.
	if lim.asked != 0 {
		t.Fatalf("sandbox applied the money limits (CheckJoin called %d times)", lim.asked)
	}
}
