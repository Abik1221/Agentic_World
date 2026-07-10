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

// Stripe event types we act on. Coins credit on a SETTLED payment only:
// synchronous methods (card) settle at checkout.session.completed with
// payment_status=paid; asynchronous methods (ACH debit, some bank/PayPal flows)
// complete first as "unpaid" and settle later via async_payment_succeeded — so
// we must wait for that event rather than credit on completion.
const (
	EventCheckoutCompleted      = "checkout.session.completed"
	EventCheckoutAsyncSucceeded = "checkout.session.async_payment_succeeded"
	EventCheckoutAsyncFailed    = "checkout.session.async_payment_failed"
	EventCheckoutExpired        = "checkout.session.expired"
	EventPaymentSucceeded       = "payment_intent.succeeded"
	EventChargeRefunded         = "charge.refunded"
	EventDisputeCreated         = "charge.dispute.created"
	// Payout side: a transfer we made to a connected account was reversed (money
	// clawed back after we already burned the coins) — reconcile by re-crediting.
	EventTransferReversed = "transfer.reversed"
	// A connected account changed (e.g. KYC/onboarding finished → payouts_enabled
	// flips true) — auto-clear that account's KYC-blocked pending withdrawals.
	EventAccountUpdated = "account.updated"
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
	ID             string
	Type           string
	ObjectID       string
	AgentPublicID  string
	UserPublicID   string
	Coins          int64
	AmountCents    int64
	PaymentStatus  string // checkout session payment_status: paid|unpaid|no_payment_required
	PayoutsEnabled bool   // account.updated: connected account can now receive payouts
	// PaymentIntentID links a checkout session to its charge/dispute (all three
	// carry it). It is the key of the coin_purchases mapping used for clawback.
	PaymentIntentID string
	// AmountRefunded is the refunded/disputed cents on a charge.refunded /
	// charge.dispute.created event (drives proportional partial-refund clawback).
	AmountRefunded int64
	Payload        []byte
}

// Purchase is a settled top-up recorded at credit time, keyed by PaymentIntent,
// so a later refund/dispute (whose event carries only payment_intent) can be
// clawed back with the right coins/user/agent.
type Purchase struct {
	PaymentIntentID string
	SessionID       string
	UserPublicID    string
	AgentPublicID   string
	Coins           int64
	AmountCents     int64
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
	// Reverse claws back `coins` from the user's treasury on a refund/chargeback;
	// any shortfall (already spent) is booked as bad debt AND recorded against
	// agentPublicID so the payout debt-gate can block that agent's next cash-out.
	Reverse(ctx context.Context, userPublicID, agentPublicID string, coins int64, idemKey string) error
}

// PayoutReconciler handles the payout side of Stripe events (transfer reversals),
// so the single webhook endpoint drives both deposits and cash-out reconciliation.
// Satisfied by payout.Service; nil disables payout reconciliation.
type PayoutReconciler interface {
	ReverseByTransfer(ctx context.Context, transferID, reason string) error
	// OnAccountUpdated fires when a connected account changes; when payouts become
	// enabled it clears that account's KYC-blocked pending withdrawals.
	OnAccountUpdated(ctx context.Context, connectAccountID string, payoutsEnabled bool) error
}

// Repo persists the idempotent webhook log and Stripe linkage on users.
type Repo interface {
	// InsertEvent stores the event by id (persist-first); alreadySeen reports a redelivery.
	InsertEvent(ctx context.Context, id, typ string, payload []byte) (alreadySeen bool, err error)
	MarkProcessed(ctx context.Context, id string) error
	UnprocessedEvents(ctx context.Context, limit int) ([]StoredEvent, error)
	// RecordPurchase persists a settled top-up keyed by PaymentIntent (idempotent
	// upsert) so a later refund/dispute can be clawed back. No-op when the
	// PaymentIntent id is empty (e.g. DevGateway/legacy sessions).
	RecordPurchase(ctx context.Context, p Purchase) error
	// PurchaseByPaymentIntent returns the recorded purchase for a PaymentIntent,
	// or found=false if none (e.g. a refund we can't map).
	PurchaseByPaymentIntent(ctx context.Context, paymentIntentID string) (Purchase, bool, error)
	// ReverseToLevel raises the cumulative reversed-coins high-water mark for a
	// PaymentIntent to `target` under a row lock, invoking reverse(delta) for the
	// additional coins (delta = target − previous, > 0) BEFORE persisting the new
	// level — so a crash re-runs the idempotent reverse rather than under-clawing.
	// A target at or below the current level is a no-op (reverse is not called).
	// This lets multiple partial refunds on one PaymentIntent each claw their share
	// while a dispute-then-refund still claws at most once.
	ReverseToLevel(ctx context.Context, paymentIntentID string, target int64, reverse func(delta int64) error) error
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
		ID             string            `json:"id"`
		AmountTotal    int64             `json:"amount_total"`
		Amount         int64             `json:"amount"`
		AmountRefunded int64             `json:"amount_refunded"`
		PaymentIntent  string            `json:"payment_intent"`
		PaymentStatus  string            `json:"payment_status"`
		PayoutsEnabled bool              `json:"payouts_enabled"`
		Metadata       map[string]string `json:"metadata"`
	}
	_ = json.Unmarshal(env.Data.Object, &obj) // metadata-less objects are fine

	coins, _ := strconv.ParseInt(obj.Metadata["coins"], 10, 64)
	cents := obj.AmountTotal
	if cents == 0 {
		cents = obj.Amount
	}
	// Refunded/disputed amount: charge.refunded carries amount_refunded; a dispute
	// object carries the disputed amount in `amount`.
	refunded := obj.AmountRefunded
	if refunded == 0 && env.Type == EventDisputeCreated {
		refunded = obj.Amount
	}
	return Event{
		ID:              env.ID,
		Type:            env.Type,
		ObjectID:        obj.ID,
		AgentPublicID:   obj.Metadata["agent"],
		UserPublicID:    obj.Metadata["user"],
		Coins:           coins,
		AmountCents:     cents,
		PaymentStatus:   obj.PaymentStatus,
		PayoutsEnabled:  obj.PayoutsEnabled,
		PaymentIntentID: obj.PaymentIntent,
		AmountRefunded:  refunded,
		Payload:         payload,
	}, nil
}
