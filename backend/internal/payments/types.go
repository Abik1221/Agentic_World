// Package payments turns real money into coins (Tier 1) and lays the Stripe
// Connect groundwork for funded payouts (Tier 2). It owns Stripe Checkout session
// creation, signed + idempotent webhook processing (→ ledger top-up), refund/
// chargeback reversal, Connect onboarding, and Stripe↔ledger reconciliation.
//
// External Stripe calls sit behind the Gateway port (DevGateway runs the whole
// flow offline; StripeGateway calls the real REST API). Coin movement goes
// through the Coiner port (satisfied by wallet.Service) so payments never touches
// the ledger or DB driver directly. See docs/stages/stage-05-payments.
package payments

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

// Stripe event types we act on.
const (
	EventCheckoutCompleted = "checkout.session.completed"
	EventPaymentSucceeded  = "payment_intent.succeeded"
	EventChargeRefunded    = "charge.refunded"
	EventDisputeCreated    = "charge.dispute.created"
)

// Pack is a purchasable bundle of coins. Defined in config, never in handlers.
type Pack struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Cents int64  `json:"price_cents"` // charged amount in the smallest currency unit
	Coins int64  `json:"coins"`       // coins credited on success
}

// DefaultPacks is the launch price list ($1=100, $5=550, $20=2400, $50=6500).
func DefaultPacks() []Pack {
	return []Pack{
		{Key: "starter", Label: "$1 — 100 coins", Cents: 100, Coins: 100},
		{Key: "plus", Label: "$5 — 550 coins (+10%)", Cents: 500, Coins: 550},
		{Key: "pro", Label: "$20 — 2,400 coins (+20%)", Cents: 2000, Coins: 2400},
		{Key: "whale", Label: "$50 — 6,500 coins (+30%)", Cents: 5000, Coins: 6500},
	}
}

// CheckoutParams is what the gateway needs to open a hosted Checkout session.
type CheckoutParams struct {
	Pack               Pack
	UserPublicID       string
	AgentPublicID      string // optional; for UI hint only after purchase
	ProcessingFeeCents int64
	SuccessURL         string
	CancelURL          string
}

// Checkout is the created hosted session (the URL the client is redirected to).
type Checkout struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

// CheckoutRecord is a Stripe-side completed session, used by reconciliation to
// detect a top-up that never produced a (received) webhook.
type CheckoutRecord struct {
	EventID       string
	SessionID     string
	AgentPublicID string
	UserPublicID  string
	Coins         int64
}

// Event is the parsed, domain-relevant slice of a Stripe webhook event.
type Event struct {
	ID            string
	Type          string
	ObjectID      string
	AgentPublicID string
	UserPublicID  string
	Coins         int64
	AmountCents   int64
	Payload       []byte
}

// Gateway is the Stripe boundary. DevGateway implements it offline; StripeGateway
// calls the real API. Webhook signature verification is NOT here — it is shared,
// stdlib-only, and lives in webhook.go (see VerifySignature).
type Gateway interface {
	CreateCheckout(ctx context.Context, p CheckoutParams) (Checkout, error)
	// EnsureConnectAccount returns existingID unchanged if non-empty, else creates
	// a new Express account and returns its id.
	EnsureConnectAccount(ctx context.Context, existingID, userPublicID string) (string, error)
	CreateOnboardingLink(ctx context.Context, accountID, returnURL, refreshURL string) (string, error)
	// ListRecentCheckouts returns completed sessions since `since` for reconciliation.
	ListRecentCheckouts(ctx context.Context, since time.Time) ([]CheckoutRecord, error)
}

// Coiner moves coins via the ledger. Satisfied by wallet.Service.
type Coiner interface {
	Topup(ctx context.Context, userPublicID string, coins int64, idemKey string) error
	Reverse(ctx context.Context, userPublicID string, coins int64, idemKey string) error
}

// Repo persists the idempotent webhook log and Stripe linkage on users.
type Repo interface {
	// InsertEvent stores the event by id (persist-first); alreadySeen reports a redelivery.
	InsertEvent(ctx context.Context, id, typ string, payload []byte) (alreadySeen bool, err error)
	MarkProcessed(ctx context.Context, id string) error
	UnprocessedEvents(ctx context.Context, limit int) ([]StoredEvent, error)
	OwnerOfAgent(ctx context.Context, agentPublicID string) (string, error)
	StripeConnectID(ctx context.Context, userPublicID string) (string, error)
	SetStripeConnectID(ctx context.Context, userPublicID, connectID string) error
}

// StoredEvent is a persisted webhook awaiting (re)processing.
type StoredEvent struct {
	ID      string
	Type    string
	Payload []byte
}

// parseEvent extracts the domain-relevant fields from a raw Stripe event body.
func parseEvent(payload []byte) (Event, error) {
	var env struct {
		ID   string `json:"id"`
		Type string `json:"type"`
		Data struct {
			Object json.RawMessage `json:"object"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return Event{}, err
	}
	var obj struct {
		ID          string            `json:"id"`
		AmountTotal int64             `json:"amount_total"`
		Amount      int64             `json:"amount"`
		Metadata    map[string]string `json:"metadata"`
	}
	_ = json.Unmarshal(env.Data.Object, &obj) // metadata-less objects are fine

	coins, _ := strconv.ParseInt(obj.Metadata["coins"], 10, 64)
	cents := obj.AmountTotal
	if cents == 0 {
		cents = obj.Amount
	}
	return Event{
		ID:            env.ID,
		Type:          env.Type,
		ObjectID:      obj.ID,
		AgentPublicID: obj.Metadata["agent"],
		UserPublicID:  obj.Metadata["user"],
		Coins:         coins,
		AmountCents:   cents,
		Payload:       payload,
	}, nil
}
