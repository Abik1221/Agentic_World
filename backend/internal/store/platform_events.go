package store

import (
	"context"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/platformsign"
	"github.com/redis/go-redis/v9"
)

// platformEventsPublishTimeout bounds a single XADD so a slow/blocked Redis can't
// stall the whole event dispatcher (this handler runs on the dispatcher's tick
// goroutine). A timeout surfaces as a delivery failure → attempts bump → retry.
const platformEventsPublishTimeout = 3 * time.Second

// streamPlatformEvents is the cross-service domain-event log the Super Admin
// consumes (via a consumer group) to power the dashboard + analytics. See
// docs/architecture/platform-config-bus.md.
const streamPlatformEvents = "platform:events"

// platformEventsMaxLen caps the stream so a slow/absent consumer can't grow it
// unbounded; trimming is approximate (~) for O(1) XADD. This is a live feed, not
// the system of record — the transactional outbox in Postgres is authoritative.
const platformEventsMaxLen = 100_000

// PlatformEventStream mirrors delivered domain events onto a Redis Stream for the
// Super Admin. It is the cross-service arm of the transactional outbox: the
// dispatcher calls Publish as a regular idempotent handler, so an event reaches
// the stream iff it was durably recorded. Consumers key off the event id.
type PlatformEventStream struct {
	rdb    *redis.Client
	signer *platformsign.Signer // engine's private key; nil => unsigned (dev)
}

// NewPlatformEventStream wraps the shared Redis client. signer may be nil to
// publish unsigned events (dev/local); in that case the Admin consumer's verifier
// must also be disabled.
func NewPlatformEventStream(rdb *redis.Client, signer *platformsign.Signer) *PlatformEventStream {
	return &PlatformEventStream{rdb: rdb, signer: signer}
}

// platformEventSchemaVersion versions the platform:events envelope + payload
// contract. It travels in the signed envelope so a consumer can detect field
// drift (e.g. a renamed payload key) instead of silently mis-decoding — the class
// of bug that once zeroed the admin leaderboard Elo. Bump on any breaking payload
// change; the Super Admin mirror must recognize the value.
const platformEventSchemaVersion = "1"

// eventSigningInput is the exact byte string signed and verified for one event.
// Both sides MUST build it identically from the stream field STRINGS. The schema
// version is appended ONLY when non-empty, so pre-version events (no `v` field)
// still verify with the original id\ntype\npayload\nts form.
func eventSigningInput(id, typ, payload, ts, v string) []byte {
	b := id + "\n" + typ + "\n" + payload + "\n" + ts
	if v != "" {
		b += "\n" + v
	}
	return []byte(b)
}

// Publish appends one event to the stream. The event id is the idempotency key
// (safe for at-least-once re-delivery), and an Ed25519 signature over the entry
// lets the Admin consumer reject anything not produced by the engine.
func (s *PlatformEventStream) Publish(ctx context.Context, e events.Event) error {
	ctx, cancel := context.WithTimeout(ctx, platformEventsPublishTimeout)
	defer cancel()
	ts := strconv.FormatInt(e.CreatedAt.UnixMilli(), 10)
	payload := string(e.Payload)
	sig := s.signer.Sign(eventSigningInput(e.ID, e.Type, payload, ts, platformEventSchemaVersion))
	return s.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: streamPlatformEvents,
		MaxLen: platformEventsMaxLen,
		Approx: true,
		Values: map[string]any{
			"id":      e.ID,
			"type":    e.Type,
			"payload": payload,
			"ts":      ts,
			"v":       platformEventSchemaVersion,
			"sig":     sig,
		},
	}).Err()
}
