package autoplay

import (
	"context"
	"sync"
)

// MemRepo is an in-memory Repo — used in tests and as a runnable default until a
// Postgres-backed repo is wired. Settings do not survive a restart, which is fine
// while auto-play is opt-in and default-off.
type MemRepo struct {
	mu sync.RWMutex
	m  map[string]Setting
}

func NewMemRepo() *MemRepo { return &MemRepo{m: map[string]Setting{}} }

func (r *MemRepo) ListEnabled(context.Context) ([]Setting, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Setting, 0, len(r.m))
	for _, s := range r.m {
		if s.Enabled {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *MemRepo) ListByOwner(_ context.Context, ownerPublicID string) ([]Setting, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Setting, 0)
	for _, s := range r.m {
		if s.OwnerPublicID == ownerPublicID {
			out = append(out, s)
		}
	}
	return out, nil
}

func (r *MemRepo) Get(_ context.Context, agentPublicID string) (Setting, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.m[agentPublicID]
	return s, ok, nil
}

func (r *MemRepo) Set(_ context.Context, s Setting) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.m[s.AgentPublicID] = s
	return nil
}

func (r *MemRepo) SetStatus(_ context.Context, agentPublicID, status, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.m[agentPublicID]
	if !ok {
		return nil
	}
	s.LastStatus = status
	s.LastStatusReason = reason
	r.m[agentPublicID] = s
	return nil
}
