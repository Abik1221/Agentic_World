package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	mono "github.com/agent-arena/arena/internal/engine/monopoly"
	"github.com/agent-arena/arena/internal/monopoly"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MonopolyRepo persists Monopoly matches on the shared matches / match_players /
// match_events tables (game='monopoly'), storing the full engine State as JSONB —
// exactly the pattern the Mafia repo uses. The player count is recovered from
// len(State.Players), so no extra columns are needed.
type MonopolyRepo struct{ db *pgxpool.Pool }

func NewMonopolyRepo(db *pgxpool.Pool) *MonopolyRepo { return &MonopolyRepo{db: db} }

var _ monopoly.Repo = (*MonopolyRepo)(nil)

func (r *MonopolyRepo) Create(ctx context.Context, in monopoly.CreateMatchInput) (monopoly.Match, error) {
	var out monopoly.Match
	err := r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
			     engine_version, prize_seed_commit, prize_seed, fairness_mode, state,
			     round_deadline, started_at, creator_owner_user_id)
			 VALUES ($1,'monopoly','active',$2,$3,$4,$5,$6,$7,'deterministic',$8::jsonb,$9,now(),
			         (SELECT id FROM users WHERE public_id=$10))
			 RETURNING id`,
			in.PublicID, in.EntryFee, in.RakePct, mono.DefaultMaxTurns, mono.Version,
			in.Commit, in.Seed, mustJSON(in.State), in.Deadline, in.Creator.OwnerPublicID).
			Scan(&matchID)
		if err != nil {
			return err
		}
		if err := insertMonopolyPlayer(ctx, tx, matchID, in.Creator); err != nil {
			return err
		}
		return insertMonopolyEvents(ctx, tx, matchID, in.Events)
	})
	if err != nil {
		return monopoly.Match{}, err
	}
	out = monopoly.Match{
		PublicID: in.PublicID, Title: in.Title, Status: monopoly.StatusActive,
		EntryFee: in.EntryFee, RakePct: in.RakePct, EngineVersion: mono.Version,
		Commit: in.Commit, Seed: in.Seed, Players: in.Players, State: in.State,
		Agents: []monopoly.Player{in.Creator}, WinnerSeat: -1,
	}
	return out, nil
}

// CreateWaiting opens a staked agent-vs-agent table in the WAITING state: only the
// creator is seated (seat 0), no engine state yet (state stays NULL until Start).
func (r *MonopolyRepo) CreateWaiting(ctx context.Context, in monopoly.CreateMatchInput) (monopoly.Match, error) {
	err := r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
			     engine_version, prize_seed_commit, prize_seed, fairness_mode, target_players,
			     creator_owner_user_id)
			 VALUES ($1,'monopoly','waiting',$2,$3,$4,$5,$6,$7,'deterministic',$8,
			         (SELECT id FROM users WHERE public_id=$9))
			 RETURNING id`,
			in.PublicID, in.EntryFee, in.RakePct, mono.DefaultMaxTurns, mono.Version,
			in.Commit, in.Seed, in.TargetPlayers, in.Creator.OwnerPublicID).Scan(&matchID)
		if err != nil {
			return err
		}
		return insertMonopolyPlayer(ctx, tx, matchID, in.Creator)
	})
	if err != nil {
		return monopoly.Match{}, err
	}
	return monopoly.Match{
		PublicID: in.PublicID, Title: in.Title, Status: monopoly.StatusWaiting,
		EntryFee: in.EntryFee, RakePct: in.RakePct, EngineVersion: mono.Version,
		Commit: in.Commit, Seed: in.Seed, TargetPlayers: in.TargetPlayers,
		Agents: []monopoly.Player{in.Creator}, WinnerSeat: -1,
	}, nil
}

// ListWaiting lists open waiting Monopoly tables at the given entry fee (0 = any),
// excluding tables the given owner already created.
func (r *MonopolyRepo) ListWaiting(ctx context.Context, entryFee int64, excludeOwnerPublicID string, limit int) ([]monopoly.LobbyItem, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.bid,
		        (SELECT COUNT(*) FROM match_players mp2 WHERE mp2.match_id = m.id),
		        COALESCE(m.target_players, 0), ag.public_id, m.created_at
		 FROM matches m
		 JOIN match_players mp ON mp.match_id = m.id AND mp.seat = 0
		 JOIN agents ag ON ag.id = mp.agent_id
		 WHERE m.status = 'waiting' AND m.game = 'monopoly'
		   AND ($1 <= 0 OR m.bid = $1)
		   AND m.creator_owner_user_id <> COALESCE((SELECT id FROM users WHERE public_id = $2), 0)
		 ORDER BY m.created_at DESC
		 LIMIT $3`, entryFee, excludeOwnerPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monopoly.LobbyItem
	for rows.Next() {
		var it monopoly.LobbyItem
		if err := rows.Scan(&it.PublicID, &it.EntryFee, &it.SeatsFilled, &it.SeatsTotal, &it.CreatorAgentPublicID, &it.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// JoinSeat seats a new agent on a waiting table under a row lock so concurrent
// joins can't oversubscribe seats.
func (r *MonopolyRepo) JoinSeat(ctx context.Context, matchPublicID string, p monopoly.Player) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`SELECT id FROM matches WHERE public_id=$1 AND status='waiting' AND game='monopoly' FOR UPDATE`,
			matchPublicID).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return monopoly.ErrNotWaiting
		}
		if err != nil {
			return err
		}
		return insertMonopolyPlayer(ctx, tx, matchID, p)
	})
}

// Start flips a full waiting table to active with its initialized engine state.
func (r *MonopolyRepo) Start(ctx context.Context, matchPublicID string, state mono.State, deadline time.Time, events []mono.Event) error {
	err := r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET status='active', state=$2::jsonb, round_deadline=$3, started_at=now(), updated_at=now()
			 WHERE public_id=$1 AND status='waiting' AND game='monopoly' RETURNING id`,
			matchPublicID, mustJSON(state), deadline).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return monopoly.ErrNotWaiting
		}
		if err != nil {
			return err
		}
		return insertMonopolyEvents(ctx, tx, matchID, events)
	})
	if isUniqueViolation(err) {
		return monopoly.ErrConcurrentUpdate
	}
	return err
}

// CancelWaiting aborts a waiting table if the caller is its creator (seat 0).
func (r *MonopolyRepo) CancelWaiting(ctx context.Context, matchPublicID, creatorAgentPublicID string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE matches SET status='aborted', updated_at=now()
		 WHERE public_id=$1 AND status='waiting' AND game='monopoly'
		   AND EXISTS (
		     SELECT 1 FROM match_players mp
		     JOIN agents ag ON ag.id = mp.agent_id
		     WHERE mp.match_id = matches.id AND mp.seat = 0 AND ag.public_id = $2
		   )`, matchPublicID, creatorAgentPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return monopoly.ErrNotWaiting
	}
	return nil
}

func (r *MonopolyRepo) ExpireStaleWaiting(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE matches SET status='aborted', updated_at=now()
		 WHERE id IN (
		   SELECT id FROM matches
		   WHERE game='monopoly' AND status='waiting' AND created_at <= $1
		   ORDER BY created_at ASC LIMIT $2
		 )`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (r *MonopolyRepo) Get(ctx context.Context, matchPublicID string) (monopoly.Match, error) {
	var m monopoly.Match
	var stateBytes []byte
	var deadline *time.Time
	var seed []byte
	err := r.db.QueryRow(ctx,
		`SELECT m.public_id, m.status, m.bid, m.rake_pct, m.engine_version,
		        m.prize_seed_commit, m.prize_seed, COALESCE(m.state, '{}'::jsonb),
		        m.round_deadline, COALESCE(m.replay_hash, ''), COALESCE(m.target_players, 0)
		 FROM matches m WHERE m.public_id = $1 AND m.game = 'monopoly'`, matchPublicID).
		Scan(&m.PublicID, &m.Status, &m.EntryFee, &m.RakePct, &m.EngineVersion,
			&m.Commit, &seed, &stateBytes, &deadline, &m.ReplayHash, &m.TargetPlayers)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return monopoly.Match{}, monopoly.ErrNotFound
		}
		return monopoly.Match{}, err
	}
	m.Title = "Monopoly AI Arena"
	m.Seed = seed
	m.RoundDeadline = deadline
	if len(stateBytes) > 0 {
		// Authoritative state: fail loudly on a decode error rather than load a
		// zero-value (phantom reset) board that PendingSeat/view/Act would trust.
		if err := json.Unmarshal(stateBytes, &m.State); err != nil {
			return monopoly.Match{}, err
		}
	}
	m.Players = len(m.State.Players)
	m.WinnerSeat = m.State.Winner
	agents, err := r.loadAgents(ctx, matchPublicID)
	if err != nil {
		return monopoly.Match{}, err
	}
	m.Agents = agents
	return m, nil
}

func (r *MonopolyRepo) loadAgents(ctx context.Context, matchPublicID string) ([]monopoly.Player, error) {
	rows, err := r.db.Query(ctx,
		`SELECT mp.seat, ag.public_id, u.public_id, COALESCE(mp.coins_delta, 0),
		        COALESCE(ag.name, ''), COALESCE(u.display_name, ''), COALESCE(u.avatar_url, '')
		 FROM match_players mp
		 JOIN agents ag ON ag.id = mp.agent_id
		 JOIN users u ON u.id = mp.owner_user_id
		 WHERE mp.match_id = (SELECT id FROM matches WHERE public_id = $1)
		 ORDER BY mp.seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monopoly.Player
	for rows.Next() {
		var p monopoly.Player
		if err := rows.Scan(&p.Seat, &p.AgentPublicID, &p.OwnerPublicID, &p.CoinsDelta,
			&p.Name, &p.OwnerName, &p.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *MonopolyRepo) Advance(ctx context.Context, matchPublicID string, state mono.State, deadline *time.Time, events []mono.Event) error {
	err := r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET state=$2::jsonb, round_deadline=$3, updated_at=now()
			 WHERE public_id=$1 AND status='active' AND game='monopoly' RETURNING id`,
			matchPublicID, mustJSON(state), deadline).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return monopoly.ErrNotActive
		}
		if err != nil {
			return err
		}
		return insertMonopolyEvents(ctx, tx, matchID, events)
	})
	// A racing writer inserted the same (match_id, seq) first — surface as a
	// retryable concurrent-update so the service can re-read and retry (this is
	// what makes the Redis lock a fast-path rather than a correctness requirement).
	if isUniqueViolation(err) {
		return monopoly.ErrConcurrentUpdate
	}
	return err
}

func (r *MonopolyRepo) Finish(ctx context.Context, matchPublicID string, state mono.State, winnerSeat int, replayHash string, agents []monopoly.Player, events []mono.Event) error {
	err := r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET status='finished', state=$2::jsonb, round_deadline=NULL,
			     finished_at=now(), updated_at=now(), replay_hash=$3,
			     winner_agent_id = (
			       SELECT mp.agent_id FROM match_players mp
			       WHERE mp.match_id = matches.id AND mp.seat = $4
			     )
			 WHERE public_id=$1 AND status='active' AND game='monopoly' RETURNING id`,
			matchPublicID, mustJSON(state), replayHash, winnerSeat).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return monopoly.ErrNotActive
		}
		if err != nil {
			return err
		}
		for _, p := range agents {
			if _, err := tx.Exec(ctx,
				`UPDATE match_players SET coins_delta=$3 WHERE match_id=$1 AND seat=$2`,
				matchID, p.Seat, p.CoinsDelta); err != nil {
				return err
			}
		}
		return insertMonopolyEvents(ctx, tx, matchID, events)
	})
	if isUniqueViolation(err) {
		return monopoly.ErrConcurrentUpdate
	}
	return err
}

func (r *MonopolyRepo) ListActiveExpired(ctx context.Context, game string, now time.Time, limit int) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT public_id FROM matches
		 WHERE status='active' AND game=$1 AND round_deadline IS NOT NULL AND round_deadline <= $2
		 ORDER BY round_deadline ASC LIMIT $3`, game, now, limit)
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

// LoadEventsTimed is LoadEvents plus each event's wall-clock write time, for a
// replay that reproduces the original pacing. Separate from LoadEvents because the
// plain events feed the replay hash and must stay byte-identical across a DB
// round-trip; timing rides alongside as presentation data.
func (r *MonopolyRepo) LoadEventsTimed(ctx context.Context, matchPublicID string) ([]monopoly.TimedEvent, error) {
	rows, err := r.db.Query(ctx,
		`SELECT seq, type, payload, created_at FROM match_events
		 WHERE match_id = (SELECT id FROM matches WHERE public_id=$1)
		 ORDER BY seq ASC`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monopoly.TimedEvent
	for rows.Next() {
		var seq int
		var typ string
		var payload []byte
		var at time.Time
		if err := rows.Scan(&seq, &typ, &payload, &at); err != nil {
			return nil, err
		}
		var p any
		if err := json.Unmarshal(payload, &p); err != nil {
			return nil, err
		}
		out = append(out, monopoly.TimedEvent{
			Event: mono.Event{Seq: seq, Type: mono.EventType(typ), Payload: p},
			At:    at,
		})
	}
	return out, rows.Err()
}

func (r *MonopolyRepo) LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mono.Event, error) {
	q := `SELECT seq, type, payload FROM match_events
	      WHERE match_id = (SELECT id FROM matches WHERE public_id=$1)`
	args := []any{matchPublicID}
	if afterSeq >= 0 {
		q += ` AND seq > $2`
		args = append(args, afterSeq)
	}
	q += ` ORDER BY seq ASC`
	rows, err := r.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mono.Event
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
		out = append(out, mono.Event{Seq: seq, Type: mono.EventType(typ), Payload: p})
	}
	return out, rows.Err()
}

func (r *MonopolyRepo) LiveMatches(ctx context.Context) ([]monopoly.LiveMatch, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, COALESCE(m.state, '{}'::jsonb),
		        (SELECT array_agg(ag.public_id ORDER BY mp.seat)
		           FROM match_players mp JOIN agents ag ON ag.id = mp.agent_id
		          WHERE mp.match_id = m.id)
		 FROM matches m
		 WHERE m.game = 'monopoly' AND m.status = 'active'
		 ORDER BY m.started_at DESC NULLS LAST, m.created_at DESC
		 LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []monopoly.LiveMatch
	for rows.Next() {
		var lm monopoly.LiveMatch
		var stateBytes []byte
		var agents []string
		if err := rows.Scan(&lm.MatchID, &stateBytes, &agents); err != nil {
			return nil, err
		}
		var st mono.State
		if len(stateBytes) > 0 {
			// Skip a row with corrupt state rather than list a zero-value phantom.
			if err := json.Unmarshal(stateBytes, &st); err != nil {
				continue
			}
		}
		lm.Title = "Monopoly AI Arena"
		lm.Agents = agents
		lm.Players = len(st.Players)
		lm.Round = st.TurnCount
		lm.Phase = st.Phase
		lm.Winner = st.Winner
		active := 0
		for i := range st.Players {
			if !st.Players[i].Bankrupt {
				active++
			}
		}
		lm.Active = active
		out = append(out, lm)
	}
	return out, rows.Err()
}

func (r *MonopolyRepo) tx(ctx context.Context, fn func(pgx.Tx) error) error {
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

func insertMonopolyPlayer(ctx context.Context, tx pgx.Tx, matchID int64, p monopoly.Player) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat)
		 VALUES ($1, (SELECT id FROM agents WHERE public_id=$2),
		         (SELECT id FROM users WHERE public_id=$3), $4)`,
		matchID, p.AgentPublicID, p.OwnerPublicID, p.Seat)
	return err
}

func insertMonopolyEvents(ctx context.Context, tx pgx.Tx, matchID int64, events []mono.Event) error {
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

// AgentSigningKey — see monopoly.Repo. Reads the shared agents.signing_pubkey.
func (r *MonopolyRepo) AgentSigningKey(ctx context.Context, agentPublicID string) (string, error) {
	return agentSigningKey(ctx, r.db, agentPublicID)
}

// RecordMoveSignature — see monopoly.Repo. Persists a per-move authorship proof.
func (r *MonopolyRepo) RecordMoveSignature(ctx context.Context, matchPublicID string, seq, seat int, action, signature, pubkey string) error {
	return recordGameMoveSig(ctx, r.db, "monopoly", matchPublicID, seq, seat, action, signature, pubkey)
}

// LoadMoveSignatures — see monopoly.Repo. Returns all proofs for replay re-verify.
func (r *MonopolyRepo) LoadMoveSignatures(ctx context.Context, matchPublicID string) ([]monopoly.MoveSig, error) {
	rows, err := loadGameMoveSigs(ctx, r.db, matchPublicID)
	if err != nil {
		return nil, err
	}
	out := make([]monopoly.MoveSig, len(rows))
	for i, s := range rows {
		out[i] = monopoly.MoveSig{Seq: s.Seq, Seat: s.Seat, Action: s.Action, Signature: s.Signature, Pubkey: s.Pubkey}
	}
	return out, nil
}
