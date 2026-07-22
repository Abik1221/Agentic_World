package payments

import (
	"context"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
)

// DisabledGateway is the fiat (card) payment gateway for a Solana-only deployment:
// there is NO Stripe configured, so card top-ups are unavailable. It never credits
// coins (unlike the offline DevGateway), so it is safe in production — the only
// on-ramp is a real USDC deposit. Checkout/Connect calls return 503; the
// reconciler's ListRecentCheckouts is a no-op.
type DisabledGateway struct{}

var errFiatDisabled = httpx.NewError(http.StatusServiceUnavailable, "payments_unavailable",
	"Card payments are not available here. Fund your balance with a USDC deposit.")

func (DisabledGateway) CreateCheckout(context.Context, CheckoutParams) (Checkout, error) {
	return Checkout{}, errFiatDisabled
}
func (DisabledGateway) EnsureConnectAccount(context.Context, string, string) (string, error) {
	return "", errFiatDisabled
}
func (DisabledGateway) CreateOnboardingLink(context.Context, string, string, string) (string, error) {
	return "", errFiatDisabled
}
func (DisabledGateway) ListRecentCheckouts(context.Context, time.Time) ([]CheckoutRecord, error) {
	return nil, nil // nothing to reconcile
}
