// Package webhook is the durable, at-least-once delivery path for the push
// protocol's asynchronous notifications (/event + /game-end). The game engines
// never block on delivery: their drive loops ENQUEUE a row (idempotently) and
// this package's Dispatcher delivers it in the background — HMAC-signed, with
// exponential backoff, and gated by a per-endpoint health circuit breaker so a
// dead or slow endpoint is skipped rather than hammered.
//
// This is the industry-standard signed-webhook pattern (Stripe/GitHub), made
// durable: rows survive restarts, a single delivery is leased by exactly one
// worker (FOR UPDATE SKIP LOCKED + a lease on next_attempt_at), and a poison
// delivery is abandoned after maxAttempts so it can never wedge the queue.
package webhook

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

// Delivery kinds.
const (
	KindEvent   = "event"
	KindGameEnd = "game-end"
)

// Delivery is one queued webhook notification.
type Delivery struct {
	PublicID      string
	AgentPublicID string
	Kind          string // KindEvent | KindGameEnd
	Game          string
	MatchID       string
	Seq           int
	EventType     string
	Payload       json.RawMessage
	Attempts      int
	// CreatedAt is when the event was enqueued, and it is what bounds the backlog.
	//
	// Attempts bounds the RETRY path, but the circuit-breaker path deliberately does
	// not count an attempt (delivery was never tried), so nothing bounded it at all:
	// an endpoint that stays down is deferred forever. Age is the bound that path
	// needs, and it has to come from the row rather than from a counter the defer
	// path is not allowed to touch.
	CreatedAt time.Time
}

// Enqueuer is the write side used by the game drive loops. Both methods are
// idempotent: re-enqueuing the same (agent, match, kind, seq) is a no-op.
type Enqueuer interface {
	EnqueueEvent(ctx context.Context, agentPublicID, game, matchID string, seq int, eventType string, payload []byte) error
	EnqueueGameEnd(ctx context.Context, agentPublicID, game, matchID string, payload []byte) error
}

// Store is the delivery-side persistence the Dispatcher drives.
type Store interface {
	// ClaimDue atomically leases up to limit due deliveries (next_attempt_at <=
	// now, not delivered, attempts < maxAttempts), pushing their next_attempt_at
	// forward by lease so a concurrent worker/instance won't also claim them.
	ClaimDue(ctx context.Context, limit, maxAttempts int, lease time.Duration) ([]Delivery, error)
	MarkDelivered(ctx context.Context, publicID string) error
	// Reschedule bumps attempts and sets the next attempt time (backoff) + error.
	Reschedule(ctx context.Context, publicID string, nextAttempt time.Time, lastErr string) error
	// Defer pushes the next attempt time WITHOUT bumping attempts — used when the
	// endpoint's circuit is open (delivery wasn't tried, so it isn't a failure).
	Defer(ctx context.Context, publicID string, nextAttempt time.Time) error
	// GiveUp stamps a delivery abandoned (delivered_at set, with the error) so it
	// leaves the backlog after attempts are exhausted or the endpoint is gone.
	GiveUp(ctx context.Context, publicID, lastErr string) error
}

// Resolver resolves an agent's current push target (endpoint URL + token). It is
// consulted at SEND time, so a rotated secret or changed endpoint is picked up.
type Resolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// Sender delivers a signed notification. *agentclient.Client satisfies it.
type Sender interface {
	Event(ctx context.Context, t agentclient.Target, n agentclient.EventNotification) error
	GameEnd(ctx context.Context, t agentclient.Target, n agentclient.GameEndNotification) error
}

// Config tunes the dispatcher. Zero values fall back to sensible defaults.
type Config struct {
	Interval    time.Duration // poll cadence (default 500ms)
	Batch       int           // deliveries claimed per tick (default 100)
	Workers     int           // concurrent deliveries per tick (default 8)
	MaxAttempts int           // give up after this many failed attempts (default 12)
	BaseBackoff time.Duration // first retry delay; doubles each attempt (default 2s)
	MaxBackoff  time.Duration // backoff ceiling (default 5m)
	Lease       time.Duration // claim lease; a dead worker's rows re-due after this (default 30s)
	// MaxAge abandons a delivery this long after it was enqueued, whatever its
	// attempt count (default 24h).
	//
	// This is a cost control and a correctness one. An endpoint whose circuit stays
	// open is DEFERRED rather than retried — correctly, since nothing was tried — but
	// a defer rewrites next_attempt_at, which is an indexed column, so every one is a
	// non-HOT update that adds an entry to every index on the table. Measured on the
	// lab database: 79.5 million updates against 93,831 live rows, ZERO of them HOT,
	// 242,591 dead tuples standing, autovacuum triggered 3,339 times and still behind,
	// and as a result claiming 64 due deliveries walked 883 MB of bloated index.
	//
	// The deliveries doing that were unwinnable: a dead endpoint deferred every
	// cooldown, forever, with no attempt ever counted. Capping by age retires them.
	// It also happens to be what the product wants — a game event delivered a day
	// late is of no use to the agent that missed it — so the bound is not a tuning
	// knob bolted onto a broken loop, it is the missing half of the retry policy.
	MaxAge time.Duration
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = 500 * time.Millisecond
	}
	if c.Batch <= 0 {
		c.Batch = 100
	}
	if c.Workers <= 0 {
		c.Workers = 8
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 12
	}
	if c.BaseBackoff <= 0 {
		c.BaseBackoff = 2 * time.Second
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 5 * time.Minute
	}
	if c.Lease <= 0 {
		c.Lease = 30 * time.Second
	}
	if c.MaxAge <= 0 {
		c.MaxAge = 24 * time.Hour
	}
	return c
}

// Dispatcher delivers queued webhooks. Construct with NewDispatcher, then Run.
type Dispatcher struct {
	store    Store
	resolver Resolver
	sender   Sender
	health   *HealthTracker
	log      *slog.Logger
	cfg      Config
	now      func() time.Time
}

// NewDispatcher wires a dispatcher. health may be nil (delivery is then never
// health-gated), though production always passes one.
func NewDispatcher(store Store, resolver Resolver, sender Sender, health *HealthTracker, log *slog.Logger, cfg Config) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{
		store: store, resolver: resolver, sender: sender, health: health,
		log: log, cfg: cfg.withDefaults(), now: time.Now,
	}
}

// Run polls until ctx is cancelled.
func (d *Dispatcher) Run(ctx context.Context) {
	d.log.Info("webhook dispatcher started", "interval", d.cfg.Interval.String())
	t := time.NewTicker(d.cfg.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.tick(ctx); err != nil {
				d.log.Error("webhook dispatch tick failed", "error", err)
			}
		}
	}
}

func (d *Dispatcher) tick(ctx context.Context) error {
	batch, err := d.store.ClaimDue(ctx, d.cfg.Batch, d.cfg.MaxAttempts, d.cfg.Lease)
	if err != nil {
		return err
	}
	if len(batch) == 0 {
		return nil
	}
	sem := make(chan struct{}, d.cfg.Workers)
	var wg sync.WaitGroup
	for _, dl := range batch {
		wg.Add(1)
		sem <- struct{}{}
		go func(dl Delivery) {
			defer wg.Done()
			defer func() { <-sem }()
			d.process(ctx, dl)
		}(dl)
	}
	wg.Wait()
	return nil
}

// process delivers one leased delivery, then records the outcome.
func (d *Dispatcher) process(ctx context.Context, dl Delivery) {
	// THE BACKLOG HAS A MAXIMUM AGE, and it is checked before anything else.
	//
	// Placed here rather than in the circuit-breaker branch below on purpose. The
	// defer path is the one that was unbounded, but a bound that only exists on one
	// branch is a bound that the next branch added will not have — and the reason to
	// abandon a day-old event does not depend on WHY it is a day old. Ahead of the
	// resolver too, so an expired delivery costs no lookup.
	//
	// A zero CreatedAt means the row predates the column being selected; treated as
	// ageless rather than as instantly expired, because guessing "very old" here would
	// silently bin a live backlog on the deploy that introduced this.
	if !dl.CreatedAt.IsZero() && d.now().Sub(dl.CreatedAt) > d.cfg.MaxAge {
		d.log.Info("webhook delivery abandoned: older than the backlog window",
			"delivery", dl.PublicID, "agent", dl.AgentPublicID,
			"age", d.now().Sub(dl.CreatedAt).Round(time.Second).String(),
			"max_age", d.cfg.MaxAge.String(), "attempts", dl.Attempts)
		_ = d.store.GiveUp(ctx, dl.PublicID, "older than the backlog window")
		return
	}

	target, found, err := d.resolver.PlayTarget(ctx, dl.AgentPublicID)
	if err != nil {
		// Transient resolve failure (e.g. DB blip) — retry with backoff.
		d.fail(ctx, dl, "resolve target: "+err.Error())
		return
	}
	if !found || target.EndpointURL == "" {
		// The agent no longer has an active verified endpoint — abandon; retrying
		// can't help until they re-register (a fresh match re-enqueues then).
		_ = d.store.GiveUp(ctx, dl.PublicID, "no active verified endpoint")
		return
	}

	// Circuit breaker: skip (defer) delivery to an unhealthy endpoint without
	// counting it as a failed attempt — the monitor will re-close the circuit when
	// the endpoint recovers.
	if d.health != nil && !d.health.Allow(target.EndpointURL) {
		_ = d.store.Defer(ctx, dl.PublicID, d.now().Add(d.health.Cooldown()))
		return
	}

	start := d.now()
	err = d.send(ctx, target, dl)
	latencyMs := int(d.now().Sub(start).Milliseconds())
	if err != nil {
		if d.health != nil {
			d.health.RecordFailure(target.EndpointURL, err.Error())
		}
		d.fail(ctx, dl, err.Error())
		return
	}
	if d.health != nil {
		d.health.RecordSuccess(target.EndpointURL, latencyMs)
	}
	if err := d.store.MarkDelivered(ctx, dl.PublicID); err != nil {
		d.log.Error("webhook mark delivered failed", "delivery", dl.PublicID, "error", err)
	}
}

func (d *Dispatcher) send(ctx context.Context, target agentclient.Target, dl Delivery) error {
	switch dl.Kind {
	case KindGameEnd:
		return d.sender.GameEnd(ctx, target, agentclient.GameEndNotification{
			MatchID: dl.MatchID, Game: dl.Game, Result: dl.Payload,
		})
	default: // KindEvent
		return d.sender.Event(ctx, target, agentclient.EventNotification{
			MatchID: dl.MatchID, Game: dl.Game, Seq: dl.Seq, Type: dl.EventType, Payload: dl.Payload,
		})
	}
}

// fail either reschedules with exponential backoff or gives up once attempts are
// exhausted. dl.Attempts is the count BEFORE this attempt (as claimed).
func (d *Dispatcher) fail(ctx context.Context, dl Delivery, reason string) {
	nextAttemptNum := dl.Attempts + 1
	if nextAttemptNum >= d.cfg.MaxAttempts {
		if err := d.store.GiveUp(ctx, dl.PublicID, reason); err != nil {
			d.log.Error("webhook give up failed", "delivery", dl.PublicID, "error", err)
		}
		d.log.Warn("webhook delivery abandoned", "delivery", dl.PublicID, "agent", dl.AgentPublicID,
			"match", dl.MatchID, "attempts", nextAttemptNum, "reason", reason)
		return
	}
	next := d.now().Add(backoff(d.cfg.BaseBackoff, d.cfg.MaxBackoff, dl.Attempts))
	if err := d.store.Reschedule(ctx, dl.PublicID, next, reason); err != nil {
		d.log.Error("webhook reschedule failed", "delivery", dl.PublicID, "error", err)
	}
}

// backoff returns base * 2^attempt, capped at max. attempt is 0-based.
func backoff(base, max time.Duration, attempt int) time.Duration {
	d := base
	for i := 0; i < attempt && d < max; i++ {
		d *= 2
	}
	if d > max {
		d = max
	}
	return d
}
