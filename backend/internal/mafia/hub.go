// Package mafia streams a social-deduction match to spectators over SSE. It is a
// second game alongside Goofspiel, played entirely by AI agents while humans watch.
//
// Every frame relayed here comes from a real engine match. An earlier version seeded
// an always-on scripted table so the arena never looked empty, and served it from the
// PUBLIC live list as if it were a live game — that is gone. An empty arena now
// reports itself honestly.
//
// Fan-out mirrors the spectator Hub: non-blocking and drop-slow, so a stalled
// watcher can never delay the match clock.
package mafia

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/prometheus/client_golang/prometheus"
)

// ErrTooManyWatchers is returned when this instance's per-match watcher cap is
// reached; the load balancer routes additional spectators to other instances.
var ErrTooManyWatchers = httpx.NewError(http.StatusServiceUnavailable, "watchers_full", "This match has too many watchers on this node; retry.")

// frame is one pre-encoded SSE event tagged with its sequence number so a
// resuming client can dedup against its backlog.
//
// seq == unsequencedSeq marks a PRESENCE frame (who is thinking): live-only, never
// persisted, never part of the replay. Such frames carry no `id:` line, so they do
// not move the client's Last-Event-ID and cannot break resume — a reconnect picks up
// from the last real game event, and fresh presence arrives on the next change.
type frame struct {
	seq  int
	data []byte
}

// unsequencedSeq tags a live-only frame that must bypass sequence dedup entirely.
const unsequencedSeq = -1

// sub is a single watcher's buffered mailbox. Closing dead signals the handler
// goroutine to disconnect (used when the buffer overflows = slow consumer).
type sub struct {
	ch   chan frame
	dead chan struct{}
	once sync.Once
}

func (s *sub) kill() { s.once.Do(func() { close(s.dead) }) }

// EventBacklog loads persisted events for SSE resume on real matches.
type EventBacklog interface {
	LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mf.Event, error)
}

// Hub fans match events out to per-match subscriber sets. Safe for concurrent use.
type Hub struct {
	mu          sync.RWMutex
	subs        map[string]map[*sub]struct{}
	realMatches map[string]struct{}

	eventLog    EventBacklog
	log         *slog.Logger
	m           *metrics
	bufSize     int
	maxPerMatch int
}

// NewHub builds the broadcast hub.
func NewHub(backlog EventBacklog, log *slog.Logger, reg *prometheus.Registry) *Hub {
	h := &Hub{
		subs:        map[string]map[*sub]struct{}{},
		realMatches: map[string]struct{}{},
		eventLog:    backlog,
		log:         log,
		m:           newMetrics(reg),
		bufSize:     64,
		maxPerMatch: 1000,
	}
	return h
}

// RegisterMatch marks a DB-backed table as watchable over SSE.
func (h *Hub) RegisterMatch(matchID string) {
	h.mu.Lock()
	h.realMatches[matchID] = struct{}{}
	h.mu.Unlock()
}

// Broadcast fans engine events to live spectators. Implements mafia.Broadcaster.
//
// Live spectators only ever receive the redacted (public) view: night secrets —
// kill targets, investigation findings, doctor/sheriff actions and the acting
// seats' roles — are stripped here so hidden information never leaves the server
// mid-match. The full log is still persisted for the post-match replay.
// BroadcastPending pushes the "thinking…" set to watchers of a match.
//
// This is ephemeral presence, deliberately kept OUT of the authoritative log: it is
// derived from state (see Service.baseView / mf.PendingActors), so it can always be
// recomputed and never needs to be replayed. Keeping it out of match_events also
// keeps the log churn-free and leaves Goofspiel's fairness proof untouched.
//
// Dropped for slow consumers like any other frame — a lagging watcher missing a
// typing indicator is harmless, and the next change resyncs it.
func (h *Hub) BroadcastPending(matchID string, seats []int) {
	if matchID == "" {
		return
	}
	body, err := json.Marshal(map[string]any{"pending": seats})
	if err != nil {
		return
	}
	// No `id:` line — this must not advance Last-Event-ID.
	fr := frame{
		seq:  unsequencedSeq,
		data: []byte(fmt.Sprintf("event: pending\ndata: %s\n\n", body)),
	}

	h.mu.RLock()
	targets := make([]*sub, 0, len(h.subs[matchID]))
	for s := range h.subs[matchID] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		select {
		case s.ch <- fr:
		case <-s.dead:
		default:
			// Presence is disposable: drop it rather than killing the watcher.
			h.m.droppedSlow.Inc()
		}
	}
}

func (h *Hub) Broadcast(matchID string, events []mf.Event) {
	if len(events) == 0 {
		return
	}
	h.RegisterMatch(matchID)
	public := mf.RedactLog(events)
	if len(public) == 0 {
		return
	}
	frames := make([]frame, 0, len(public))
	for _, ev := range public {
		frames = append(frames, EncodeEvent(ev))
	}
	h.m.broadcastEvents.Add(float64(len(frames)))

	h.mu.RLock()
	targets := make([]*sub, 0, len(h.subs[matchID]))
	for s := range h.subs[matchID] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		for _, fr := range frames {
			select {
			case s.ch <- fr:
			case <-s.dead:
				return
			default:
				h.m.droppedSlow.Inc()
				s.kill()
				return
			}
		}
	}
}

// Run blocks until the context is cancelled.
//
// It used to start scripted "director" goroutines that replayed a hand-written
// match on a loop so the arena always looked busy. That table was served from the
// PUBLIC live list as though it were a real game, so it is gone; the hub now only
// ever relays genuine engine events. Kept as a no-op lifecycle hook so the
// composition root's launch() wiring is unchanged.
func (h *Hub) Run(ctx context.Context) {
	<-ctx.Done()
}

// broadcast pushes one frame to every current watcher of matchID. It NEVER
// blocks: a subscriber whose buffer is full is dropped (drop-slow).
func (h *Hub) broadcast(matchID string, fr frame) {
	h.m.broadcastEvents.Inc()
	h.mu.RLock()
	targets := make([]*sub, 0, len(h.subs[matchID]))
	for s := range h.subs[matchID] {
		targets = append(targets, s)
	}
	h.mu.RUnlock()

	for _, s := range targets {
		select {
		case s.ch <- fr:
		case <-s.dead:
		default:
			h.m.droppedSlow.Inc()
			s.kill()
		}
	}
}

// Subscribe registers a watcher for a match. ErrNotFound for an unknown match,
// ErrTooManyWatchers when this instance's per-match cap is hit.
func (h *Hub) Subscribe(matchID string) (*sub, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.realMatches[matchID]; !ok {
		return nil, httpx.ErrNotFound
	}
	set := h.subs[matchID]
	if set == nil {
		set = map[*sub]struct{}{}
		h.subs[matchID] = set
	}
	if len(set) >= h.maxPerMatch {
		return nil, ErrTooManyWatchers
	}
	s := &sub{ch: make(chan frame, h.bufSize), dead: make(chan struct{})}
	set[s] = struct{}{}
	h.m.subscribers.Inc()
	return s, nil
}

// Unsubscribe removes a watcher and releases it.
func (h *Hub) Unsubscribe(matchID string, s *sub) {
	h.mu.Lock()
	if set := h.subs[matchID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.subs, matchID)
		}
	}
	h.mu.Unlock()
	h.m.subscribers.Dec()
	s.kill()
}

// backlog returns frames after lastSeq from the persisted event log.
func (h *Hub) backlog(ctx context.Context, matchID string, lastSeq int) []frame {
	if h.eventLog == nil {
		return nil
	}
	events, err := h.eventLog.LoadEvents(ctx, matchID, lastSeq)
	if err != nil {
		return nil
	}
	// A resuming watcher is a live spectator: redact night secrets exactly like
	// the live broadcast. Full detail is only available post-match via /replay.
	public := mf.RedactLog(events)
	out := make([]frame, 0, len(public))
	for _, ev := range public {
		out = append(out, EncodeEvent(ev))
	}
	return out
}

func (h *Hub) watchers(matchID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[matchID])
}

// liveMatches returns the real, DB-backed live tables supplied by the caller.
//
// It previously prepended scripted demo tables, which is why a signed-out visitor
// always saw a "live" Mafia match that did not exist and why the Live Now counter
// was never zero. Only genuine matches are listed now; an empty arena reports
// itself honestly.
func (h *Hub) liveMatches(extra []LiveMatch) []LiveMatch {
	out := make([]LiveMatch, 0, len(extra))
	for i := range extra {
		extra[i].Watchers = h.watchers(extra[i].MatchID)
		out = append(out, extra[i])
	}
	return out
}

// LiveMatch is one row of the public Mafia arena list.
type LiveMatch struct {
	MatchID  string   `json:"match_id"`
	Title    string   `json:"title"`
	Agents   []string `json:"agents"`
	Players  int      `json:"players"`
	Alive    int      `json:"alive"`
	Day      int      `json:"day"`
	Phase    string   `json:"phase"`
	Winner   string   `json:"winner,omitempty"`
	Watchers int      `json:"watchers"`
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	subscribers     prometheus.Gauge
	droppedSlow     prometheus.Counter
	broadcastEvents prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		subscribers: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "mafia_sse_subscribers", Help: "Current Mafia SSE spectator connections on this instance.",
		}),
		droppedSlow: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mafia_sse_dropped_slow_total", Help: "Mafia spectators dropped for being too slow to read.",
		}),
		broadcastEvents: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "mafia_sse_broadcast_events_total", Help: "Mafia events fanned out to spectators.",
		}),
	}
	reg.MustRegister(m.subscribers, m.droppedSlow, m.broadcastEvents)
	return m
}
