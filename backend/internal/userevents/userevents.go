// Package userevents is the realtime push rail for ONE signed-in person.
//
// Everything that moves a user's money — a deposit detected on-chain, a deposit
// credited, a withdrawal requested/paid/failed, coins allocated to an agent, coins
// staked into a match, a match settled — was already written to the notifications
// table and then left sitting there. The browser found out on its next 60-second
// bell poll or on a full page reload, whichever came first, which is why a
// successful payment produced no confirmation and no visible balance change. This
// package is the missing half: the same event, pushed to that user's open tabs the
// moment it happens.
//
// Transport is Redis pub/sub on a per-user channel, so it is cross-instance — a
// deposit credited by the chain listener on instance A reaches a stream held open
// on instance B.
//
// Delivery is best-effort BY DESIGN, and that is a safety property, not a
// shortcut. An event carries a kind plus a small payload for the toast text; it
// never carries an authoritative balance. The client re-reads /v1/user/wallet for
// the number. So a dropped, duplicated or out-of-order event costs one refresh,
// never correctness — the same contract store.Notifier holds for match wake-ups.
// Nothing in the money path may block on, or fail because of, this package.
package userevents

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
)

// Event kinds. These are the SAME strings the notifications table stores in
// `kind`, deliberately: the bell's persisted feed and the live stream describe one
// event, so the client renders both through one label/icon table and a reload can
// never disagree with what the toast said.
const (
	KindDepositDetected     = "deposit_detected"
	KindDepositCompleted    = "deposit_completed"
	KindWithdrawalRequested = "withdrawal_requested"
	KindWithdrawalPaid      = "withdrawal_paid"
	KindWithdrawalFailed    = "withdrawal_failed"
	KindCoinsAllocated      = "coins_allocated"
	KindCoinsStaked         = "coins_staked"
	KindCoinsToppedUp       = "coins_topped_up"
	KindBalanceAdjusted     = "balance_adjusted"
	KindMatchResult         = "match_result"
	KindAgentMatch          = "agent_match"
)

// moneyKinds are the events after which the client's cached balance is known to be
// wrong. The stream tags each event with `wallet: true` for these so the client
// refetches the treasury instead of pattern-matching kind strings of its own.
var moneyKinds = map[string]bool{
	KindDepositCompleted:    true,
	KindWithdrawalRequested: true,
	KindWithdrawalPaid:      true,
	KindWithdrawalFailed:    true,
	KindCoinsAllocated:      true,
	KindCoinsStaked:         true,
	KindCoinsToppedUp:       true,
	KindBalanceAdjusted:     true,
	KindMatchResult:         true,
}

// MovesMoney reports whether an event of this kind invalidates a cached balance.
func MovesMoney(kind string) bool { return moneyKinds[kind] }

// Event is one realtime message for one user.
type Event struct {
	Kind string `json:"kind"`
	// Ref is the event's dedup key (same value as the notifications row's `ref`),
	// so a client that also polls the feed can recognise an event it already showed.
	Ref string `json:"ref,omitempty"`
	// Payload is the notification's own JSON body (coins, net_cents, match id …).
	Payload json.RawMessage `json:"payload,omitempty"`
	// Wallet marks an event after which the cached balance must be refetched.
	Wallet    bool      `json:"wallet"`
	CreatedAt time.Time `json:"created_at"`
}

// publishTimeout bounds a single publish so a slow or unreachable Redis can never
// hold up a deposit credit, a withdrawal request or a match settlement.
const publishTimeout = 2 * time.Second

// Bus fans events out to a user's open streams over Redis pub/sub.
type Bus struct {
	rdb *redis.Client
	log *slog.Logger
}

// NewBus builds the event bus. A nil *redis.Client is tolerated and makes every
// Publish a no-op, so a deployment without Redis degrades to the polled feed
// rather than failing money operations.
func NewBus(rdb *redis.Client, log *slog.Logger) *Bus { return &Bus{rdb: rdb, log: log} }

func channel(userPublicID string) string { return "user:events:" + userPublicID }

// Publish sends one event to every stream this user has open, on any instance.
// Best-effort and non-fatal: a publish failure is logged at debug and swallowed.
func (b *Bus) Publish(ctx context.Context, userPublicID, kind, ref string, payload json.RawMessage) {
	if b == nil || b.rdb == nil || userPublicID == "" {
		return
	}
	ev := Event{
		Kind:      kind,
		Ref:       ref,
		Payload:   payload,
		Wallet:    MovesMoney(kind),
		CreatedAt: time.Now().UTC(),
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	// An independent timeout, NOT the caller's context: the caller here is a money
	// path whose context may already be cancelling, and a missed UI refresh must not
	// be the thing that makes a credit look failed.
	pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), publishTimeout)
	defer cancel()
	if err := b.rdb.Publish(pctx, channel(userPublicID), body).Err(); err != nil {
		b.log.Debug("userevents publish failed", "user", userPublicID, "kind", kind, "error", err)
	}
}

// PublishJSON marshals payload and publishes. Convenience for callers holding a map.
func (b *Bus) PublishJSON(ctx context.Context, userPublicID, kind, ref string, payload map[string]any) {
	var raw json.RawMessage
	if payload != nil {
		if body, err := json.Marshal(payload); err == nil {
			raw = body
		}
	}
	b.Publish(ctx, userPublicID, kind, ref, raw)
}

// Subscribe returns a channel of this user's events and a cancel func that
// releases the underlying subscription. Sends are non-blocking against a small
// buffer: a stream that cannot keep up drops events rather than wedging the pump,
// because the client's reconnect + feed re-read is the recovery path.
func (b *Bus) Subscribe(userPublicID string) (<-chan Event, func()) {
	out := make(chan Event, 16)
	if b == nil || b.rdb == nil || userPublicID == "" {
		close(out)
		return out, func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	ps := b.rdb.Subscribe(ctx, channel(userPublicID))

	go func() {
		defer close(out)
		ch := ps.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var ev Event
				if err := json.Unmarshal([]byte(msg.Payload), &ev); err != nil {
					continue
				}
				select {
				case out <- ev:
				default: // subscriber is behind; drop rather than block the pump
				}
			}
		}
	}()

	return out, func() {
		cancel()
		_ = ps.Close()
	}
}

// ── background dispatch ──────────────────────────────────────────────────────

// Signaller runs best-effort publish jobs off the caller's goroutine on a small
// bounded pool.
//
// It exists because some publishers must first resolve WHO the event belongs to
// (an agent's owner, say) and that is a database round trip. Doing it inline would
// put a DB query and a Redis publish inside a match's stake transaction path for
// the sake of a UI refresh. Doing it in a bare `go func()` would let a stalled
// Redis grow goroutines without bound. A fixed pool with a bounded queue that drops
// on overflow is the honest shape: the worst case is a browser that refreshes a
// second later than it could have.
type Signaller struct {
	jobs    chan func(context.Context)
	log     *slog.Logger
	workers int
}

// NewSignaller builds the pool. workers ≤ 0 → 2; queue ≤ 0 → 256.
func NewSignaller(workers, queue int, log *slog.Logger) *Signaller {
	if workers <= 0 {
		workers = 2
	}
	if queue <= 0 {
		queue = 256
	}
	return &Signaller{jobs: make(chan func(context.Context), queue), log: log, workers: workers}
}

// Go schedules fn. Never blocks: when the queue is full the job is dropped and
// logged, because a delayed UI refresh is strictly better than a stalled caller.
func (s *Signaller) Go(fn func(context.Context)) {
	if s == nil || fn == nil {
		return
	}
	select {
	case s.jobs <- fn:
	default:
		s.log.Warn("userevents: signal queue full; dropping realtime push")
	}
}

// Run starts the workers and blocks until ctx is cancelled.
func (s *Signaller) Run(ctx context.Context) {
	done := make(chan struct{})
	for i := 0; i < s.workers; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for {
				select {
				case <-ctx.Done():
					return
				case fn := <-s.jobs:
					s.run(ctx, fn)
				}
			}
		}()
	}
	for i := 0; i < s.workers; i++ {
		<-done
	}
}

// run isolates one job: a panic in a publisher must not take down the pool.
func (s *Signaller) run(ctx context.Context, fn func(context.Context)) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("userevents: signal job panicked", "recover", r)
		}
	}()
	jctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fn(jctx)
}
