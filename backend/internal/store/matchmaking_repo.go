package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/matchmaking"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MatchmakingRepo is the pgx implementation of matchmaking.Repo. One row per agent
// (PK agent_id); re-queueing upserts. The elo snapshot is written at enqueue so the
// matcher reads a single table.
type MatchmakingRepo struct{ db *pgxpool.Pool }

func NewMatchmakingRepo(db *pgxpool.Pool) *MatchmakingRepo { return &MatchmakingRepo{db: db} }

var _ matchmaking.Repo = (*MatchmakingRepo)(nil)

func (r *MatchmakingRepo) Upsert(ctx context.Context, e matchmaking.Entry) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO matchmaking_queue (agent_id, owner_user_id, bid, elo, status)
		 SELECT a.id, u.id, $3, $4, 'waiting'
		 FROM agents a, users u
		 WHERE a.public_id = $1 AND u.public_id = $2
		 ON CONFLICT (agent_id) DO UPDATE
		   SET bid = EXCLUDED.bid, elo = EXCLUDED.elo, status = 'waiting',
		       match_id = NULL, enqueued_at = now(), updated_at = now()`,
		e.AgentPublicID, e.OwnerPublicID, e.Bid, e.Elo)
	return err
}

func (r *MatchmakingRepo) Get(ctx context.Context, agentPublicID string) (matchmaking.Entry, error) {
	var e matchmaking.Entry
	var matchID *string
	err := r.db.QueryRow(ctx,
		`SELECT a.public_id, q.bid, q.elo, q.status, q.match_id, q.enqueued_at
		 FROM matchmaking_queue q
		 JOIN agents a ON a.id = q.agent_id
		 WHERE a.public_id = $1`, agentPublicID).
		Scan(&e.AgentPublicID, &e.Bid, &e.Elo, &e.Status, &matchID, &e.EnqueuedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return matchmaking.Entry{}, matchmaking.ErrNotQueued
	}
	if err != nil {
		return matchmaking.Entry{}, err
	}
	if matchID != nil {
		e.MatchID = *matchID
	}
	return e, nil
}

func (r *MatchmakingRepo) Delete(ctx context.Context, agentPublicID string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM matchmaking_queue
		 WHERE agent_id = (SELECT id FROM agents WHERE public_id = $1)`, agentPublicID)
	return err
}

func (r *MatchmakingRepo) Waiting(ctx context.Context, limit int) ([]matchmaking.Entry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, ow.public_id, q.bid, q.elo, q.enqueued_at
		 FROM matchmaking_queue q
		 JOIN agents a  ON a.id  = q.agent_id
		 JOIN users  ow ON ow.id = q.owner_user_id
		 WHERE q.status = 'waiting'
		 ORDER BY q.bid, q.enqueued_at
		 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []matchmaking.Entry
	for rows.Next() {
		var e matchmaking.Entry
		e.Status = matchmaking.StatusWaiting
		if err := rows.Scan(&e.AgentPublicID, &e.OwnerPublicID, &e.Bid, &e.Elo, &e.EnqueuedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ClaimPair reserves both entries (waiting -> claimed) all-or-nothing. It locks
// the two rows FOR UPDATE in public_id order (deadlock-safe) and only claims when
// BOTH are still waiting, so two concurrent matcher instances racing the same
// snapshot can never both escrow — the loser sees 'claimed' and returns false.
func (r *MatchmakingRepo) ClaimPair(ctx context.Context, agentA, agentB string) (bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)

	ids := []string{agentA, agentB}
	rows, err := tx.Query(ctx,
		`SELECT q.status
		 FROM matchmaking_queue q
		 JOIN agents a ON a.id = q.agent_id
		 WHERE a.public_id = ANY($1)
		 ORDER BY a.public_id
		 FOR UPDATE`, ids)
	if err != nil {
		return false, err
	}
	total, waiting := 0, 0
	for rows.Next() {
		var status string
		if err := rows.Scan(&status); err != nil {
			rows.Close()
			return false, err
		}
		total++
		if status == "waiting" {
			waiting++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return false, err
	}
	if total != 2 || waiting != 2 {
		return false, nil // one side already claimed/matched/dequeued
	}

	if _, err := tx.Exec(ctx,
		`UPDATE matchmaking_queue
		 SET status = 'claimed', updated_at = now()
		 WHERE agent_id IN (SELECT id FROM agents WHERE public_id = ANY($1))`,
		ids); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// ReleasePair returns a claimed pair to waiting (used when escrow fails).
func (r *MatchmakingRepo) ReleasePair(ctx context.Context, agentA, agentB string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE matchmaking_queue
		 SET status = 'waiting', updated_at = now()
		 WHERE agent_id IN (SELECT id FROM agents WHERE public_id = ANY($1))
		   AND status = 'claimed'`,
		[]string{agentA, agentB})
	return err
}

func (r *MatchmakingRepo) MarkMatched(ctx context.Context, agentA, agentB, matchPublicID string) error {
	// Only promotes a pair this matcher holds (status='claimed'), so it can never
	// flip a waiting/re-queued entry that another tick is about to pair.
	_, err := r.db.Exec(ctx,
		`UPDATE matchmaking_queue
		 SET status = 'matched', match_id = $2, updated_at = now()
		 WHERE agent_id IN (SELECT id FROM agents WHERE public_id = ANY($1))
		   AND status = 'claimed'`,
		[]string{agentA, agentB}, matchPublicID)
	return err
}
