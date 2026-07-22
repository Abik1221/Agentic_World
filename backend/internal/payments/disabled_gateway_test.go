package payments

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
)

// The Solana-only deployment runs DisabledGateway. Its whole security value is:
// the fiat entry points fail (503) and NONE of them can credit coins — there is
// no Coiner reachable from here, so a card "top-up" can never mint balance.
func TestDisabledGateway_FailsClosedNoCredit(t *testing.T) {
	var g Gateway = DisabledGateway{}

	if _, err := g.CreateCheckout(context.Background(), CheckoutParams{}); !isUnavailable(t, err) {
		t.Fatalf("CreateCheckout: want 503 payments_unavailable, got %v", err)
	}
	if _, err := g.EnsureConnectAccount(context.Background(), "", "u"); !isUnavailable(t, err) {
		t.Fatalf("EnsureConnectAccount: want 503, got %v", err)
	}
	if _, err := g.CreateOnboardingLink(context.Background(), "acct", "r", "f"); !isUnavailable(t, err) {
		t.Fatalf("CreateOnboardingLink: want 503, got %v", err)
	}
	// Reconciliation is a no-op (nothing to reconcile), never an error.
	recs, err := g.ListRecentCheckouts(context.Background(), time.Time{})
	if err != nil || len(recs) != 0 {
		t.Fatalf("ListRecentCheckouts: want (nil,nil), got (%v,%v)", recs, err)
	}
}

func isUnavailable(t *testing.T, err error) bool {
	t.Helper()
	var api *httpx.APIError
	return errors.As(err, &api) && api.Status == http.StatusServiceUnavailable
}
