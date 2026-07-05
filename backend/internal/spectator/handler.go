package spectator

import (
	"net/http"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the public spectator surface: the SSE watch stream, the live
// match list, and the stats ticker. Everything here is read-only and unauthenticated.
type Handler struct {
	hub       *Hub
	live      *Live
	heartbeat time.Duration
}

func NewHandler(hub *Hub, live *Live) *Handler {
	return &Handler{hub: hub, live: live, heartbeat: 25 * time.Second}
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/v1/match/{id}/watch", h.watch)
	r.Get("/v1/matches/live", h.matchesLive)
	r.Get("/v1/stats/live", h.statsLive)
}

// watch streams a match as Server-Sent Events. It first replays any history after
// the client's Last-Event-ID, then live rounds as they resolve, with heartbeats
// to keep proxies from closing an idle connection.
func (h *Handler) watch(w http.ResponseWriter, r *http.Request) {
	matchID := chi.URLParam(r, "id")
	s, err := h.hub.Subscribe(matchID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	defer h.hub.Unsubscribe(matchID, s)

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // ask proxies not to buffer the stream
	rc := http.NewResponseController(w)

	lastSeq := parseLastEventID(r)

	// Resume/backlog: everything after lastSeq (full history for a fresh viewer),
	// deduped against what the live stream may also deliver.
	if frames, err := h.hub.backlog(r.Context(), matchID, lastSeq); err == nil {
		for _, fr := range frames {
			if fr.seq <= lastSeq {
				continue
			}
			if _, err := w.Write(fr.data); err != nil {
				return
			}
			lastSeq = fr.seq
		}
		_ = rc.Flush()
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-s.dead:
			return
		case fr := <-s.ch:
			if fr.seq <= lastSeq {
				continue // already delivered via backlog
			}
			if _, err := w.Write(fr.data); err != nil {
				return
			}
			lastSeq = fr.seq
			_ = rc.Flush()
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			_ = rc.Flush()
		}
	}
}

func (h *Handler) matchesLive(w http.ResponseWriter, r *http.Request) {
	v, err := h.live.Matches(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=2")
	httpx.JSON(w, http.StatusOK, map[string]any{"matches": v})
}

func (h *Handler) statsLive(w http.ResponseWriter, r *http.Request) {
	v, err := h.live.Stats(r.Context())
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=5")
	httpx.JSON(w, http.StatusOK, v)
}

// parseLastEventID reads the SSE resume cursor from the standard header (or a
// query fallback). Absent/invalid ⇒ -1, which includes seq 0 (match_created).
func parseLastEventID(r *http.Request) int {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("last_event_id")
	}
	if raw == "" {
		return -1
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return -1
	}
	return n
}
