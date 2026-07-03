package payments

import (
	"context"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/platform"
)

// DevGateway implements Gateway without touching Stripe, so the full top-up and
// onboarding flow runs offline in local/test. NEVER used in prod.
type DevGateway struct {
	mu      sync.Mutex
	sessions []CheckoutRecord
}

func (g *DevGateway) CreateCheckout(_ context.Context, p CheckoutParams) (Checkout, error) {
	id := platform.NewID("cs_dev")
	g.mu.Lock()
	g.sessions = append(g.sessions, CheckoutRecord{
		SessionID: id, UserPublicID: p.UserPublicID, AgentPublicID: p.AgentPublicID, Coins: p.Pack.Coins,
	})
	g.mu.Unlock()
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

func (g *DevGateway) ListRecentCheckouts(_ context.Context, since time.Time) ([]CheckoutRecord, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	var out []CheckoutRecord
	for _, s := range g.sessions {
		out = append(out, s)
	}
	_ = since
	return out, nil
}
