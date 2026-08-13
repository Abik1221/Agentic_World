package store

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// QueueEventsRepo records what happened to a queued agent, and answers the questions the
// live queue tables cannot.
//
// matchmaking_queue and group_queue hold one row per agent and delete it when a match
// finalizes. So "how long did agents wait who never got a game" has no answer there: the
// evidence is gone at precisely the moment it becomes interesting. This is the append-only
// history that keeps it.
//
// # Recording must never break matchmaking
//
// Every writer here is BEST EFFORT and returns no error to its caller. Telemetry that can
// fail a pairing is worse than no telemetry: an agent losing its place in the queue because
// an observability insert hit a constraint would be a real outage caused by a reporting
// feature. Failures are logged at debug and dropped.
//
// Nothing reads this table to make a decision. That is deliberate and worth keeping — it is
// what makes the table safe to add, safe to drop, and safe to write to on a hot path.
type QueueEventsRepo struct {
	db  *pgxpool.Pool
	log *slog.Logger
}

func NewQueueEventsRepo(db *pgxpool.Pool, log *slog.Logger) *QueueEventsRepo {
	if log == nil {
		log = slog.Default()
	}
	return &QueueEventsRepo{db: db, log: log}
}

// Queue kinds. Short strings, matched by the table's CHECK constraint.
const (
	QueueTwoPlayer = "2p"
	QueueGroup     = "group"
)

// Event kinds, mirroring queue_events_kind_chk. Constants rather than literals at the call
// sites: a typo in a string would pass the compiler and then fail the CHECK at runtime, on a
// best-effort path that swallows the error — so the event would simply never be recorded and
// the funnel would quietly stop adding up.
const (
	QueueEnqueued   = "enqueued"
	QueueMatched    = "matched"
	QueueReadyAsked = "ready_asked"
	QueueReadyOK    = "ready_ok"
	QueueDropped    = "dropped"
	QueueRequeued   = "requeued"
	QueueLeft       = "left"
)

// QueueEvent is one thing that happened to one queued agent.
type QueueEvent struct {
	AgentPublicID string
	Game          string
	Queue         string // QueueTwoPlayer | QueueGroup
	Kind          string
	Bid           int64
	MatchPublicID string
	Reason        string
	// Waited is how long this agent had been in the queue when the event happened. Zero or
	// negative is stored as NULL rather than 0: "not measured" and "waited no time at all"
	// are different facts, and averaging a pile of placeholder zeros would understate every
	// wait statistic on the board.
	Waited time.Duration
}

// Record appends one event. Never returns an error — see the type comment.
func (r *QueueEventsRepo) Record(ctx context.Context, e QueueEvent) {
	if e.AgentPublicID == "" || e.Kind == "" {
		return // nothing identifiable to record; not worth a log line on a hot path
	}
	var waited *int64
	if e.Waited > 0 {
		ms := e.Waited.Milliseconds()
		waited = &ms
	}
	var match *string
	if e.MatchPublicID != "" {
		match = &e.MatchPublicID
	}
	queue := e.Queue
	if queue != QueueGroup {
		queue = QueueTwoPlayer // the CHECK only accepts the two; default rather than fail
	}
	game := e.Game
	if game == "" {
		game = "goofspiel" // the only game on the 2p queue; better than violating NOT NULL
	}

	// owner_user_id comes from the agent row rather than the caller: the caller usually has a
	// public id and not the internal one, and looking it up here keeps every call site short
	// enough that nobody is tempted to skip the field. LEFT JOIN so a missing user records the
	// event with a null owner instead of dropping it — the event matters more than the
	// attribution.
	//
	// context.WithoutCancel: this is called from paths that are finishing up (a match
	// finalizing, a seat being dropped), and their context is often already cancelled by the
	// time the write happens. Without this the insert fails on cancellation and the most
	// interesting events — the ones at the end of something — would be the ones that never
	// get recorded.
	_, err := r.db.Exec(context.WithoutCancel(ctx),
		`INSERT INTO queue_events
		     (agent_id, owner_user_id, game, queue, bid, kind, match_public_id, reason, waited_ms)
		 SELECT a.id, u.id, $2, $3, $4, $5, $6, $7, $8
		   FROM agents a
		   LEFT JOIN users u ON u.id = a.owner_user_id
		  WHERE a.public_id = $1`,
		e.AgentPublicID, game, queue, e.Bid, e.Kind, match, e.Reason, waited)
	if err != nil {
		// Debug, not warn: this runs on every enqueue and every pairing, and a reporting
		// insert that fails is a gap in a chart, not an incident. Louder would train people
		// to ignore the log.
		r.log.Debug("queue events: could not record",
			"agent", e.AgentPublicID, "kind", e.Kind, "error", err)
	}
}

// ── reporting ────────────────────────────────────────────────────────────────

// QueueFunnel is where agents got to, over a window.
type QueueFunnel struct {
	Enqueued   int64 `json:"enqueued"`
	ReadyAsked int64 `json:"ready_asked"`
	ReadyOK    int64 `json:"ready_ok"`
	Matched    int64 `json:"matched"`
	Dropped    int64 `json:"dropped"`
	Requeued   int64 `json:"requeued"`
	Left       int64 `json:"left"`
	// NeverMatched counts agents that enqueued in the window and have no matched event
	// AFTER their last enqueue. The interesting number, and the one the live queue cannot
	// produce at all.
	NeverMatched int64 `json:"never_matched"`
	// WaitP50Ms/WaitP95Ms are over MATCHED events only — the wait of agents that actually
	// got a game. Mixing in unmatched entries would report a wait for a thing that never
	// happened.
	WaitP50Ms int64 `json:"wait_p50_ms"`
	WaitP95Ms int64 `json:"wait_p95_ms"`
	// LongestWaitingMs is the oldest CURRENTLY-waiting entry, taken from the live queue —
	// an agent still sitting there has no event to measure yet, and it is exactly the case
	// someone is complaining about.
	LongestWaitingMs int64 `json:"longest_waiting_ms"`
}

// Funnel aggregates the window ending now.
func (r *QueueEventsRepo) Funnel(ctx context.Context, since time.Duration) (QueueFunnel, error) {
	var f QueueFunnel
	cutoff := time.Now().Add(-since)

	err := r.db.QueryRow(ctx,
		`SELECT
		    count(*) FILTER (WHERE kind = 'enqueued'),
		    count(*) FILTER (WHERE kind = 'ready_asked'),
		    count(*) FILTER (WHERE kind = 'ready_ok'),
		    count(*) FILTER (WHERE kind = 'matched'),
		    count(*) FILTER (WHERE kind = 'dropped'),
		    count(*) FILTER (WHERE kind = 'requeued'),
		    count(*) FILTER (WHERE kind = 'left'),
		    -- percentiles over matched waits only, and only where a wait was measured
		    COALESCE(percentile_disc(0.5) WITHIN GROUP (
		        ORDER BY waited_ms) FILTER (WHERE kind = 'matched' AND waited_ms IS NOT NULL), 0),
		    COALESCE(percentile_disc(0.95) WITHIN GROUP (
		        ORDER BY waited_ms) FILTER (WHERE kind = 'matched' AND waited_ms IS NOT NULL), 0)
		 FROM queue_events WHERE created_at >= $1`, cutoff).
		Scan(&f.Enqueued, &f.ReadyAsked, &f.ReadyOK, &f.Matched,
			&f.Dropped, &f.Requeued, &f.Left, &f.WaitP50Ms, &f.WaitP95Ms)
	if err != nil {
		return QueueFunnel{}, err
	}

	// Never matched: enqueued in the window with no matched event after that enqueue.
	//
	// "after that enqueue", not "anywhere in the window" — a requeued agent that eventually
	// paired has both an unmatched early enqueue and a later match, and counting it as never
	// matched would inflate the number with agents that did fine in the end.
	if err := r.db.QueryRow(ctx,
		`WITH last_enqueue AS (
		     SELECT agent_id, max(created_at) AS at
		       FROM queue_events
		      WHERE kind = 'enqueued' AND created_at >= $1
		      GROUP BY agent_id
		 )
		 SELECT count(*) FROM last_enqueue le
		  WHERE NOT EXISTS (
		     SELECT 1 FROM queue_events m
		      WHERE m.agent_id = le.agent_id AND m.kind = 'matched' AND m.created_at >= le.at
		  )`, cutoff).Scan(&f.NeverMatched); err != nil {
		return QueueFunnel{}, err
	}

	// The oldest live wait, across both queues. Best effort: a failure here should not lose
	// the rest of the funnel, which is already computed.
	var longest *int64
	if err := r.db.QueryRow(ctx,
		`SELECT GREATEST(
		     COALESCE((SELECT max(EXTRACT(EPOCH FROM (now() - enqueued_at)) * 1000)::bigint
		                 FROM matchmaking_queue WHERE status = 'waiting'), 0),
		     COALESCE((SELECT max(EXTRACT(EPOCH FROM (now() - enqueued_at)) * 1000)::bigint
		                 FROM group_queue WHERE status = 'waiting'), 0))`).Scan(&longest); err == nil && longest != nil {
		f.LongestWaitingMs = *longest
	}
	return f, nil
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

// ByOwner is the per-developer breakdown — "every detail for this user", which is what makes
// the funnel actionable rather than merely interesting: a platform-wide drop rate says
// something is wrong, and this says who it is happening to.
func (r *QueueEventsRepo) ByOwner(ctx context.Context, since time.Duration, limit int) ([]QueueOwnerRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cutoff := time.Now().Add(-since)
	rows, err := r.db.Query(ctx,
		`WITH ev AS (
		     SELECT * FROM queue_events WHERE created_at >= $1 AND owner_user_id IS NOT NULL
		 ), last_enqueue AS (
		     SELECT agent_id, owner_user_id, max(created_at) AS at
		       FROM ev WHERE kind = 'enqueued' GROUP BY agent_id, owner_user_id
		 ), never AS (
		     SELECT le.owner_user_id, count(*) AS n
		       FROM last_enqueue le
		      WHERE NOT EXISTS (
		          SELECT 1 FROM ev m
		           WHERE m.agent_id = le.agent_id AND m.kind = 'matched' AND m.created_at >= le.at)
		      GROUP BY le.owner_user_id
		 )
		 SELECT u.public_id, COALESCE(NULLIF(u.display_name, ''), NULLIF(u.username, ''), NULLIF(u.x_handle, ''), ''),
		        count(*) FILTER (WHERE ev.kind = 'enqueued'),
		        count(*) FILTER (WHERE ev.kind = 'matched'),
		        count(*) FILTER (WHERE ev.kind = 'dropped'),
		        COALESCE(max(n.n), 0),
		        COALESCE(max(ev.waited_ms), 0)
		   FROM ev
		   JOIN users u ON u.id = ev.owner_user_id
		   LEFT JOIN never n ON n.owner_user_id = ev.owner_user_id
		  GROUP BY u.public_id, u.display_name, u.username, u.x_handle
		  ORDER BY COALESCE(max(n.n), 0) DESC, count(*) FILTER (WHERE ev.kind = 'dropped') DESC
		  LIMIT $2`, cutoff, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []QueueOwnerRow
	for rows.Next() {
		var q QueueOwnerRow
		if err := rows.Scan(&q.OwnerPublicID, &q.OwnerName, &q.Enqueued, &q.Matched,
			&q.Dropped, &q.NeverMatched, &q.WorstWaitMs); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// ── match-service adapter ────────────────────────────────────────────────────

// QueueEventAdapter satisfies match.QueueEventRecorder over this repository.
//
// A thin adapter rather than making QueueEventsRepo implement the interface directly: the
// match package declares what it needs and the store supplies it, so the dependency points
// from storage to domain and not the other way. It also keeps the repo's own API in terms of
// a QueueEvent struct, which the funnel queries and the tests read, instead of three
// near-identical methods.
type QueueEventAdapter struct{ Repo *QueueEventsRepo }

func (a QueueEventAdapter) ReadyAsked(ctx context.Context, agentPublicID, matchPublicID string) {
	a.record(ctx, agentPublicID, matchPublicID, QueueReadyAsked, "")
}

func (a QueueEventAdapter) ReadyOK(ctx context.Context, agentPublicID, matchPublicID string) {
	a.record(ctx, agentPublicID, matchPublicID, QueueReadyOK, "")
}

func (a QueueEventAdapter) Dropped(ctx context.Context, agentPublicID, matchPublicID, reason string) {
	a.record(ctx, agentPublicID, matchPublicID, QueueDropped, reason)
}

func (a QueueEventAdapter) record(ctx context.Context, agent, match, kind, reason string) {
	if a.Repo == nil {
		return // an unwired adapter is silent, never a nil dereference on a staked path
	}
	a.Repo.Record(ctx, QueueEvent{
		AgentPublicID: agent, MatchPublicID: match, Kind: kind, Reason: reason,
		// Game and queue are left at their defaults: a ready check only runs on a paired
		// table, and Record fills them rather than letting a NOT NULL violation drop the row.
	})
}
