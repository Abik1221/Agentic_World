// Package monopoly is the impure shell around the deterministic Monopoly engine
// (internal/engine/monopoly): match lifecycle, persistence, SSE spectator
// streaming, and the agent action API. It mirrors internal/mafia so the whole
// arena is built on one set of patterns.
package monopoly

import (
	"context"
	"log/slog"
	"net/http"
	"sync"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/httpx"
)

// ErrTooManyWatchers is returned when this instance's per-match watcher cap is
// reached; the load balancer routes additional spectators elsewhere.
var ErrTooManyWatchers = httpx.NewError(http.StatusServiceUnavailable, "watchers_full", "This match has too many watchers on this node; retry.")

// frame is one pre-encoded SSE event tagged with its sequence number so a
// resuming client can dedup against its backlog.
type frame struct {
	seq  int
	data []byte
}

type sub struct {
	ch   chan frame
	dead chan struct{}
	once sync.Once
}

func (s *sub) kill() { s.once.Do(func() { close(s.dead) }) }

// EventBacklog loads persisted events for SSE resume.
type EventBacklog interface {
	LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mono.Event, error)
	LiveMatches(ctx context.Context) ([]LiveMatch, error)
}

// Hub fans match events out to per-match subscriber sets. Safe for concurrent
// use. Fan-out is non-blocking and drop-slow, so a stalled watcher can never
// delay the match clock.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*sub]struct{}
	live map[string]struct{}

	eventLog    EventBacklog
	log         *slog.Logger
	bufSize     int
	maxPerMatch int
}

func NewHub(backlog EventBacklog, log *slog.Logger) *Hub {
	return &Hub{
		subs:        map[string]map[*sub]struct{}{},
		live:        map[string]struct{}{},
		eventLog:    backlog,
		log:         log,
		bufSize:     64,
		maxPerMatch: 1000,
	}
}

// RegisterMatch marks a DB-backed table as watchable over SSE.
func (h *Hub) RegisterMatch(matchID string) {
	h.mu.Lock()
	h.live[matchID] = struct{}{}
	h.mu.Unlock()
}

// Broadcast fans engine events to live spectators. Implements Broadcaster.
// Every Monopoly event is a public record, so nothing is redacted here.
func (h *Hub) Broadcast(matchID string, events []mono.Event) {
	if len(events) == 0 {
		return
	}
	h.RegisterMatch(matchID)
	frames := make([]frame, 0, len(events))
	for _, ev := range events {
		frames = append(frames, EncodeEvent(ev))
	}

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
				s.kill()
				return
			}
		}
	}
}

// Subscribe registers a watcher for a match.
func (h *Hub) Subscribe(matchID string) (*sub, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.live[matchID]; !ok {
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
	return s, nil
}

func (h *Hub) Unsubscribe(matchID string, s *sub) {
	h.mu.Lock()
	if set := h.subs[matchID]; set != nil {
		delete(set, s)
		if len(set) == 0 {
			delete(h.subs, matchID)
		}
	}
	h.mu.Unlock()
	s.kill()
}

// backlog returns encoded frames after lastSeq from the persisted event log.
func (h *Hub) backlog(ctx context.Context, matchID string, lastSeq int) []frame {
	if h.eventLog == nil {
		return nil
	}
	events, err := h.eventLog.LoadEvents(ctx, matchID, lastSeq)
	if err != nil {
		return nil
	}
	out := make([]frame, 0, len(events))
	for _, ev := range events {
		out = append(out, EncodeEvent(ev))
	}
	return out
}

func (h *Hub) watchers(matchID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[matchID])
}

// liveMatches decorates DB rows with this node's watcher counts.
func (h *Hub) liveMatches(rows []LiveMatch) []LiveMatch {
	for i := range rows {
		rows[i].Watchers = h.watchers(rows[i].MatchID)
		h.RegisterMatch(rows[i].MatchID)
	}
	return rows
}
