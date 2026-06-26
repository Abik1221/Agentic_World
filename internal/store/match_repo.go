package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	"github.com/agent-arena/arena/internal/match"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MatchRepo is the pgx implementation of match.Repo. Each event-appending method
// performs the append in the SAME transaction as the snapshot/status update, so
// the authoritative log and the cached snapshot can never diverge.
//
// (Hand-written parameterized SQL; sqlc-generation is the documented target.)
type MatchRepo struct{ db *pgxpool.Pool }

func NewMatchRepo(db *pgxpool.Pool) *MatchRepo { return &MatchRepo{db: db} }

var _ match.Repo = (*MatchRepo)(nil)

func (r *MatchRepo) CreateWaitingMatch(ctx context.Context, in match.CreateMatchInput) (match.Match, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return match.Match{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var matchID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
		     engine_version, prize_seed_commit, prize_seed, fairness_mode, creator_owner_user_id)
		 VALUES ($1,$2,'waiting',$3,$4,$5,$6,$7,$8,$9,(SELECT id FROM users WHERE public_id=$10))
		 RETURNING id`,
		in.PublicID, in.Game, in.Bid, in.RakePct, in.TotalRounds,
		in.EngineVersion, in.Commit, in.Seed, in.FairnessMode, in.Creator.OwnerPublicID).Scan(&matchID)
	if err != nil {
		return match.Match{}, err
	}
	if err := insertPlayer(ctx, tx, matchID, in.Creator); err != nil {
		return match.Match{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return match.Match{}, err
	}
	return match.Match{
		PublicID: in.PublicID, Game: in.Game, Status: match.StatusWaiting, Bid: in.Bid,
		RakePct: in.RakePct, TotalRounds: in.TotalRounds, EngineVersion: in.EngineVersion,
		Commit: in.Commit, FairnessMode: in.FairnessMode, Seed: in.Seed,
		Players: []match.Player{in.Creator},
	}, nil
}

func (r *MatchRepo) ListWaiting(ctx context.Context, game string, bid int64, excludeOwnerPublicID string, limit int) ([]match.LobbyItem, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.game, m.bid, ag.public_id, m.created_at
		 FROM matches m
		 JOIN match_players mp ON mp.match_id = m.id AND mp.seat = 0
		 JOIN agents ag ON ag.id = mp.agent_id
		 WHERE m.status = 'waiting' AND m.game = $1
		   AND ($2 <= 0 OR m.bid = $2)
		   AND m.creator_owner_user_id <> COALESCE((SELECT id FROM users WHERE public_id = $3), 0)
		 ORDER BY m.created_at DESC
		 LIMIT $4`,
		game, bid, excludeOwnerPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []match.LobbyItem
	for rows.Next() {
		var it match.LobbyItem
		if err := rows.Scan(&it.PublicID, &it.Game, &it.Bid, &it.CreatorAgentPublicID, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *MatchRepo) Get(ctx context.Context, matchPublicID string) (match.Match, error) {
	var m match.Match
	var stateBytes []byte
	var deadline *time.Time
	err := r.db.QueryRow(ctx,
		`SELECT m.public_id, m.game, m.status, m.mode, COALESCE(m.bot_policy, ''), m.bid, m.rake_pct, m.total_rounds,
		        m.engine_version, m.prize_seed_commit, m.prize_seed, m.fairness_mode,
		        COALESCE(m.state, '{}'::jsonb), m.round_deadline,
		        COALESCE(wa.public_id, ''), COALESCE(m.replay_hash, '')
		 FROM matches m
		 LEFT JOIN agents wa ON wa.id = m.winner_agent_id
		 WHERE m.public_id = $1`, matchPublicID).
		Scan(&m.PublicID, &m.Game, &m.Status, &m.Mode, &m.BotPolicy, &m.Bid, &m.RakePct, &m.TotalRounds,
			&m.EngineVersion, &m.Commit, &m.Seed, &m.FairnessMode,
			&stateBytes, &deadline, &m.WinnerAgent, &m.ReplayHash)
	if err != nil {
		return match.Match{}, err
	}
	if len(stateBytes) > 0 {
		if err := json.Unmarshal(stateBytes, &m.State); err != nil {
			return match.Match{}, err
		}
	}
	m.RoundDeadline = deadline

	players, err := r.loadPlayers(ctx, matchPublicID)
	if err != nil {
		return match.Match{}, err
	}
	m.Players = players
	return m, nil
}

func (r *MatchRepo) loadPlayers(ctx context.Context, matchPublicID string) ([]match.Player, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ag.public_id, u.public_id, mp.seat, COALESCE(mp.final_score,0), COALESCE(mp.coins_delta,0)
		 FROM match_players mp
		 JOIN agents ag ON ag.id = mp.agent_id
		 JOIN users  u  ON u.id  = mp.owner_user_id
		 WHERE mp.match_id = (SELECT id FROM matches WHERE public_id = $1)
		 ORDER BY mp.seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []match.Player
	for rows.Next() {
		var p match.Player
		if err := rows.Scan(&p.AgentPublicID, &p.OwnerPublicID, &p.Seat, &p.FinalScore, &p.CoinsDelta); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *MatchRepo) Activate(ctx context.Context, matchPublicID string, joiner match.Player, state gs.State, deadline time.Time, events []gs.Event) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET status='active', state=$2::jsonb, round_deadline=$3, started_at=now(), updated_at=now()
			 WHERE public_id=$1 AND status='waiting' RETURNING id`,
			matchPublicID, mustJSON(state), deadline).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return match.ErrNotWaiting
		}
		if err != nil {
			return err
		}
		if err := insertPlayer(ctx, tx, matchID, joiner); err != nil {
			return err
		}
		return insertEvents(ctx, tx, matchID, events)
	})
}

func (r *MatchRepo) CreatePairedActive(ctx context.Context, in match.CreatePairedInput) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO matches (public_id, game, status, mode, bot_policy, bid, rake_pct, total_rounds,
			     engine_version, prize_seed_commit, prize_seed, fairness_mode,
			     state, round_deadline, started_at, creator_owner_user_id)
			 VALUES ($1,$2,'active',COALESCE(NULLIF($13,''),'competitive'),NULLIF($14,''),$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,now(),
			     (SELECT id FROM users WHERE public_id=$12))
			 RETURNING id`,
			in.PublicID, in.Game, in.Bid, in.RakePct, in.TotalRounds,
			in.EngineVersion, in.Commit, in.Seed, in.FairnessMode,
			mustJSON(in.State), in.Deadline, in.SeatA.OwnerPublicID, in.Mode, in.BotPolicy).Scan(&matchID)
		if err != nil {
			return err
		}
		if err := insertPlayer(ctx, tx, matchID, in.SeatA); err != nil {
			return err
		}
		if err := insertPlayer(ctx, tx, matchID, in.SeatB); err != nil {
			return err
		}
		return insertEvents(ctx, tx, matchID, in.Events)
	})
}

func (r *MatchRepo) Advance(ctx context.Context, matchPublicID string, state gs.State, deadline *time.Time, events []gs.Event) error {
	err := r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET state=$2::jsonb, round_deadline=$3, updated_at=now()
			 WHERE public_id=$1 AND status='active' RETURNING id`,
			matchPublicID, mustJSON(state), deadline).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return match.ErrNotActive
		}
		if err != nil {
			return err
		}
		return insertEvents(ctx, tx, matchID, events)
	})
	// A UNIQUE(match_id, seq) collision means another writer advanced this match
	// first: optimistic-concurrency conflict, not a hard error. The service re-reads
	// and retries (so correctness no longer depends on holding the Redis lock).
	if isUniqueViolation(err) {
		return match.ErrConcurrentUpdate
	}
	return err
}

func (r *MatchRepo) Finish(ctx context.Context, matchPublicID string, state gs.State, winnerAgentPublicID, replayHash string, players []match.Player, events []gs.Event) error {
	err := r.finishTx(ctx, matchPublicID, state, winnerAgentPublicID, replayHash, players, events)
	if isUniqueViolation(err) {
		return match.ErrConcurrentUpdate
	}
	return err
}

func (r *MatchRepo) finishTx(ctx context.Context, matchPublicID string, state gs.State, winnerAgentPublicID, replayHash string, players []match.Player, events []gs.Event) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET status='finished', state=$2::jsonb, round_deadline=NULL,
			     finished_at=now(), updated_at=now(),
			     winner_agent_id=(SELECT id FROM agents WHERE public_id=$3), replay_hash=$4
			 WHERE public_id=$1 AND status='active' RETURNING id`,
			matchPublicID, mustJSON(state), nullString(winnerAgentPublicID), replayHash).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return match.ErrNotActive
		}
		if err != nil {
			return err
		}
		for _, p := range players {
			if _, err := tx.Exec(ctx,
				`UPDATE match_players SET final_score=$3, coins_delta=$4 WHERE match_id=$1 AND seat=$2`,
				matchID, p.Seat, p.FinalScore, p.CoinsDelta); err != nil {
				return err
			}
		}
		return insertEvents(ctx, tx, matchID, events)
	})
}

func (r *MatchRepo) ListActiveExpired(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT public_id FROM matches
		 WHERE status='active' AND round_deadline IS NOT NULL AND round_deadline <= $1
		 ORDER BY round_deadline ASC LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *MatchRepo) LoadEvents(ctx context.Context, matchPublicID string) ([]gs.Event, error) {
	rows, err := r.db.Query(ctx,
		`SELECT seq, type, payload FROM match_events
		 WHERE match_id = (SELECT id FROM matches WHERE public_id=$1)
		 ORDER BY seq ASC`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []gs.Event
	for rows.Next() {
		var seq int
		var typ string
		var payload []byte
		if err := rows.Scan(&seq, &typ, &payload); err != nil {
			return nil, err
		}
		var p any
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, err
		}
		out = append(out, gs.Event{Seq: seq, Type: gs.EventType(typ), Payload: p})
	}
	return out, rows.Err()
}

func (r *MatchRepo) AgentSigningKey(ctx context.Context, agentPublicID string) (string, error) {
	var key string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE(signing_pubkey, '') FROM agents WHERE public_id = $1`, agentPublicID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return key, err
}

func (r *MatchRepo) RecordMoveSignature(ctx context.Context, matchPublicID string, round, seat, card int, signature, pubkey string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO move_signatures (match_id, round, seat, card, signature, pubkey)
		 SELECT m.id, $2, $3, $4, $5, $6 FROM matches m WHERE m.public_id = $1
		 ON CONFLICT (match_id, round, seat) DO NOTHING`,
		matchPublicID, round, seat, card, signature, pubkey)
	return err
}

func (r *MatchRepo) LoadMoveSignatures(ctx context.Context, matchPublicID string) ([]match.MoveSignature, error) {
	rows, err := r.db.Query(ctx,
		`SELECT round, seat, card, signature, pubkey FROM move_signatures
		 WHERE match_id = (SELECT id FROM matches WHERE public_id = $1)
		 ORDER BY round, seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []match.MoveSignature
	for rows.Next() {
		var m match.MoveSignature
		if err := rows.Scan(&m.Round, &m.Seat, &m.Card, &m.Signature, &m.Pubkey); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ── helpers ──────────────────────────────────────────────────────────────────

func (r *MatchRepo) tx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func insertPlayer(ctx context.Context, tx pgx.Tx, matchID int64, p match.Player) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat)
		 VALUES ($1, (SELECT id FROM agents WHERE public_id=$2), (SELECT id FROM users WHERE public_id=$3), $4)`,
		matchID, p.AgentPublicID, p.OwnerPublicID, p.Seat)
	return err
}

func insertEvents(ctx context.Context, tx pgx.Tx, matchID int64, events []gs.Event) error {
	for _, ev := range events {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO match_events (match_id, seq, type, payload) VALUES ($1,$2,$3,$4::jsonb)`,
			matchID, ev.Seq, string(ev.Type), string(b)); err != nil {
			return err
		}
	}
	return nil
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}
