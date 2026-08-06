package store

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/agentwire"
	"github.com/agent-arena/arena/internal/deadline"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WindowRepo supplies each agent's adaptive decision window from its own demonstrated
// latency. Implements match.WindowProvider.
//
// # Why it caches
//
// Window() is called while a round deadline is being set, which is on the path of every
// turn on the platform. A percentile query per turn would put a database round trip in
// front of every decision for a number that moves slowly — an agent's latency profile is
// a property of its model and its hosting, not of the current round.
//
// So the answer is cached per agent for a few minutes and refreshed in the background.
// A stale window is harmless: it is bounded by the policy floor and ceiling either way,
// and the liveness gate handles the case the window gets wrong.
//
// # Why it never blocks
//
// Every failure path returns 0, which the match service reads as "use the configured
// constant". A deadline provider that can hang would hang every turn on the platform, so
// it is built to be wrong-but-fast rather than right-but-slow.
type WindowRepo struct {
	db  *pgxpool.Pool
	log *slog.Logger

	mu    sync.RWMutex
	cache map[string]cachedWindow
	ttl   time.Duration
}

type cachedWindow struct {
	window time.Duration
	at     time.Time
}

func NewWindowRepo(db *pgxpool.Pool, log *slog.Logger) *WindowRepo {
	if log == nil {
		log = slog.Default()
	}
	return &WindowRepo{db: db, log: log, cache: map[string]cachedWindow{}, ttl: 5 * time.Minute}
}

// Window returns the decision budget for an agent in a game.
func (r *WindowRepo) Window(ctx context.Context, agentPublicID, game string) time.Duration {
	if r == nil || r.db == nil || agentPublicID == "" {
		return 0
	}
	key := agentPublicID + "|" + game

	r.mu.RLock()
	if c, ok := r.cache[key]; ok && time.Since(c.at) < r.ttl {
		r.mu.RUnlock()
		return c.window
	}
	r.mu.RUnlock()

	w := r.compute(ctx, agentPublicID, game)
	r.mu.Lock()
	r.cache[key] = cachedWindow{window: w, at: time.Now()}
	r.mu.Unlock()
	return w
}

// compute reads the agent's recent latencies and asks the policy for a window.
//
// Bounded to the last 200 decisions: an agent that upgraded its model last week should be
// judged on what it does now, not on a year of history it has moved past.
//
// Deliberately counts only decisions the agent actually ANSWERED. A timeout records the
// full window as its latency, so including them would ratchet the window upward every
// time it expired — a dead agent would earn itself an ever-longer deadline, which is the
// exact opposite of what the liveness gate is for.
func (r *WindowRepo) compute(ctx context.Context, agentPublicID, game string) time.Duration {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	rows, err := r.db.Query(ctx,
		`SELECT d.latency_ms
		   FROM agent_match_decisions d
		   JOIN agents a ON a.id = d.agent_id
		   JOIN matches m ON m.public_id = d.match_id
		  WHERE a.public_id = $1 AND m.game = $2
		    AND d.outcome = 'ok' AND d.latency_ms > 0
		  ORDER BY d.created_at DESC
		  LIMIT 200`, agentPublicID, game)
	if err != nil {
		// Never fatal, and never louder than debug: a deadline lookup failing is a
		// degraded window, not an incident, and this runs on every turn.
		r.log.Debug("window: latency lookup failed; using the configured window",
			"agent", agentPublicID, "error", err)
		return 0
	}
	defer rows.Close()

	samples := make([]int64, 0, 200)
	for rows.Next() {
		var ms int64
		if err := rows.Scan(&ms); err != nil {
			return 0
		}
		samples = append(samples, ms)
	}
	if rows.Err() != nil || len(samples) < deadline.MinSamples {
		return 0 // not enough evidence; the configured constant is the honest answer
	}
	return deadline.For(deadline.DefaultPolicy(game), samples)
}

// ── Liveness gate ────────────────────────────────────────────────────────────

// EndpointProber answers "is this agent's endpoint listening right now", for the
// deadline-extension gate. Implements match.LivenessProber.
//
// The probe is /health: unauthenticated, no game state, NO INFERENCE. That is what makes
// it safe to run at a deadline when retrying the turn itself is not — it costs the
// developer nothing.
type EndpointProber struct {
	Resolve func(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
	Client  agentwire.Prober
	Log     *slog.Logger
}

// Alive reports whether the endpoint answered.
//
// ANY doubt answers false. A false "alive" stalls a table by extending a deadline for an
// agent that will never reply; a false "gone" only forfeits a turn the agent was already
// failing to answer. The costs are not symmetric, so the tie goes to forfeiting.
func (p EndpointProber) Alive(ctx context.Context, agentPublicID string) bool {
	if p.Resolve == nil || p.Client == nil || agentPublicID == "" {
		return false
	}
	target, ok, err := p.Resolve(ctx, agentPublicID)
	if err != nil || !ok || target.EndpointURL == "" {
		// No hosted endpoint: a socket-connected agent's liveness is the gateway's
		// business, not this probe's, and it must not be granted an extension on the
		// strength of a check that never ran.
		return false
	}
	return agentwire.ConfirmReachability(ctx, p.Client, target, 3*time.Second, p.Log) == agentwire.ReachAlive
}
