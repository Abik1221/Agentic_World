package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	gs "github.com/agent-arena/arena/internal/engine/goofspiel"
	eventbus "github.com/agent-arena/arena/internal/events"
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
	var deadline, base *time.Time
	err := r.db.QueryRow(ctx,
		`SELECT m.public_id, m.game, m.status, m.mode, COALESCE(m.bot_policy, ''), m.bid, m.rake_pct, m.total_rounds,
		        m.engine_version, m.prize_seed_commit, m.prize_seed, m.fairness_mode,
		        COALESCE(m.state, '{}'::jsonb), m.round_deadline, m.round_deadline_base,
		        COALESCE(wa.public_id, ''), COALESCE(m.replay_hash, '')
		 FROM matches m
		 LEFT JOIN agents wa ON wa.id = m.winner_agent_id
		 WHERE m.public_id = $1`, matchPublicID).
		Scan(&m.PublicID, &m.Game, &m.Status, &m.Mode, &m.BotPolicy, &m.Bid, &m.RakePct, &m.TotalRounds,
			&m.EngineVersion, &m.Commit, &m.Seed, &m.FairnessMode,
			&stateBytes, &deadline, &base, &m.WinnerAgent, &m.ReplayHash)
	if err != nil {
		return match.Match{}, err
	}
	if len(stateBytes) > 0 {
		if err := json.Unmarshal(stateBytes, &m.State); err != nil {
			return match.Match{}, err
		}
	}
	m.RoundDeadline = deadline
	// Fall back to the deadline when no base is recorded. A row that predates the column, or
	// one written by a path that forgot to set it, then behaves exactly as it did before rather
	// than losing its extension budget outright.
	m.RoundDeadlineBase = base
	if m.RoundDeadlineBase == nil {
		m.RoundDeadlineBase = deadline
	}

	players, err := r.loadPlayers(ctx, matchPublicID)
	if err != nil {
		return match.Match{}, err
	}
	m.Players = players
	return m, nil
}

func (r *MatchRepo) loadPlayers(ctx context.Context, matchPublicID string) ([]match.Player, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ag.public_id, u.public_id, mp.seat, COALESCE(mp.final_score,0), COALESCE(mp.coins_delta,0),
		        COALESCE(ag.name, ''), COALESCE(u.display_name, ''), COALESCE(u.avatar_url, '')
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
		if err := rows.Scan(&p.AgentPublicID, &p.OwnerPublicID, &p.Seat, &p.FinalScore, &p.CoinsDelta,
			&p.Name, &p.OwnerName, &p.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *MatchRepo) Activate(ctx context.Context, matchPublicID string, joiner match.Player, state gs.State, deadline time.Time, events []gs.Event) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		var game string
		var bid int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET status='active', state=$2::jsonb, round_deadline=$3, round_deadline_base=$3, started_at=now(), updated_at=now()
			 WHERE public_id=$1 AND status='waiting' RETURNING id, game, bid`,
			matchPublicID, mustJSON(state), deadline).Scan(&matchID, &game, &bid)
		if errors.Is(err, pgx.ErrNoRows) {
			return match.ErrNotWaiting
		}
		if err != nil {
			return err
		}
		if err := insertPlayer(ctx, tx, matchID, joiner); err != nil {
			return err
		}
		if err := insertEvents(ctx, tx, matchID, events); err != nil {
			return err
		}
		return emitMatchStarted(ctx, tx, matchPublicID, game, bid)
	})
}

func (r *MatchRepo) CreatePairedActive(ctx context.Context, in match.CreatePairedInput) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO matches (public_id, game, status, mode, bot_policy, bid, rake_pct, total_rounds,
			     engine_version, prize_seed_commit, prize_seed, fairness_mode,
			     state, round_deadline, round_deadline_base, started_at, creator_owner_user_id)
			 VALUES ($1,$2,'active',COALESCE(NULLIF($13,''),'competitive'),NULLIF($14,''),$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11,$11,now(),
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
		if err := insertEvents(ctx, tx, matchID, in.Events); err != nil {
			return err
		}
		return emitMatchStarted(ctx, tx, in.PublicID, in.Game, in.Bid)
	})
}

func (r *MatchRepo) Advance(ctx context.Context, matchPublicID string, state gs.State, deadline *time.Time, events []gs.Event) error {
	err := r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET state=$2::jsonb, round_deadline=$3, round_deadline_base=$3, updated_at=now()
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

func (r *MatchRepo) Finish(ctx context.Context, matchPublicID string, state gs.State, winnerAgentPublicID, replayHash string, players []match.Player, events []gs.Event, finishedEvent []byte) error {
	err := r.finishTx(ctx, matchPublicID, state, winnerAgentPublicID, replayHash, players, events, finishedEvent)
	if isUniqueViolation(err) {
		return match.ErrConcurrentUpdate
	}
	return err
}

func (r *MatchRepo) finishTx(ctx context.Context, matchPublicID string, state gs.State, winnerAgentPublicID, replayHash string, players []match.Player, events []gs.Event, finishedEvent []byte) error {
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
		if err := insertEvents(ctx, tx, matchID, events); err != nil {
			return err
		}
		// Domain event outbox (transactional): emit match.finished in the same tx
		// as the match becoming finished, iff the caller supplied a payload.
		if finishedEvent != nil {
			if _, err := InsertEventTx(ctx, tx, eventbus.TypeMatchFinished, finishedEvent); err != nil {
				return err
			}
		}
		return nil
	})
}

// emitMatchStarted appends a match.started fact to the transactional outbox in the
// same tx as the match going active, so the Super Admin mirror learns of a live
// match the instant it begins (not on the next 30s backfill). Best-effort payload:
// a marshal failure aborts the tx (the match wouldn't have started cleanly anyway).
func emitMatchStarted(ctx context.Context, tx pgx.Tx, matchPublicID, game string, bid int64) error {
	payload, err := json.Marshal(map[string]any{
		"match_id": matchPublicID,
		"game":     game,
		"bid":      bid,
	})
	if err != nil {
		return err
	}
	_, err = InsertEventTx(ctx, tx, eventbus.TypeMatchStarted, payload)
	return err
}

func (r *MatchRepo) ListActiveExpired(ctx context.Context, now time.Time, limit int) ([]string, error) {
	// Scope to goofspiel: this repo drives the goofspiel timeout sweeper, and the
	// mafia/monopoly services own their own rows in the shared `matches` table
	// (their repos filter by game too). Without this, the goofspiel sweeper claims
	// a mafia/monopoly match, runs it through the goofspiel engine, and fails
	// (ErrIllegalCard) on every tick forever.
	rows, err := r.db.Query(ctx,
		`SELECT public_id FROM matches
		 WHERE status='active' AND game='goofspiel' AND round_deadline IS NOT NULL AND round_deadline <= $1
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

func (r *MatchRepo) CancelWaiting(ctx context.Context, matchPublicID, creatorAgentPublicID string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE matches SET status='aborted', updated_at=now()
		 WHERE public_id=$1 AND status='waiting'
		   AND EXISTS (
		     SELECT 1 FROM match_players mp
		     JOIN agents ag ON ag.id = mp.agent_id
		     WHERE mp.match_id = matches.id AND mp.seat = 0 AND ag.public_id = $2
		   )`, matchPublicID, creatorAgentPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return match.ErrNotWaiting
	}
	return nil
}

// LoadEventsTimed is LoadEvents plus each event's wall-clock write time.
//
// Separate from LoadEvents on purpose: the plain events feed the fairness proof and
// the replay hash, both of which require gs.Event to be byte-identical between memory
// and a DB round-trip. Timing is presentation data for pacing, so it rides alongside.
func (r *MatchRepo) LoadEventsTimed(ctx context.Context, matchPublicID string) ([]match.TimedEvent, error) {
	rows, err := r.db.Query(ctx,
		`SELECT seq, type, payload, created_at FROM match_events
		 WHERE match_id = (SELECT id FROM matches WHERE public_id=$1)
		 ORDER BY seq ASC`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []match.TimedEvent
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
		out = append(out, match.TimedEvent{
			Event: gs.Event{Seq: seq, Type: gs.EventType(typ), Payload: p},
			At:    at,
		})
	}
	return out, rows.Err()
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

// ExtendDeadline pushes the current round's deadline out without touching state.
//
// Guarded on status='active' so a match that finished between the sweeper's read and this
// write cannot have a deadline resurrected onto it. Deliberately does NOT bump the state
// revision: an extension is not a game event — the board is unchanged, an agent is simply
// still thinking — and writing a revision for it would put a non-move into the replay and
// invalidate the OCC token a concurrent real move is holding.
func (r *MatchRepo) ExtendDeadline(ctx context.Context, matchPublicID string, deadline time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE matches SET round_deadline = $2, updated_at = now()
		  WHERE public_id = $1 AND status = 'active'`,
		matchPublicID, deadline)
	return err
}

// RecordMoveRejection notes that a seat submitted a move for this round and it was refused.
//
// Idempotent on (match, agent, round): one refusal answers the question tryExtend asks, and
// counting attempts would invite tuning a threshold that has no honest value — there is no
// number of refusals that means "still thinking". A repeat therefore keeps the FIRST reason,
// which is the one that first told the seat to stop.
//
// INSERT…SELECT against agents, so an unknown agent id writes nothing and reports no error.
func (r *MatchRepo) RecordMoveRejection(ctx context.Context, matchID, agentPublicID string, round int, reason string) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_move_rejections (match_id, agent_id, round, reason)
		 SELECT $1, a.id, $3, $4 FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id, round) DO NOTHING`,
		matchID, agentPublicID, round, reason)
	return err
}

// MoveRejected reports whether this seat has had a move refused for this round.
//
// Read on the deadline-sweep path, once per unsealed seat per expiry. The caller treats an
// error as "not rejected", so a lookup failure leaves the extension behaviour exactly as it
// was before this record existed.
func (r *MatchRepo) MoveRejected(ctx context.Context, matchID, agentPublicID string, round int) (bool, error) {
	var found bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (
		   SELECT 1 FROM agent_move_rejections j
		     JOIN agents a ON a.id = j.agent_id
		    WHERE j.match_id = $1 AND a.public_id = $2 AND j.round = $3)`,
		matchID, agentPublicID, round).Scan(&found)
	return found, err
}

// ── ready check ──────────────────────────────────────────────────────────────
//
// The write half of internal/readycheck. Added ALONGSIDE CreatePairedActive rather than
// replacing it: pairing must keep working at every commit, so the switch happens once the
// ready endpoint and the sweeper exist to drive this path. Until then these are unused.

// CreatePairedReadyCheck persists a paired table that has NOT started and whose stakes have
// NOT been escrowed.
//
// Three deliberate differences from CreatePairedActive, and each one is the point:
//
//   - status is 'ready_check', not 'active' — so no move is accepted (tryAct requires active)
//     and the table is not in the lobby (both lobby indexes are WHERE status = 'waiting').
//   - started_at and round_deadline stay NULL. The match has not begun; writing a start time
//     for a table nobody has agreed to play would make every latency and deadline derived
//     from it wrong.
//   - no match.started event. Emitting it here would tell every consumer — boards, traces,
//     the SDK — that a match began, and the whole point is that it has not.
func (r *MatchRepo) CreatePairedReadyCheck(ctx context.Context, in match.CreatePairedInput) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`INSERT INTO matches (public_id, game, status, mode, bot_policy, bid, rake_pct, total_rounds,
			     engine_version, prize_seed_commit, prize_seed, fairness_mode,
			     state, creator_owner_user_id)
			 VALUES ($1,$2,'ready_check',COALESCE(NULLIF($12,''),'competitive'),NULLIF($13,''),$3,$4,$5,$6,$7,$8,$9,$10::jsonb,
			     (SELECT id FROM users WHERE public_id=$11))
			 RETURNING id`,
			in.PublicID, in.Game, in.Bid, in.RakePct, in.TotalRounds,
			in.EngineVersion, in.Commit, in.Seed, in.FairnessMode,
			mustJSON(in.State), in.SeatA.OwnerPublicID, in.Mode, in.BotPolicy).Scan(&matchID)
		if err != nil {
			return err
		}
		if err := insertPlayer(ctx, tx, matchID, in.SeatA); err != nil {
			return err
		}
		if err := insertPlayer(ctx, tx, matchID, in.SeatB); err != nil {
			return err
		}
		// Events are the dealt opening state and are kept: the deal is already committed to
		// (prize_seed_commit), and re-dealing on start would break that commitment.
		return insertEvents(ctx, tx, matchID, in.Events)
	})
}

// MarkReady records that a seat has acknowledged. IDEMPOTENT on (match, agent).
//
// Idempotent because a retry is the same agent answering once. Without the guard a resent ack
// would refresh ready_at, and a seat that answered early would keep looking like it answered
// just now — which is exactly the signal used to explain why a table started when it did.
//
// Returns whether this call was the one that marked it, so a caller can tell a first ack from
// a duplicate without a second query.
func (r *MatchRepo) MarkReady(ctx context.Context, matchPublicID, agentPublicID string, at time.Time) (bool, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE match_players mp SET ready_at = $3
		   FROM matches m, agents a
		  WHERE mp.match_id = m.id AND mp.agent_id = a.id
		    AND m.public_id = $1 AND a.public_id = $2
		    AND m.status = 'ready_check'
		    AND mp.ready_at IS NULL`,
		matchPublicID, agentPublicID, at)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// ReadySeats reads a table's readiness for readycheck.Evaluate.
//
// Ordered by seat so a decision is reproducible: the sweeper's Drop list feeds requeues and
// refunds-that-never-happened, and a set that reorders between reads makes an incident
// impossible to reconstruct.
func (r *MatchRepo) ReadySeats(ctx context.Context, matchPublicID string) ([]match.ReadySeat, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id, u.public_id, mp.seat, mp.ready_at, mp.ready_asks, mp.ready_asked_at
		   FROM match_players mp
		   JOIN matches m ON m.id = mp.match_id
		   JOIN agents  a ON a.id = mp.agent_id
		   JOIN users   u ON u.id = mp.owner_user_id
		  WHERE m.public_id = $1
		  ORDER BY mp.seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []match.ReadySeat
	for rows.Next() {
		var s match.ReadySeat
		if err := rows.Scan(&s.AgentPublicID, &s.OwnerPublicID, &s.Seat,
			&s.ReadyAt, &s.Asks, &s.AskedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// RecordAsk notes that a seat was asked, so its window can be measured and its second chance
// counted. Bumps the count and resets the clock in one statement — two statements could leave
// a seat asked-but-untimed if the process died between them, and an untimed ask never expires.
func (r *MatchRepo) RecordAsk(ctx context.Context, matchPublicID, agentPublicID string, at time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE match_players mp SET ready_asks = mp.ready_asks + 1, ready_asked_at = $3
		   FROM matches m, agents a
		  WHERE mp.match_id = m.id AND mp.agent_id = a.id
		    AND m.public_id = $1 AND a.public_id = $2
		    AND m.status = 'ready_check'`,
		matchPublicID, agentPublicID, at)
	return err
}

// ActivateAfterReady flips a ready table to active once its stakes are escrowed.
//
// The caller escrows FIRST and calls this second. Ordering matters: if escrow succeeds and
// this fails, the caller refunds — the same shape CreatePaired already uses. The reverse
// order would start a staked match with nothing behind it.
//
// Guarded on status = 'ready_check' so two sweepers racing cannot both start the same table;
// the loser affects no rows and is told so.
func (r *MatchRepo) ActivateAfterReady(ctx context.Context, matchPublicID string, startsAt, deadline time.Time) (bool, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE matches
		    SET status = 'active', started_at = now(), starts_at = $2,
		        round_deadline = $3, round_deadline_base = $3
		  WHERE public_id = $1 AND status = 'ready_check'`,
		matchPublicID, startsAt, deadline)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// AbandonReadyCheck releases a table that can never start. Nothing is refunded because
// nothing was ever escrowed — which is the entire point of the ready check.
func (r *MatchRepo) AbandonReadyCheck(ctx context.Context, matchPublicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE matches SET status = 'aborted' WHERE public_id = $1 AND status = 'ready_check'`,
		matchPublicID)
	return err
}

// ReadyCheckMatches lists tables still collecting acknowledgements, oldest first.
//
// Oldest first because a table that has been waiting longest is closest to a decision —
// either it starts or someone is dropped — and serving newer tables ahead of it would let a
// busy arena starve the ones already holding agents.
//
// Bounded by limit so one sweep tick cannot stall on a backlog. Uses the partial index added
// in 0088; ready_check is a brief state, so this is a small set in practice.
func (r *MatchRepo) ReadyCheckMatches(ctx context.Context, limit int) ([]string, error) {
	if limit <= 0 {
		limit = 32
	}
	rows, err := r.db.Query(ctx,
		`SELECT public_id FROM matches
		  WHERE status = 'ready_check'
		  ORDER BY created_at
		  LIMIT $1`, limit)
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
