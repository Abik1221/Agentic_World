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

// RecordPurchase upserts the settled top-up keyed by PaymentIntent (idempotent:
// a redelivered completion event is a no-op), so a later refund/dispute can map
// payment_intent -> {user, agent, coins, amount} for clawback.
func (r *PaymentsRepo) RecordPurchase(ctx context.Context, p payments.Purchase) error {
	if p.PaymentIntentID == "" {
		return nil
	}
	var agent *string
	if p.AgentPublicID != "" {
		agent = &p.AgentPublicID
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO coin_purchases
		   (payment_intent, session_id, user_public_id, agent_public_id, coins, amount_cents)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (payment_intent) DO NOTHING`,
		p.PaymentIntentID, p.SessionID, p.UserPublicID, agent, p.Coins, p.AmountCents)
	return err
}

func (r *PaymentsRepo) PurchaseByPaymentIntent(ctx context.Context, paymentIntentID string) (payments.Purchase, bool, error) {
	var p payments.Purchase
	var agent *string
	err := r.db.QueryRow(ctx,
		`SELECT payment_intent, session_id, user_public_id, agent_public_id, coins, amount_cents
		 FROM coin_purchases WHERE payment_intent = $1`, paymentIntentID).
		Scan(&p.PaymentIntentID, &p.SessionID, &p.UserPublicID, &agent, &p.Coins, &p.AmountCents)
	if errors.Is(err, pgx.ErrNoRows) {
		return payments.Purchase{}, false, nil
	}
	if err != nil {
		return payments.Purchase{}, false, err
	}
	if agent != nil {
		p.AgentPublicID = *agent
	}
	return p, true, nil
}

// ReverseToLevel serializes refund clawbacks for a PaymentIntent on its
// coin_purchases row (SELECT ... FOR UPDATE), so two refund events for the same
// purchase can't both reverse from a stale high-water mark. It reverses the delta
// up to `target` and only then persists the new level; a crash after the reverse
// but before the commit rolls back, and the retry re-runs the (idempotency-keyed)
// reverse and re-persists — reversing at most once in total.
func (r *PaymentsRepo) ReverseToLevel(ctx context.Context, paymentIntentID string, target int64, reverse func(delta int64) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	var prev int64
	err = tx.QueryRow(ctx,
		`SELECT reversed_coins FROM coin_purchases WHERE payment_intent = $1 FOR UPDATE`,
		paymentIntentID).Scan(&prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // no recorded purchase to reverse
	}
	if err != nil {
		return err
	}
	delta := target - prev
	if delta <= 0 {
		return tx.Commit(ctx) // already reversed to at least this level
	}
	if err := reverse(delta); err != nil {
		return err // level unchanged; a retry re-runs the idempotent reverse
	}
	if _, err := tx.Exec(ctx,
		`UPDATE coin_purchases SET reversed_coins = $2 WHERE payment_intent = $1`,
		paymentIntentID, target); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
