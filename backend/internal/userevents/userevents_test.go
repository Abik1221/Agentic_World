package userevents

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// A money event must invalidate the client's cached balance; a purely
// informational one must not, or every follower of a busy agent refetches their
// treasury on someone else's match.
func TestMovesMoney(t *testing.T) {
	moves := []string{
		KindDepositCompleted, KindWithdrawalRequested, KindWithdrawalPaid,
		KindWithdrawalFailed, KindCoinsAllocated, KindCoinsStaked,
		KindCoinsToppedUp, KindBalanceAdjusted, KindMatchResult,
	}
	for _, k := range moves {
		if !MovesMoney(k) {
			t.Errorf("%s should invalidate the cached balance", k)
		}
	}
	for _, k := range []string{KindDepositDetected, KindAgentMatch, "unknown_kind", ""} {
		if MovesMoney(k) {
			t.Errorf("%s must not force a balance refetch", k)
		}
	}
}

// The whole rail is optional. A deployment with no Redis (or a Redis that failed
// to open) must degrade to the polled feed — never panic a money path, never
// leave a caller blocked, never hand out a channel that hangs forever.
func TestNilRedisDegradesQuietly(t *testing.T) {
	b := NewBus(nil, quietLogger())

	// Publishing from a money path is a no-op, not a panic.
	b.Publish(context.Background(), "usr_1", KindDepositCompleted, "deposit:sig", json.RawMessage(`{"coins":100}`))
	b.PublishJSON(context.Background(), "usr_1", KindCoinsStaked, "stake:m_1", map[string]any{"coins": 5})

	// Subscribing yields an already-closed channel, so the SSE handler's `!ok`
	// branch fires and the client reconnects instead of holding a dead stream.
	ch, cancel := b.Subscribe("usr_1")
	defer cancel()
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected a closed channel from a bus with no redis")
		}
	case <-time.After(time.Second):
		t.Fatal("Subscribe on a nil-redis bus must not hang")
	}
}

// An empty user id is the shape a bug takes when a principal fails to resolve.
// Publishing to it would broadcast on a channel nobody owns; it must be dropped.
func TestPublishRequiresAUser(t *testing.T) {
	b := NewBus(nil, quietLogger())
	b.Publish(context.Background(), "", KindDepositCompleted, "ref", nil)

	ch, cancel := b.Subscribe("")
	defer cancel()
	if _, ok := <-ch; ok {
		t.Fatal("an empty user id must not yield a live subscription")
	}
}

// A cancelled caller context must not suppress the push. Deposit crediting and
// match settlement both run under contexts that may already be unwinding, and a
// missed UI refresh must never be caused by the very completion it announces.
func TestPublishSurvivesACancelledCallerContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := NewBus(nil, quietLogger())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.Publish(ctx, "usr_1", KindDepositCompleted, "deposit:sig", nil)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a cancelled caller context")
	}
}

func TestSignallerRunsJobs(t *testing.T) {
	s := NewSignaller(2, 8, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	var wg sync.WaitGroup
	wg.Add(4)
	var mu sync.Mutex
	seen := 0
	for i := 0; i < 4; i++ {
		s.Go(func(context.Context) {
			mu.Lock()
			seen++
			mu.Unlock()
			wg.Done()
		})
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("signaller ran %d/4 jobs", seen)
	}
}

// Saturation must shed work, not block the caller. The caller here is a stake
// transaction; making it wait on a UI refresh would be the wrong trade in every
// case, so a full queue drops the push and the browser catches up on its next
// poll or reconnect.
func TestSignallerDropsRatherThanBlocks(t *testing.T) {
	s := NewSignaller(1, 1, quietLogger()) // never started: nothing drains the queue

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			s.Go(func(context.Context) {})
		}
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Go blocked when the queue was full; it must drop instead")
	}
}

// A panicking publisher must not take the pool down with it — the next job still
// runs. (An unrecovered goroutine panic kills the whole process, taking matches
// and settlement with it; that is why every worker here is isolated.)
func TestSignallerSurvivesAPanickingJob(t *testing.T) {
	s := NewSignaller(1, 4, quietLogger())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	s.Go(func(context.Context) { panic("publisher exploded") })

	ran := make(chan struct{})
	s.Go(func(context.Context) { close(ran) })

	select {
	case <-ran:
	case <-time.After(3 * time.Second):
		t.Fatal("pool did not survive a panicking job")
	}
}

// A nil Signaller (no realtime configured) must be a silent no-op at the call
// site rather than something every publisher has to guard.
func TestNilSignallerIsSafe(t *testing.T) {
	var s *Signaller
	s.Go(func(context.Context) { t.Fatal("nil signaller must not run jobs") })
}

// The wire shape is a contract with the browser: the client keys its toast on
// `kind`, dedupes on `ref`, and decides whether to refetch the treasury from
// `wallet`. Assert the JSON the handler actually emits.
func TestEventWireShape(t *testing.T) {
	ev := Event{
		Kind:      KindDepositCompleted,
		Ref:       "deposit:5xY",
		Payload:   json.RawMessage(`{"coins":950}`),
		Wallet:    MovesMoney(KindDepositCompleted),
		CreatedAt: time.Unix(0, 0).UTC(),
	}
	body, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var back struct {
		Kind    string          `json:"kind"`
		Ref     string          `json:"ref"`
		Payload json.RawMessage `json:"payload"`
		Wallet  bool            `json:"wallet"`
	}
	if err := json.Unmarshal(body, &back); err != nil {
		t.Fatal(err)
	}
	if back.Kind != KindDepositCompleted || back.Ref != "deposit:5xY" || !back.Wallet {
		t.Fatalf("unexpected wire shape: %s", body)
	}
	var p struct {
		Coins int64 `json:"coins"`
	}
	if err := json.Unmarshal(back.Payload, &p); err != nil || p.Coins != 950 {
		t.Fatalf("payload did not survive the round trip: %s", body)
	}
}
