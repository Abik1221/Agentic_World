package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/webhook"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WebhookRepo is the durable queue behind the push-protocol /event + /game-end
// delivery path. It implements both webhook.Enqueuer (the game drive loops write
// deliveries) and webhook.Store (the dispatcher claims/completes them).
type WebhookRepo struct{ db *pgxpool.Pool }

// NewWebhookRepo wires the repo to the pool.
func NewWebhookRepo(db *pgxpool.Pool) *WebhookRepo { return &WebhookRepo{db: db} }

var (
	_ webhook.Enqueuer = (*WebhookRepo)(nil)
	_ webhook.Store    = (*WebhookRepo)(nil)
)

// EnqueueEvent appends an /event delivery. Idempotent: the unique
// (agent, match, kind, seq) index makes a re-enqueue a no-op, so a drive loop
// that re-reads the same event can call this freely.
func (r *WebhookRepo) EnqueueEvent(ctx context.Context, agentPublicID, game, matchID string, seq int, eventType string, payload []byte) error {
	return r.enqueue(ctx, agentPublicID, webhook.KindEvent, game, matchID, seq, eventType, payload)
}

// EnqueueGameEnd appends a /game-end delivery (seq 0, one per match).
func (r *WebhookRepo) EnqueueGameEnd(ctx context.Context, agentPublicID, game, matchID string, payload []byte) error {
	return r.enqueue(ctx, agentPublicID, webhook.KindGameEnd, game, matchID, 0, "", payload)
}

func (r *WebhookRepo) enqueue(ctx context.Context, agentPublicID, kind, game, matchID string, seq int, eventType string, payload []byte) error {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	id := platform.NewID(platform.PrefixWebhook)
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_webhook_deliveries
		   (public_id, agent_public_id, kind, game, match_public_id, seq, event_type, payload)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb)
		 ON CONFLICT (agent_public_id, match_public_id, kind, seq) DO NOTHING`,
		id, agentPublicID, kind, game, matchID, seq, eventType, payload)
	return err
}

// ClaimDue atomically leases up to limit due deliveries. It pushes each claimed
// row's next_attempt_at forward by lease (so an overlapping tick or another
// instance won't re-claim it) and uses FOR UPDATE SKIP LOCKED so concurrent
// claimers never block or double-claim. A worker that dies mid-delivery lets the
// lease lapse, making the row due again — at-least-once.
func (r *WebhookRepo) ClaimDue(ctx context.Context, limit, maxAttempts int, lease time.Duration) ([]webhook.Delivery, error) {
	rows, err := r.db.Query(ctx,
		`UPDATE agent_webhook_deliveries
		 SET next_attempt_at = now() + $3::interval
		 WHERE id IN (
		     SELECT id FROM agent_webhook_deliveries
		     WHERE delivered_at IS NULL AND next_attempt_at <= now() AND attempts < $2
		     ORDER BY next_attempt_at
		     LIMIT $1
		     FOR UPDATE SKIP LOCKED
		 )
		 RETURNING public_id, agent_public_id, kind, game, match_public_id, seq, event_type, payload, attempts, created_at`,
		limit, maxAttempts, lease.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []webhook.Delivery
	for rows.Next() {
		var d webhook.Delivery
		if err := rows.Scan(&d.PublicID, &d.AgentPublicID, &d.Kind, &d.Game,
			&d.MatchID, &d.Seq, &d.EventType, &d.Payload, &d.Attempts, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// MarkDelivered stamps a delivery successfully delivered.
func (r *WebhookRepo) MarkDelivered(ctx context.Context, publicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE agent_webhook_deliveries SET delivered_at = now(), last_error = '' WHERE public_id = $1`,
		publicID)
	return err
}

// Reschedule bumps the attempt count and sets the next attempt time + error.
func (r *WebhookRepo) Reschedule(ctx context.Context, publicID string, nextAttempt time.Time, lastErr string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE agent_webhook_deliveries
		 SET attempts = attempts + 1, next_attempt_at = $2, last_error = $3
		 WHERE public_id = $1`,
		publicID, nextAttempt, truncErr(lastErr))
	return err
}

// Defer pushes the next attempt time without counting a failed attempt (used when
// the endpoint's circuit is open — delivery was never tried).
func (r *WebhookRepo) Defer(ctx context.Context, publicID string, nextAttempt time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE agent_webhook_deliveries SET next_attempt_at = $2 WHERE public_id = $1`,
		publicID, nextAttempt)
	return err
}

// GiveUp abandons a delivery (stamps delivered_at so it leaves the backlog) after
// attempts are exhausted or the endpoint is permanently gone.
func (r *WebhookRepo) GiveUp(ctx context.Context, publicID, lastErr string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE agent_webhook_deliveries SET delivered_at = now(), last_error = $2 WHERE public_id = $1`,
		publicID, truncErr(lastErr))
	return err
}

// truncErr caps a stored error string so a pathological message can't bloat rows.
func truncErr(s string) string {
	const max = 500
	if len(s) > max {
		return s[:max]
	}
	return s
}
