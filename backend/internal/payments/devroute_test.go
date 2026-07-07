package payments_test

import (
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/payments"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// devConfirmPath is the coin-minting dev endpoint that MUST NOT exist in prod.
const devConfirmPath = "/v1/admin/dev/confirm-checkout"

func devRouteMounted(t *testing.T, devMode bool) bool {
	t.Helper()
	svc := payments.New(&payments.DevGateway{}, newCoiner(), &fakeRepo{},
		platform.FixedClock{T: clockT},
		payments.Config{Packs: payments.DefaultPacks(), WebhookSecret: secret, DevMode: devMode},
		slog.Default(), prometheus.NewRegistry())
	// authn is unused: chi.Walk enumerates routes without invoking middleware.
	h := payments.NewHandler(svc, nil)
	r := chi.NewRouter()
	h.Register(r)

	found := false
	_ = chi.Walk(r, func(_ string, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if strings.Contains(route, devConfirmPath) {
			found = true
		}
		return nil
	})
	return found
}

// TestDevConfirmRouteOnlyMountedInDevMode guards C1: the dev coin-mint endpoint
// must never be registered in a production (DevMode=false) configuration.
func TestDevConfirmRouteOnlyMountedInDevMode(t *testing.T) {
	if devRouteMounted(t, false) {
		t.Errorf("%s must NOT be mounted when DevMode=false (prod) — it mints coins with no charge", devConfirmPath)
	}
	if !devRouteMounted(t, true) {
		t.Errorf("%s should be mounted when DevMode=true (needed for local testing)", devConfirmPath)
	}
}
