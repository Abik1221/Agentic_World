package autoplay

import (
	"sync"
	"time"
)

// SandboxThrottle bounds how many auto-play practice matches an agent has "in
// flight" without needing a completion callback from every game driver. It
// counts starts within a trailing window (matched to the driver's max-match
// cap); once a start ages out it no longer counts. Over-counting is safe — it
// only means the next practice match starts a little later — so this can never
// spawn a runaway of matches.
type SandboxThrottle struct {
	mu     sync.Mutex
	window time.Duration
	starts map[string][]time.Time
	now    func() time.Time // injectable for tests
}

func NewSandboxThrottle(window time.Duration) *SandboxThrottle {
	if window <= 0 {
		window = 5 * time.Minute
	}
	return &SandboxThrottle{window: window, starts: map[string][]time.Time{}, now: time.Now}
}

// Record marks that a practice match just started for the agent.
func (t *SandboxThrottle) Record(agentPublicID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.starts[agentPublicID] = append(t.prune(agentPublicID), t.now())
}

// ActiveCount returns how many of the agent's recent starts are still within the
// in-flight window.
func (t *SandboxThrottle) ActiveCount(agentPublicID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	live := t.prune(agentPublicID)
	t.starts[agentPublicID] = live
	return len(live)
}

// prune drops starts older than the window (caller holds the lock).
func (t *SandboxThrottle) prune(agentPublicID string) []time.Time {
	cutoff := t.now().Add(-t.window)
	kept := t.starts[agentPublicID][:0:0]
	for _, ts := range t.starts[agentPublicID] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	return kept
}
