// Package health serves liveness, readiness, and a versioned ping. It depends on
// a small Checker interface (satisfied by store.Store) rather than the concrete
// data layer, so it stays decoupled and trivially testable with a fake.
package health

import (
	"context"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// Checker is the set of dependency probes readiness needs.
type Checker interface {
	Ping(ctx context.Context) error
	MigrationsApplied(ctx context.Context) (bool, error)
}

// Handler exposes the health endpoints.
type Handler struct {
	checker Checker
	clock   platform.Clock
	started time.Time
	version string
}

// New builds a health handler. version is the build/version string surfaced by ping.
func New(checker Checker, clock platform.Clock, version string) *Handler {
	return &Handler{checker: checker, clock: clock, started: clock.Now(), version: version}
}

// Register attaches the health routes to the root router. It is an httpx.Mount,
// so the router never needs to import this package.
func (h *Handler) Register(r chi.Router) {
	r.Get("/healthz", h.Live)
	r.Get("/readyz", h.Ready)
	r.Route("/v1", func(r chi.Router) {
		r.Get("/ping", h.Ping)
	})
}

// Live is the liveness probe: cheap, no dependencies. If the process can answer,
// it is alive. The LB uses this to decide whether to restart the instance.
func (h *Handler) Live(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// Ready is the readiness probe: the instance only accepts traffic when its data
// dependencies are reachable AND migrations have been applied. This prevents an
// un-migrated or disconnected instance from serving requests.
func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()

	if err := h.checker.Ping(ctx); err != nil {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable", "reason": "dependencies", "detail": err.Error(),
		})
		return
	}
	applied, err := h.checker.MigrationsApplied(ctx)
	if err != nil {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable", "reason": "migrations_check", "detail": err.Error(),
		})
		return
	}
	if !applied {
		httpx.JSON(w, http.StatusServiceUnavailable, map[string]any{
			"status": "unavailable", "reason": "migrations_not_applied",
		})
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// Ping is a trivial authenticated-tier-free endpoint used for smoke tests and to
// confirm the version and uptime of the instance.
func (h *Handler) Ping(w http.ResponseWriter, _ *http.Request) {
	httpx.JSON(w, http.StatusOK, map[string]any{
		"message":    "pong",
		"version":    h.version,
		"uptime_sec": int(h.clock.Now().Sub(h.started).Seconds()),
	})
}
