package adminapi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
)

// QueueHealthReader answers "where do agents get stuck in matchmaking".
//
// Satisfied by *store.QueueEventsRepo. Kept as an interface for the same reason
// TreasuryReader is: adminapi should not depend on the concrete repository, and an
// unconfigured deployment must be able to leave it nil.
//
// OPTIONAL. Nil ⇒ these routes answer 503 rather than an empty funnel. An empty funnel and
// "reporting is not wired here" look identical on a dashboard, and the first one would be
// read as "the queue is healthy" — the exact opposite of what a missing recorder means.
// QueueFunnel and QueueOwnerRow are declared HERE, not imported from the store.
//
// internal/store already imports this package (admin_repo.go), so importing it back would be
// a cycle. Declaring the wire shapes on the API side and letting the store adapt into them
// also keeps the dashboard's contract from being whatever the repository happens to return.
type QueueFunnel struct {
	Enqueued         int64 `json:"enqueued"`
	ReadyAsked       int64 `json:"ready_asked"`
	ReadyOK          int64 `json:"ready_ok"`
	Matched          int64 `json:"matched"`
	Dropped          int64 `json:"dropped"`
	Requeued         int64 `json:"requeued"`
	Left             int64 `json:"left"`
	NeverMatched     int64 `json:"never_matched"`
	WaitP50Ms        int64 `json:"wait_p50_ms"`
	WaitP95Ms        int64 `json:"wait_p95_ms"`
	LongestWaitingMs int64 `json:"longest_waiting_ms"`
}

// QueueOwnerRow is one developer's queue experience over the window.
type QueueOwnerRow struct {
	OwnerPublicID string `json:"owner_public_id"`
	OwnerName     string `json:"owner_name"`
	Enqueued      int64  `json:"enqueued"`
	Matched       int64  `json:"matched"`
	Dropped       int64  `json:"dropped"`
	NeverMatched  int64  `json:"never_matched"`
	WorstWaitMs   int64  `json:"worst_wait_ms"`
}

type QueueHealthReader interface {
	Funnel(ctx context.Context, since time.Duration) (QueueFunnel, error)
	ByOwner(ctx context.Context, since time.Duration, limit int) ([]QueueOwnerRow, error)
}

// SetQueueHealth wires the reader. Nil leaves the routes answering 503.
func (h *Handler) SetQueueHealth(q QueueHealthReader) { h.queueHealth = q }

// queueHealth serves the funnel: how many agents queued, how many were asked to confirm,
// how many answered, how many paired, how many were dropped as unreachable, and how many
// never got a game at all.
//
// The last number is the point. Everything else is visible in some form already; "enqueued
// and never matched" has no artefact anywhere else in the schema, because the queue row is
// deleted when a match finalizes and there is no row at all for an agent that never paired.
func (h *Handler) queueFunnel(w http.ResponseWriter, r *http.Request) {
	if h.queueHealth == nil {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "queue_health_unavailable",
			"Queue funnel reporting is not wired in this deployment."))
		return
	}
	since := windowParam(r)
	f, err := h.queueHealth.Funnel(r.Context(), since)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// no-store: an operator deciding whether matchmaking is broken must not be shown a
	// cached funnel from before the incident started.
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"window_seconds": int64(since.Seconds()),
		"funnel":         f,
	})
}

// queueHealthByOwner is the per-developer breakdown — which is what makes the funnel
// actionable. A platform-wide drop rate says something is wrong; this says who it is
// happening to, which is the difference between a number and a support reply.
func (h *Handler) queueHealthByOwner(w http.ResponseWriter, r *http.Request) {
	if h.queueHealth == nil {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "queue_health_unavailable",
			"Queue funnel reporting is not wired in this deployment."))
		return
	}
	since := windowParam(r)
	limit, _ := page(r)
	rows, err := h.queueHealth.ByOwner(r.Context(), since, limit)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.JSON(w, http.StatusOK, map[string]any{
		"window_seconds": int64(since.Seconds()),
		"owners":         rows,
	})
}

// windowParam reads ?window_seconds=, defaulting to an hour and clamped to a week.
//
// Clamped at both ends deliberately. Zero or negative would return an empty funnel that
// reads as a healthy queue, and an unbounded window would let one dashboard request scan
// the whole history — a reporting route must not be able to become the platform's slowest
// query.
func windowParam(r *http.Request) time.Duration {
	const def = time.Hour
	const max = 7 * 24 * time.Hour
	raw := r.URL.Query().Get("window_seconds")
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		return def
	}
	d := time.Duration(n) * time.Second
	if d > max {
		return max
	}
	return d
}
