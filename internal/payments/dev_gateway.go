package payments

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/platform"
)

// DevGateway implements Gateway without touching Stripe, so the full top-up and
// onboarding flow runs offline in local/test. It mints synthetic ids and points
// the client straight at the success page. NEVER used in prod (main selects
// StripeGateway whenever a secret key is configured).
type DevGateway struct{}

func (DevGateway) CreateCheckout(_ context.Context, p CheckoutParams) (Checkout, error) {
	id := platform.NewID("cs_dev")
	// Land directly on the success page; a real session id is echoed for parity.
	return Checkout{ID: id, URL: p.SuccessURL + "?session_id=" + id}, nil
}

func (DevGateway) EnsureConnectAccount(_ context.Context, existingID, _ string) (string, error) {
	if existingID != "" {
		return existingID, nil
	}
	return platform.NewID("acct_dev"), nil
}

func (DevGateway) CreateOnboardingLink(_ context.Context, accountID, returnURL, _ string) (string, error) {
	return returnURL + "?onboarded=dev&account=" + accountID, nil
}

func (DevGateway) ListRecentCheckouts(context.Context, time.Time) ([]CheckoutRecord, error) {
	return nil, nil // nothing to reconcile against in dev
}
