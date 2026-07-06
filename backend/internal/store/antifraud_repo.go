package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/antifraud"
	"github.com/agent-arena/arena/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AntifraudRepo is the pgx implementation of antifraud.Repo.
type AntifraudRepo struct{ db *pgxpool.Pool }

func NewAntifraudRepo(db *pgxpool.Pool) *AntifraudRepo { return &AntifraudRepo{db: db} }

var _ antifraud.Repo = (*AntifraudRepo)(nil)

func (r *AntifraudRepo) MatchAgents(ctx context.Context, matchPublicID string) ([]antifraud.AgentRef, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ag.public_id, u.public_id
		 FROM match_players mp
		 JOIN agents ag ON ag.id = mp.agent_id
		 JOIN users  u  ON u.id  = mp.owner_user_id
		 JOIN matches m ON m.id  = mp.match_id
		 WHERE m.public_id = $1
		 ORDER BY mp.seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []antifraud.AgentRef
	for rows.Next() {
		var a antifraud.AgentRef
		if err := rows.Scan(&a.AgentPublicID, &a.OwnerPublicID); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *AntifraudRepo) AnyFlagged(ctx context.Context, agentPublicIDs []string) (bool, error) {
	var exists bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM fraud_flags f JOIN agents a ON a.id = f.agent_id
		   WHERE a.public_id = ANY($1) AND f.active)`, agentPublicIDs).Scan(&exists)
	return exists, err
}

func (r *AntifraudRepo) RecordFlag(ctx context.Context, agentPublicID, matchPublicID, typ, detail string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO fraud_flags (agent_id, match_id, type, detail)
		 SELECT a.id,
		        (SELECT id FROM matches WHERE public_id = $2),
		        $3, $4
		 FROM agents a
		 WHERE a.public_id = $1
		   AND NOT EXISTS (
		     SELECT 1 FROM fraud_flags f2
		     WHERE f2.agent_id = a.id AND f2.type = $3 AND f2.active)`,
		agentPublicID, nullString(matchPublicID), typ, detail)
	return err
}

func (r *AntifraudRepo) RecordHold(ctx context.Context, matchPublicID, reason string) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO payout_holds (match_id, reason)
		 SELECT id, $2 FROM matches WHERE public_id = $1
		 ON CONFLICT (match_id) DO NOTHING`, matchPublicID, reason)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

func (r *AntifraudRepo) ResolveHold(ctx context.Context, matchPublicID, status string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE payout_holds SET status = $2, resolved_at = now()
		 WHERE match_id = (SELECT id FROM matches WHERE public_id = $1)`, matchPublicID, status)
	return err
}

func (r *AntifraudRepo) OpenDispute(ctx context.Context, in antifraud.DisputeInput) (string, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var pub string
	err = tx.QueryRow(ctx,
		`INSERT INTO disputes (public_id, match_id, agent_id, reporter_user_id, kind, detail)
		 VALUES ($1,
		         (SELECT id FROM matches WHERE public_id = $2),
		         (SELECT id FROM agents  WHERE public_id = $3),
		         (SELECT id FROM users   WHERE public_id = $4),
		         $5, $6)
		 RETURNING public_id`,
		in.PublicID, nullString(in.MatchPublicID), nullString(in.AgentPublicID),
		in.ReporterUserID, in.Kind, nullString(in.Detail)).Scan(&pub)
	if err != nil {
		return "", err
	}
	// dispute.opened for the Super Admin mirror, in the same tx as the insert.
	payload, err := json.Marshal(map[string]any{
		"dispute_id": pub, "kind": in.Kind,
		"match": in.MatchPublicID, "agent": in.AgentPublicID,
	})
	if err != nil {
		return "", err
	}
	if _, err := InsertEventTx(ctx, tx, events.TypeDisputeOpened, payload); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return pub, nil
}

// ResolveDispute transitions an open/reviewing dispute, returning the linked match
// public id. changed=false (idempotent) when the dispute was already terminal.
func (r *AntifraudRepo) ResolveDispute(ctx context.Context, disputePublicID, status, resolution string) (string, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var matchID *int64
	var cur string
	err = tx.QueryRow(ctx,
		`SELECT status, match_id FROM disputes WHERE public_id = $1 FOR UPDATE`, disputePublicID).
		Scan(&cur, &matchID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if cur == "resolved" || cur == "rejected" {
		return "", false, tx.Commit(ctx) // already terminal — idempotent
	}

	if _, err := tx.Exec(ctx,
		`UPDATE disputes SET status = $2, resolution = $3, resolved_at = now() WHERE public_id = $1`,
		disputePublicID, status, resolution); err != nil {
		return "", false, err
	}

	var matchPublicID string
	if matchID != nil {
		if err := tx.QueryRow(ctx, `SELECT public_id FROM matches WHERE id = $1`, *matchID).Scan(&matchPublicID); err != nil {
			return "", false, err
		}
	}
	return matchPublicID, true, tx.Commit(ctx)
}

func (r *AntifraudRepo) RecentPairs(ctx context.Context, since time.Time, minGames int) ([]antifraud.Pair, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, b.public_id, COUNT(*) AS games,
		        SUM(CASE WHEN mpa.coins_delta > 0 THEN 1 ELSE 0 END) AS a_wins,
		        SUM(CASE WHEN mpb.coins_delta > 0 THEN 1 ELSE 0 END) AS b_wins,
		        COALESCE(SUM(mpb.coins_delta), 0) AS net_flow_a_to_b
		 FROM matches m
		 JOIN match_players mpa ON mpa.match_id = m.id AND mpa.seat = 0
		 JOIN match_players mpb ON mpb.match_id = m.id AND mpb.seat = 1
		 JOIN agents a ON a.id = mpa.agent_id
		 JOIN agents b ON b.id = mpb.agent_id
		 WHERE m.status = 'finished' AND m.finished_at >= $1
		 GROUP BY a.public_id, b.public_id
		 HAVING COUNT(*) >= $2`, since, minGames)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []antifraud.Pair
	for rows.Next() {
		var p antifraud.Pair
		if err := rows.Scan(&p.A, &p.B, &p.Games, &p.AWins, &p.BWins, &p.NetFlowAToB); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *AntifraudRepo) AgentsWithSamples(ctx context.Context, min int) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id
		 FROM agent_timing_samples ats JOIN agents a ON a.id = ats.agent_id
		 GROUP BY a.public_id HAVING COUNT(*) >= $1`, min)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *AntifraudRepo) AgentTiming(ctx context.Context, agentPublicID string) (antifraud.TimingStat, error) {
	var t antifraud.TimingStat
	var mean, std *float64
	err := r.db.QueryRow(ctx,
		`SELECT COUNT(*), AVG(response_ms), STDDEV_SAMP(response_ms)
		 FROM agent_timing_samples ats JOIN agents a ON a.id = ats.agent_id
		 WHERE a.public_id = $1`, agentPublicID).Scan(&t.Count, &mean, &std)
	if err != nil {
		return antifraud.TimingStat{}, err
	}
	if mean != nil {
		t.MeanMs = *mean
	}
	if std != nil {
		t.StdMs = *std
	}
	return t, nil
}

func (r *AntifraudRepo) Audit(ctx context.Context, actor, action, target string, detail []byte) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO audit_log (actor, action, target, detail) VALUES ($1, $2, $3, $4::jsonb)`,
		actor, action, nullString(target), string(detail))
	return err
}

// PairMoves returns the per-round revealed bids of every finished match between
// agentA and agentB since `since`, oriented so CardA is always agentA's card
// regardless of the seat it held in each match. Reads the round_revealed events
// (payload.cards = [seat0, seat1]). Used for action-correlation detection.
func (r *AntifraudRepo) PairMoves(ctx context.Context, agentA, agentB string, since time.Time) ([]antifraud.MoveSample, error) {
	rows, err := r.db.Query(ctx,
		`SELECT mpa.seat,
		        (e.payload->'cards'->>0)::int AS c0,
		        (e.payload->'cards'->>1)::int AS c1,
		        m.total_rounds
		 FROM matches m
		 JOIN agents aa ON aa.public_id = $1
		 JOIN agents ab ON ab.public_id = $2
		 JOIN match_players mpa ON mpa.match_id = m.id AND mpa.agent_id = aa.id
		 JOIN match_players mpb ON mpb.match_id = m.id AND mpb.agent_id = ab.id
		 JOIN match_events  e   ON e.match_id   = m.id AND e.type = 'round_revealed'
		 WHERE m.status = 'finished' AND m.finished_at >= $3`,
		agentA, agentB, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []antifraud.MoveSample
	for rows.Next() {
		var seatA, c0, c1, deck int
		if err := rows.Scan(&seatA, &c0, &c1, &deck); err != nil {
			return nil, err
		}
		s := antifraud.MoveSample{CardA: c0, CardB: c1, Deck: deck}
		if seatA == 1 { // agentA actually sat at seat 1 → swap so CardA is agentA's
			s.CardA, s.CardB = c1, c0
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
