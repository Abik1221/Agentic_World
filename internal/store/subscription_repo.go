package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/subscription"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type SubscriptionRepo struct{ db *pgxpool.Pool }

func NewSubscriptionRepo(db *pgxpool.Pool) *SubscriptionRepo { return &SubscriptionRepo{db: db} }

var _ subscription.Repo = (*SubscriptionRepo)(nil)

func (r *SubscriptionRepo) GetByUser(ctx context.Context, userPublicID string) (subscription.Record, error) {
	var rec subscription.Record
	var end *time.Time
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id, COALESCE(s.stripe_customer_id,''), COALESCE(s.stripe_subscription_id,''),
		        COALESCE(s.plan_key,'arena_pass'), COALESCE(s.status,'inactive'), s.current_period_end,
		        COALESCE(s.monthly_coins, 1000)
		 FROM users u
		 LEFT JOIN subscriptions s ON s.user_id = u.id
		 WHERE u.public_id = $1`, userPublicID).
		Scan(&rec.UserPublicID, &rec.StripeCustomerID, &rec.StripeSubscriptionID,
			&rec.PlanKey, &rec.Status, &end, &rec.MonthlyCoins)
	if errors.Is(err, pgx.ErrNoRows) {
		return subscription.Record{}, httpx.ErrNotFound
	}
	rec.CurrentPeriodEnd = end
	return rec, err
}

func (r *SubscriptionRepo) Upsert(ctx context.Context, rec subscription.Record) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO subscriptions (user_id, stripe_customer_id, stripe_subscription_id, plan_key, status, current_period_end, monthly_coins)
		 SELECT u.id, NULLIF($2,''), NULLIF($3,''), $4, $5, $6, $7 FROM users u WHERE u.public_id = $1
		 ON CONFLICT (user_id) DO UPDATE SET
		   stripe_customer_id = COALESCE(NULLIF(EXCLUDED.stripe_customer_id,''), subscriptions.stripe_customer_id),
		   stripe_subscription_id = COALESCE(NULLIF(EXCLUDED.stripe_subscription_id,''), subscriptions.stripe_subscription_id),
		   plan_key = EXCLUDED.plan_key, status = EXCLUDED.status,
		   current_period_end = EXCLUDED.current_period_end, monthly_coins = EXCLUDED.monthly_coins,
		   updated_at = now()`,
		rec.UserPublicID, rec.StripeCustomerID, rec.StripeSubscriptionID,
		rec.PlanKey, rec.Status, rec.CurrentPeriodEnd, rec.MonthlyCoins)
	return err
}

func (r *SubscriptionRepo) SetStripeCustomer(ctx context.Context, userPublicID, customerID string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO subscriptions (user_id, stripe_customer_id, plan_key, status, monthly_coins)
		 SELECT u.id, $2, 'arena_pass', 'inactive', 1000 FROM users u WHERE u.public_id = $1
		 ON CONFLICT (user_id) DO UPDATE SET stripe_customer_id = EXCLUDED.stripe_customer_id, updated_at = now()`,
		userPublicID, customerID)
	return err
}

func (r *SubscriptionRepo) StripeCustomer(ctx context.Context, userPublicID string) (string, error) {
	var id string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(s.stripe_customer_id, u.stripe_customer_id, '')
		 FROM users u LEFT JOIN subscriptions s ON s.user_id = u.id WHERE u.public_id = $1`,
		userPublicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", httpx.ErrNotFound
	}
	return id, err
}

func (r *SubscriptionRepo) GrantExists(ctx context.Context, invoiceID string) (bool, error) {
	var n int
	err := r.db.QueryRow(ctx, `SELECT COUNT(*) FROM subscription_grants WHERE stripe_invoice_id = $1`, invoiceID).Scan(&n)
	return n > 0, err
}

func (r *SubscriptionRepo) RecordGrant(ctx context.Context, userPublicID, invoiceID string, coins int64) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO subscription_grants (user_id, stripe_invoice_id, coins)
		 SELECT u.id, $2, $3 FROM users u WHERE u.public_id = $1`,
		userPublicID, invoiceID, coins)
	return err
}

func (r *SubscriptionRepo) UserID(ctx context.Context, userPublicID string) (int64, error) {
	var id int64
	err := r.db.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, userPublicID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, httpx.ErrNotFound
	}
	return id, err
}
