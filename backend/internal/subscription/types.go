// Package subscription implements Arena Pass — recurring Stripe Billing with
// monthly coin grants to the owner's treasury wallet.
package subscription

import (
	"context"
	"time"
)

const (
	PlanArenaPass  = "arena_pass"
	StatusActive   = "active"
	StatusPastDue  = "past_due"
	StatusCanceled = "canceled"
	StatusInactive = "inactive"
)

// Plan describes a sellable subscription tier.
type Plan struct {
	Key          string `json:"key"`
	Label        string `json:"label"`
	PriceCents   int64  `json:"price_cents"`
	MonthlyCoins int64  `json:"monthly_coins"`
	Currency     string `json:"currency"`
}

// Status is the user's subscription read model.
type Status struct {
	Plan               string     `json:"plan"`
	Status             string     `json:"status"`
	Active             bool       `json:"active"`
	MonthlyCoins       int64      `json:"monthly_coins"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end,omitempty"`
	ManageBillingAvail bool       `json:"manage_billing_available"`
}

// Repo persists subscription state.
type Repo interface {
	GetByUser(ctx context.Context, userPublicID string) (Record, error)
	Upsert(ctx context.Context, r Record) error
	SetStripeCustomer(ctx context.Context, userPublicID, customerID string) error
	StripeCustomer(ctx context.Context, userPublicID string) (string, error)
	GrantExists(ctx context.Context, invoiceID string) (bool, error)
	RecordGrant(ctx context.Context, userPublicID, invoiceID string, coins int64) error
	UserID(ctx context.Context, userPublicID string) (int64, error)
}

// Record is the persisted subscription row.
type Record struct {
	UserPublicID         string
	StripeCustomerID     string
	StripeSubscriptionID string
	PlanKey              string
	Status               string
	CurrentPeriodEnd     *time.Time
	MonthlyCoins         int64
}

// Coiner credits the treasury on paid invoices.
type Coiner interface {
	Topup(ctx context.Context, userPublicID string, coins int64, idemKey string) error
}

// BillingGateway opens Stripe Checkout / Customer Portal sessions.
type BillingGateway interface {
	CreateSubscriptionCheckout(ctx context.Context, customerID, userPublicID, priceID, successURL, cancelURL string) (checkoutURL, sessionID string, err error)
	CreatePortalSession(ctx context.Context, customerID, returnURL string) (string, error)
	EnsureCustomer(ctx context.Context, existingID, userPublicID string) (string, error)
}
