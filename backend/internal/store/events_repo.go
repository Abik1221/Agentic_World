package store

import (
	"context"

	"github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventsRepo implements events.Repo (the outbox dispatcher's persistence port).
// Producers emit inside their own transaction via InsertEventTx (a free function
// below), so this type only serves the dispatcher's read/mark path.
type EventsRepo struct{ db *pgxpool.Pool }

// NewEventsRepo wires the repo to the pool.
func NewEventsRepo(db *pgxpool.Pool) *EventsRepo { return &EventsRepo{db: db} }

var _ events.Repo = (*EventsRepo)(nil)

func (r *EventsRepo) Unpublished(ctx context.Context, limit, maxAttempts int) ([]events.Event, error) {
	rows, err := r.db.Query(ctx,
		`SELECT public_id, type, payload, created_at
		 FROM events
		 WHERE published_at IS NULL AND attempts < $2
		 ORDER BY id
		 LIMIT $1`, limit, maxAttempts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []events.Event
	for rows.Next() {
		var e events.Event
		if err := rows.Scan(&e.ID, &e.Type, &e.Payload, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *EventsRepo) MarkPublished(ctx context.Context, publicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE events SET published_at = now() WHERE public_id = $1`, publicID)
	return err
}

func (r *EventsRepo) BumpAttempts(ctx context.Context, publicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE events SET attempts = attempts + 1 WHERE public_id = $1`, publicID)
	return err
}

// InsertEvent appends a standalone event to the outbox on its own (single
// statement, implicitly atomic). Use this for facts that are NOT tied to another
// state change in the same tx — e.g. a per-match benchmark summary emitted after
// the match loop ends. Durable + at-least-once via the dispatcher. payload must
// be valid JSON. Returns the event's public id.
func InsertEvent(ctx context.Context, db *pgxpool.Pool, eventType string, payload []byte) (string, error) {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	id := platform.NewID(platform.PrefixEvent)
	_, err := db.Exec(ctx,
		`INSERT INTO events (public_id, type, payload) VALUES ($1, $2, $3::jsonb)`,
		id, eventType, payload)
	if err != nil {
		return "", err
	}
	return id, nil
}

// InsertEventTx appends an event to the outbox WITHIN the caller's transaction,
// so the event is emitted iff the surrounding state change commits (transactional
// outbox). payload must be valid JSON. Returns the event's public id.
func InsertEventTx(ctx context.Context, tx pgx.Tx, eventType string, payload []byte) (string, error) {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	id := platform.NewID(platform.PrefixEvent)
	_, err := tx.Exec(ctx,
		`INSERT INTO events (public_id, type, payload) VALUES ($1, $2, $3::jsonb)`,
		id, eventType, payload)
	if err != nil {
		return "", err
	}
	return id, nil
}
