package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/groupmatch"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// GroupQueueRepo is the pgx implementation of groupmatch.Repo (N-player queue for
// Mafia/Monopoly). One row per agent (PK agent_id); re-queueing upserts. Mirrors
// MatchmakingRepo but carries a game column and claims a whole GROUP atomically.
type GroupQueueRepo struct{ db *pgxpool.Pool }

func NewGroupQueueRepo(db *pgxpool.Pool) *GroupQueueRepo { return &GroupQueueRepo{db: db} }

var _ groupmatch.Repo = (*GroupQueueRepo)(nil)

func (r *GroupQueueRepo) Upsert(ctx context.Context, e groupmatch.Entry) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO group_queue (agent_id, owner_user_id, game, bid, elo, status)
		 SELECT a.id, u.id, $3, $4, $5, 'waiting'
		 FROM agents a, users u
		 WHERE a.public_id = $1 AND u.public_id = $2
		 ON CONFLICT (agent_id) DO UPDATE
		   SET game = EXCLUDED.game, bid = EXCLUDED.bid, elo = EXCLUDED.elo, status = 'waiting',
		       match_id = NULL, enqueued_at = now(), updated_at = now()`,
		e.AgentPublicID, e.OwnerPublicID, e.Game, e.Bid, e.Elo)
	return err
}

func (r *GroupQueueRepo) Get(ctx context.Context, agentPublicID string) (groupmatch.Entry, error) {
	var e groupmatch.Entry
	var matchID *string
	err := r.db.QueryRow(ctx,
		`SELECT a.public_id, q.game, q.bid, q.elo, q.status, q.match_id, q.enqueued_at
		 FROM group_queue q
		 JOIN agents a ON a.id = q.agent_id
		 WHERE a.public_id = $1`, agentPublicID).
		Scan(&e.AgentPublicID, &e.Game, &e.Bid, &e.Elo, &e.Status, &matchID, &e.EnqueuedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return groupmatch.Entry{}, groupmatch.ErrNotQueued
	}
	if err != nil {
		return groupmatch.Entry{}, err
	}
	if matchID != nil {
		e.MatchID = *matchID
	}
	return e, nil
}

func (r *GroupQueueRepo) Delete(ctx context.Context, agentPublicID string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM group_queue
		 WHERE agent_id = (SELECT id FROM agents WHERE public_id = $1)`, agentPublicID)
	return err
}

func (r *GroupQueueRepo) WaitingByGame(ctx context.Context, game string, limit int) ([]groupmatch.Entry, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, ow.public_id, q.game, q.bid, q.elo, q.enqueued_at
		 FROM group_queue q
		 JOIN agents a  ON a.id  = q.agent_id
		 JOIN users  ow ON ow.id = q.owner_user_id
		 WHERE q.status = 'waiting' AND q.game = $1
		 ORDER BY q.bid, q.enqueued_at
		 LIMIT $2`, game, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []groupmatch.Entry
	for rows.Next() {
		var e groupmatch.Entry
		e.Status = groupmatch.StatusWaiting
		if err := rows.Scan(&e.AgentPublicID, &e.OwnerPublicID, &e.Game, &e.Bid, &e.Elo, &e.EnqueuedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PoolStats reports one (game, bid) waiting pool: its size, how many distinct owners
// it represents, and where the given agent sits in it by enqueue time.
//
// Position uses a row comparison on (enqueued_at, agent_id) rather than enqueued_at
// alone, so agents that enqueued in the same instant get stable, distinct positions
// instead of all reporting the same one. An agent that is not waiting (already claimed,
// matched, or gone) yields position 0: the subquery is NULL, the comparison is NULL, no
// rows match, and COUNT(*) is 0.
func (r *GroupQueueRepo) PoolStats(ctx context.Context, game string, bid int64, agentPublicID string) (groupmatch.PoolStats, error) {
	var ps groupmatch.PoolStats
	err := r.db.QueryRow(ctx,
		`WITH pool AS (
		     SELECT q.agent_id, q.owner_user_id, q.enqueued_at, a.public_id
		     FROM group_queue q
		     JOIN agents a ON a.id = q.agent_id
		     WHERE q.status = 'waiting' AND q.game = $1 AND q.bid = $2
		 ),
		 me AS (SELECT enqueued_at, agent_id FROM pool WHERE public_id = $3)
		 SELECT
		     (SELECT COUNT(*) FROM pool p
		        WHERE (p.enqueued_at, p.agent_id) <= (SELECT enqueued_at, agent_id FROM me))::int,
		     (SELECT COUNT(*) FROM pool)::int,
		     (SELECT COUNT(DISTINCT owner_user_id) FROM pool)::int`,
		game, bid, agentPublicID).
		Scan(&ps.Position, &ps.Waiting, &ps.DistinctOwners)
	if err != nil {
		return groupmatch.PoolStats{}, err
	}
	return ps, nil
}

// ClaimGroup reserves ALL given entries (waiting -> claimed) all-or-nothing. It locks
// the rows FOR UPDATE in public_id order (deadlock-safe) and only claims when EVERY
// one is still waiting, so two matcher instances racing the same snapshot can never
// both create a table for the same agents — the loser sees 'claimed' and returns false.
func (r *GroupQueueRepo) ClaimGroup(ctx context.Context, agentPublicIDs []string) (bool, error) {
	if len(agentPublicIDs) == 0 {
		return false, nil
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx,
		`SELECT q.status
		 FROM group_queue q
		 JOIN agents a ON a.id = q.agent_id
		 WHERE a.public_id = ANY($1)
		 ORDER BY a.public_id
		 FOR UPDATE OF q`, agentPublicIDs) // lock only the queue rows, not agents
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
	if total != len(agentPublicIDs) || waiting != len(agentPublicIDs) {
		return false, nil // at least one already claimed/matched/dequeued
	}

	if _, err := tx.Exec(ctx,
		`UPDATE group_queue
		 SET status = 'claimed', updated_at = now()
		 WHERE agent_id IN (SELECT id FROM agents WHERE public_id = ANY($1))`,
		agentPublicIDs); err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func (r *GroupQueueRepo) ReleaseGroup(ctx context.Context, agentPublicIDs []string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE group_queue SET status = 'waiting', updated_at = now()
		 WHERE status = 'claimed'
		   AND agent_id IN (SELECT id FROM agents WHERE public_id = ANY($1))`,
		agentPublicIDs)
	return err
}

func (r *GroupQueueRepo) MarkMatchedGroup(ctx context.Context, agentPublicIDs []string, matchPublicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE group_queue SET status = 'matched', match_id = $2, updated_at = now()
		 WHERE status = 'claimed'
		   AND agent_id IN (SELECT id FROM agents WHERE public_id = ANY($1))`,
		agentPublicIDs, matchPublicID)
	return err
}
