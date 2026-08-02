package httpx

import (
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/config"
	mw "github.com/agent-arena/arena/internal/middleware"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// Deps are the cross-cutting dependencies the router needs. Domain modules are
// NOT referenced here — they attach their routes via the `mounts` registrars, so
// httpx stays decoupled from every module (no import cycles, easy to extend).
type Deps struct {
	Config  *config.Config
	Logger  *slog.Logger
	Metrics *platform.Metrics
}

// metricsHandler gates the Prometheus endpoint.
//
//   - METRICS_TOKEN set  → require `Authorization: Bearer <token>`.
//   - unset, prod        → 404, identical to any unknown path. A 401 would confirm
//     the endpoint exists; there is no reason to tell a scanner that.
//   - unset, non-prod    → open, so local debugging is unchanged.
//
// Comparison is constant-time: a byte-by-byte early exit leaks the token one
// character at a time to anyone willing to measure.
func metricsHandler(d Deps) http.Handler {
	inner := d.Metrics.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := d.Config.MetricsToken
		if token == "" {
			if d.Config.IsProd() {
				Error(w, ErrNotFound)
				return
			}
			inner.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			Error(w, ErrNotFound)
			return
		}
		inner.ServeHTTP(w, r)
	})
}

// Mount is a route registrar a module provides, e.g. health.Handler.Register or
// (later) identity.Module.Register. It receives the root router and adds its routes.
type Mount func(r chi.Router)

// NewRouter builds the HTTP handler with the standard middleware chain, the
// operational /metrics endpoint, uniform 404/405, and every module's routes.
//
// Middleware order is deliberate: correlate first (RequestID), then recover (so a
// panic still carries a request ID), then observe (log + metrics), then CORS, then a
// default write deadline (the server's WriteTimeout is 0 so streaming works), then
// the handler.
func NewRouter(d Deps, mounts ...Mount) http.Handler {
	r := chi.NewRouter()

	r.Use(mw.RequestID)
	r.Use(mw.Recover(d.Logger))
	r.Use(mw.Logging(d.Logger))
	r.Use(mw.Metrics(d.Metrics))
	r.Use(mw.CORS(d.Config.CORSAllowedOrigins))
	// Bounds ordinary responses now that the server sets no WriteTimeout; streaming
	// and long-poll handlers re-arm a longer rolling deadline before they write.
	r.Use(mw.WriteDeadline(d.Config.WriteTimeout))

	// Operational metrics. The old comment here said "restrict to the internal
	// network at the LB" — that restriction was never actually applied, so this
	// served the full Prometheus exposition to the open internet: every route
	// label (including all /v1/admin paths) plus coins_staked_total,
	// chargeback_debt_coins_total and fraud_flags_total. Enforced in the app
	// instead, where it cannot be forgotten by an LB config.
	r.Handle("/metrics", metricsHandler(d))

	// Each module mounts its own routes (health now; identity/match/wallet later).
	for _, m := range mounts {
		m(r)
	}

	// Uniform 404/405 in the error envelope.
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { Error(w, ErrNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		Error(w, NewError(http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed."))
	})

	return r
}
