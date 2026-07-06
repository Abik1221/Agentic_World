// Package events is the platform's domain event backbone (transactional outbox).
//
// Producers insert an event row in the SAME DB transaction as the state change
// (see store.InsertEventTx), guaranteeing an event is emitted iff the change
// committed. The Dispatcher polls unpublished rows and invokes registered
// handlers; only after every handler succeeds is the event stamped published.
//
// Delivery is at-least-once, so handlers MUST be idempotent (key off Event.ID).
// A poison event is retried up to maxAttempts, then skipped so it can never wedge
// the loop. Everything downstream — notifications, badges, analytics, live feeds
// — is a projection over this log, which keeps the platform auditable.
package events

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"
)

// Event types are past-tense facts. Add new ones here as producers emit them.
const (
	TypeAgentCertified = "agent.certified"
	TypeMatchStarted   = "match.started"
	TypeMatchFinished  = "match.finished"
	TypeSeasonRolled   = "season.rolled"
	TypeBadgeAwarded   = "badge.awarded"
	// Cross-service live-feed facts for the Super Admin mirror. See
	// docs/architecture/platform-events-contract.md. TypeTopupSucceeded is defined
	// for the contract but not yet emitted (topups commit inside the ledger's own
	// tx; the mirror picks them up via backfill until a hook lands).
	TypeDisputeOpened       = "dispute.opened"
	TypeWithdrawalRequested = "withdrawal.requested"
	TypeTopupSucceeded      = "topup.succeeded"
)

// Event is one persisted domain fact.
type Event struct {
	ID        string          // public id (evt_...) — the idempotency key
	Type      string          // e.g. "agent.certified"
	Payload   json.RawMessage // event-specific JSON
	CreatedAt time.Time
}

// Handler consumes an event. It must be idempotent: the same Event.ID may be
// delivered more than once. Returning an error leaves the event unpublished for
// a later retry.
type Handler func(ctx context.Context, e Event) error

// Repo is the outbox persistence port (implemented in internal/store).
type Repo interface {
	// Unpublished returns up to limit undelivered events (oldest first) whose
	// attempts are below the retry cap.
	Unpublished(ctx context.Context, limit, maxAttempts int) ([]Event, error)
	// MarkPublished stamps an event delivered.
	MarkPublished(ctx context.Context, publicID string) error
	// BumpAttempts records a failed delivery attempt.
	BumpAttempts(ctx context.Context, publicID string) error
}

// Dispatcher polls the outbox and fans events out to handlers.
type Dispatcher struct {
	repo        Repo
	log         *slog.Logger
	interval    time.Duration
	batch       int
	maxAttempts int
	handlers    map[string][]Handler
}

// New builds a dispatcher. interval<=0 defaults to 1s.
func New(repo Repo, log *slog.Logger, interval time.Duration) *Dispatcher {
	if interval <= 0 {
		interval = time.Second
	}
	return &Dispatcher{
		repo:        repo,
		log:         log,
		interval:    interval,
		batch:       100,
		maxAttempts: 10,
		handlers:    make(map[string][]Handler),
	}
}

// On registers a handler for an event type. Not safe to call after Run starts
// (call all On(...) during wiring).
func (d *Dispatcher) On(eventType string, h Handler) {
	d.handlers[eventType] = append(d.handlers[eventType], h)
}

// Run polls until ctx is cancelled. Mirrors the other background loops.
func (d *Dispatcher) Run(ctx context.Context) {
	d.log.Info("event dispatcher started", "interval", d.interval.String())
	t := time.NewTicker(d.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.tick(ctx); err != nil {
				d.log.Error("event dispatch tick failed", "error", err)
			}
		}
	}
}

// tick delivers one batch. An event is published only when every handler for its
// type succeeds; a failure bumps attempts and leaves it for the next tick.
func (d *Dispatcher) tick(ctx context.Context) error {
	batch, err := d.repo.Unpublished(ctx, d.batch, d.maxAttempts)
	if err != nil {
		return err
	}
	for _, e := range batch {
		if d.deliver(ctx, e) {
			if err := d.repo.MarkPublished(ctx, e.ID); err != nil {
				d.log.Error("mark published failed", "event", e.ID, "error", err)
			}
		} else {
			if err := d.repo.BumpAttempts(ctx, e.ID); err != nil {
				d.log.Error("bump attempts failed", "event", e.ID, "error", err)
			}
		}
	}
	return nil
}

// deliver runs every handler registered for the event's type. Returns true only
// if all succeed (an event with no handlers is trivially delivered).
func (d *Dispatcher) deliver(ctx context.Context, e Event) bool {
	ok := true
	for _, h := range d.handlers[e.Type] {
		if err := h(ctx, e); err != nil {
			ok = false
			d.log.Error("event handler failed", "event", e.ID, "type", e.Type, "error", err)
		}
	}
	return ok
}
