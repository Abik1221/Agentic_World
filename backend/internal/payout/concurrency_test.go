package payout_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/agent-arena/arena/internal/payout"
)

// raceRepo models the property that matters for the withdrawal TOCTOU: the amount
// still withdrawable SHRINKS as live withdrawals are created (committed coins are
// subtracted). WithOwnerLock is a real mutex (standing in for the Postgres advisory
// lock) that also records peak concurrency, so the test can assert the service
// actually holds the lock across the read-check-create critical section.
type raceRepo struct {
	*fakeRepo
	mu        sync.Mutex
	balance   int64
	active    int32
	maxActive int32
}

// Withdrawable = balance − coins already committed to live withdrawal rows. Safe
// to read the map unsynchronized: it is only ever reached inside WithOwnerLock,
// which serializes every request for this owner.
func (r *raceRepo) Withdrawable(context.Context, string) (int64, error) {
	var committed int64
	for _, w := range r.fakeRepo.rows {
		committed += w.Coins
	}
	avail := r.balance - committed
	if avail < 0 {
		avail = 0
	}
	return avail, nil
}

// AgentBalance mirrors Withdrawable here: the race being tested is over the
// entitlement read, not over where the coins physically sit, so modelling a treasury
// split would only add noise. Returning the whole amount means no sweep is needed and
// the critical section under test is unchanged.
func (r *raceRepo) AgentBalance(ctx context.Context, agent string) (int64, error) {
	return r.Withdrawable(ctx, agent)
}

func (r *raceRepo) WithOwnerLock(_ context.Context, _ string, fn func() error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := atomic.AddInt32(&r.active, 1)
	for {
		old := atomic.LoadInt32(&r.maxActive)
		if n <= old || atomic.CompareAndSwapInt32(&r.maxActive, old, n) {
			break
		}
	}
	defer atomic.AddInt32(&r.active, -1)
	return fn()
}

// TestConcurrentRequestsCannotOverWithdraw fires many simultaneous withdrawal
// requests for the SAME owner, each asking for the full withdrawable balance. With
// the per-owner lock the reads and the Create serialize, so exactly one succeeds
// and the rest see the committed row and are rejected (ErrInsufficient). Without
// the lock the requests would each read the stale pre-create balance and all pass,
// draining past the owner's net winnings — the exact money-theft race this guards.
func TestConcurrentRequestsCannotOverWithdraw(t *testing.T) {
	base := newRepo()
	base.withdrawable = 0 // unused; raceRepo computes it dynamically
	repo := &raceRepo{fakeRepo: base, balance: 600}

	svc := newSvc(repo, newBank(), &fakeXfer{})

	const n = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	var ok, insufficient, other int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := svc.Request(context.Background(), "usr_a", "ag_a", 600)
			switch err {
			case nil:
				atomic.AddInt32(&ok, 1)
			case payout.ErrInsufficient:
				atomic.AddInt32(&insufficient, 1)
			default:
				atomic.AddInt32(&other, 1)
			}
		}()
	}
	close(start)
	wg.Wait()

	if ok != 1 {
		t.Fatalf("successful withdrawals = %d, want exactly 1 (over-withdraw race)", ok)
	}
	if insufficient != n-1 {
		t.Fatalf("rejected = %d, want %d ErrInsufficient", insufficient, n-1)
	}
	if other != 0 {
		t.Fatalf("unexpected errors = %d", other)
	}
	if repo.maxActive != 1 {
		t.Fatalf("peak concurrent critical sections = %d, want 1 (lock not held across check+create)", repo.maxActive)
	}
}
