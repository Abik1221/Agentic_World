package replay

import (
	"context"
	"fmt"
	"sync"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
)

// MemStore is an in-memory EventStore for tests and local/dev. It enforces the
// same gap-free, monotonic seq invariant the DB-backed store (Stage 3) will.
type MemStore struct {
	mu     sync.Mutex
	events map[string][]gs.Event
}

// NewMemStore returns an empty in-memory event store.
func NewMemStore() *MemStore { return &MemStore{events: make(map[string][]gs.Event)} }

var _ EventStore = (*MemStore)(nil)

// Append validates that the incoming events continue the log contiguously
// (no gaps, no duplicates) before persisting them.
func (m *MemStore) Append(_ context.Context, matchID string, events []gs.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing := m.events[matchID]
	next := len(existing)
	for i, ev := range events {
		if ev.Seq != next+i {
			return fmt.Errorf("replay: non-contiguous seq for %s: got %d want %d", matchID, ev.Seq, next+i)
		}
	}
	m.events[matchID] = append(existing, events...)
	return nil
}

// Load returns a copy of the match's events in seq order.
func (m *MemStore) Load(_ context.Context, matchID string) ([]gs.Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]gs.Event(nil), m.events[matchID]...), nil
}
