// Package arena serves a small PUBLIC discovery endpoint listing the arenas
// (games) the platform offers, so clients and the pyyol SDK (`pyyol arenas`) have a
// single source of truth instead of hardcoding the game list. The set is static
// today (the engines are compiled in); it lives here rather than being duplicated
// across the SDK/UI. `sandbox` is always available (no certification); `ranked`
// reflects whether an arena has a live ranked matchmaking queue.
package arena

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Arena describes one playable arena for discovery clients.
type Arena struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	MinPlayers int    `json:"min_players"`
	MaxPlayers int    `json:"max_players"`
	Sandbox    bool   `json:"sandbox"` // practice available without certification
	Ranked     bool   `json:"ranked"`  // a live ranked (real-stakes) queue exists
	Status     string `json:"status"`  // "available" | "beta" | "coming_soon"
}

// Arenas is the current catalog. Player counts mirror the engines:
// goofspiel = 1v1, mafia = fixed 12-seat, monopoly = 2–8. Ranked matchmaking is
// Goofspiel-only today (mafia/monopoly are sandbox/lobby until their ranked queues land).
var Arenas = []Arena{
	{ID: "goofspiel", Name: "Goofspiel", MinPlayers: 2, MaxPlayers: 2, Sandbox: true, Ranked: true, Status: "available"},
	{ID: "mafia", Name: "Mafia", MinPlayers: 12, MaxPlayers: 12, Sandbox: true, Ranked: false, Status: "beta"},
	{ID: "monopoly", Name: "Monopoly", MinPlayers: 2, MaxPlayers: 8, Sandbox: true, Ranked: false, Status: "beta"},
}

// Handler serves GET /v1/arenas (public).
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/arenas", h.list)
}

func (h *Handler) list(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=60")
	httpx.JSON(w, http.StatusOK, map[string]any{"arenas": Arenas})
}
