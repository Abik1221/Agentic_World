package subscription

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// DisabledBillingGateway is the subscription billing gateway when no Stripe is
// configured (Solana-only deployment). Subscription checkout/portal return 503;
// customer creation is a harmless no-op so callers don't error on lookups.
type DisabledBillingGateway struct{}

var errBillingDisabled = httpx.NewError(http.StatusServiceUnavailable, "subscriptions_unavailable",
	"Subscriptions are not available here.")

func (DisabledBillingGateway) CreateSubscriptionCheckout(context.Context, string, string, string, string, string) (string, string, error) {
	return "", "", errBillingDisabled
}
func (DisabledBillingGateway) CreatePortalSession(context.Context, string, string) (string, error) {
	return "", errBillingDisabled
}
func (DisabledBillingGateway) EnsureCustomer(_ context.Context, existingID, _ string) (string, error) {
	return existingID, nil
}
