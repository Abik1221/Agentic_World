package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/payments"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PaymentsRepo is the pgx implementation of payments.Repo: the idempotent Stripe
// webhook log plus the Connect-account linkage on users.
type PaymentsRepo struct{ db *pgxpool.Pool }

func NewPaymentsRepo(db *pgxpool.Pool) *PaymentsRepo { return &PaymentsRepo{db: db} }

var _ payments.Repo = (*PaymentsRepo)(nil)

// InsertEvent stores the event by id; ON CONFLICT DO NOTHING makes a redelivery a
// no-op insert. alreadySeen is true when the row already existed.
func (r *PaymentsRepo) InsertEvent(ctx context.Context, id, typ string, payload []byte) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO stripe_events (id, type, payload) VALUES ($1, $2, $3::jsonb)
		 ON CONFLICT (id) DO NOTHING`,
		id, typ, string(payload))
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() == 0, nil
}

func (r *PaymentsRepo) MarkProcessed(ctx context.Context, id string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE stripe_events SET processed_at = now() WHERE id = $1`, id)
	return err
}

func (r *PaymentsRepo) UnprocessedEvents(ctx context.Context, limit int) ([]payments.StoredEvent, error) {
	rows, err := r.db.Query(ctx,
		`SELECT id, type, payload FROM stripe_events
		 WHERE processed_at IS NULL ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []payments.StoredEvent
	for rows.Next() {
		var e payments.StoredEvent
		if err := rows.Scan(&e.ID, &e.Type, &e.Payload); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *PaymentsRepo) OwnerOfAgent(ctx context.Context, agentPublicID string) (string, error) {
	var owner string
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id FROM agents a JOIN users u ON u.id = a.owner_user_id WHERE a.public_id = $1`,
		agentPublicID).Scan(&owner)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return owner, err
}

func (r *PaymentsRepo) StripeConnectID(ctx context.Context, userPublicID string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(stripe_connect_id, '') FROM users WHERE public_id = $1`, userPublicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return id, err
}

func (r *PaymentsRepo) SetStripeConnectID(ctx context.Context, userPublicID, connectID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET stripe_connect_id = $2, updated_at = now() WHERE public_id = $1`,
		userPublicID, connectID)
	return err
}
