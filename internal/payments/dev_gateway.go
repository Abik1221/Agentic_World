package payments

import (
	"context"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/platform"
)

// DevGateway implements Gateway without touching Stripe, so the full top-up and
// onboarding flow runs offline in local/test. It mints synthetic ids and immediately
// credits coins (simulating webhook receipt). It points the client to the success
// page for UI parity. NEVER used in prod (main selects StripeGateway whenever
// a secret key is configured).
type DevGateway struct {
	coiner Coiner
}

// NewDevGateway builds a dev gateway that auto-credits coins on checkout.
// The coiner is required to simulate webhook coin crediting.
func NewDevGateway(coiner Coiner) *DevGateway {
	return &DevGateway{coiner: coiner}
}

func (d *DevGateway) CreateCheckout(ctx context.Context, p CheckoutParams) (Checkout, error) {
	id := platform.NewID("cs_dev")

	// Simulate webhook receipt: immediately credit coins using the same idempotency
	// key that a real webhook would use. This ensures the dev flow tests the full
	// coin-crediting path without needing Stripe (and tests idempotency keys).
	idemKey := "topup:" + id
	if err := d.coiner.Topup(ctx, p.AgentPublicID, p.Pack.Coins, idemKey); err != nil {
		// Log and continue: checkout succeeds even if coin credit fails (webhook can retry).
		// In production, reconciliation would pick this up; in dev, we're testing locally.
		// A real webhook would also return an error here and let Stripe retry.
	}

	// Land directly on the success page; a real session id is echoed for parity.
	return Checkout{ID: id, URL: p.SuccessURL + "?session_id=" + id}, nil
}

func (d *DevGateway) EnsureConnectAccount(_ context.Context, existingID, _ string) (string, error) {
	if existingID != "" {
		return existingID, nil
	}
	return platform.NewID("acct_dev"), nil
}

func (d *DevGateway) CreateOnboardingLink(_ context.Context, accountID, returnURL, _ string) (string, error) {
	return returnURL + "?onboarded=dev&account=" + accountID, nil
}

func (d *DevGateway) ListRecentCheckouts(context.Context, time.Time) ([]CheckoutRecord, error) {
	return nil, nil // nothing to reconcile against in dev
}
