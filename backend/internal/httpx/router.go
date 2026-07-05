package httpx

import (
	"log/slog"
	"net/http"

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

// Mount is a route registrar a module provides, e.g. health.Handler.Register or
// (later) identity.Module.Register. It receives the root router and adds its routes.
type Mount func(r chi.Router)

// NewRouter builds the HTTP handler with the standard middleware chain, the
// operational /metrics endpoint, uniform 404/405, and every module's routes.
//
// Middleware order is deliberate: correlate first (RequestID), then recover (so a
// panic still carries a request ID), then observe (log + metrics), then CORS,
// then the handler.
func NewRouter(d Deps, mounts ...Mount) http.Handler {
	r := chi.NewRouter()

	r.Use(mw.RequestID)
	r.Use(mw.Recover(d.Logger))
	r.Use(mw.Logging(d.Logger))
	r.Use(mw.Metrics(d.Metrics))
	r.Use(mw.CORS(d.Config.CORSAllowedOrigins))

	// Operational metrics endpoint (restrict to the internal network at the LB).
	r.Handle("/metrics", d.Metrics.Handler())

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
