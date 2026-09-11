package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	mf "github.com/agent-arena/arena/internal/engine/mafia"
	eventbus "github.com/agent-arena/arena/internal/events"
	"github.com/agent-arena/arena/internal/mafia"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MafiaRepo persists Mafia matches using shared match tables + mafia_seats.
type MafiaRepo struct{ db *pgxpool.Pool }

func NewMafiaRepo(db *pgxpool.Pool) *MafiaRepo { return &MafiaRepo{db: db} }

var _ mafia.Repo = (*MafiaRepo)(nil)

func (r *MafiaRepo) CreateWaiting(ctx context.Context, in mafia.CreateMatchInput) (mafia.Match, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return mafia.Match{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var matchID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
		     engine_version, prize_seed_commit, prize_seed, fairness_mode, creator_owner_user_id)
		 VALUES ($1,'mafia','waiting',$2,$3,$4,$5,$6,$7,'shuffled',
		         (SELECT id FROM users WHERE public_id=$8))
		 RETURNING id`,
		in.PublicID, in.EntryFee, in.RakePct, mf.RosterSize, mf.Version, in.Commit, in.Seed, in.Creator.OwnerPublicID).
		Scan(&matchID)
	if err != nil {
		return mafia.Match{}, err
	}
	if err := insertMafiaPlayer(ctx, tx, matchID, in.Creator); err != nil {
		return mafia.Match{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return mafia.Match{}, err
	}
	return mafia.Match{
		PublicID: in.PublicID, Title: in.Title, Status: mafia.StatusWaiting,
		EntryFee: in.EntryFee, RakePct: in.RakePct, EngineVersion: mf.Version,
		Commit: in.Commit, Seed: in.Seed, Players: []mafia.Player{in.Creator},
	}, nil
}

func (r *MafiaRepo) ListWaiting(ctx context.Context, entryFee int64, excludeOwnerPublicID string, limit int) ([]mafia.LobbyItem, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.bid,
		        (SELECT COUNT(*) FROM match_players mp2 WHERE mp2.match_id = m.id),
		        ag.public_id, m.created_at
		 FROM matches m
		 JOIN match_players mp ON mp.match_id = m.id AND mp.seat = 1
		 JOIN agents ag ON ag.id = mp.agent_id
		 WHERE m.status = 'waiting' AND m.game = 'mafia'
		   AND ($1 <= 0 OR m.bid = $1)
		   AND m.creator_owner_user_id <> COALESCE((SELECT id FROM users WHERE public_id = $2), 0)
		 ORDER BY m.created_at DESC
		 LIMIT $3`, entryFee, excludeOwnerPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mafia.LobbyItem
	for rows.Next() {
		var it mafia.LobbyItem
		if err := rows.Scan(&it.PublicID, &it.EntryFee, &it.SeatsFilled, &it.CreatorAgentPublicID, &it.CreatedAt); err != nil {
			return nil, err
		}
		it.SeatsTotal = mf.RosterSize
		out = append(out, it)
	}
	return out, rows.Err()
}

func (r *MafiaRepo) Get(ctx context.Context, matchPublicID string) (mafia.Match, error) {
	var m mafia.Match
	var stateBytes []byte
	var deadline *time.Time
	var startsAt *time.Time
	var seed []byte
	err := r.db.QueryRow(ctx,
		`SELECT m.public_id, m.status, m.bid, m.rake_pct, m.engine_version,
		        m.prize_seed_commit, m.prize_seed, COALESCE(m.state, '{}'::jsonb),
		        m.round_deadline, m.starts_at, COALESCE(m.replay_hash, '')
		 FROM matches m WHERE m.public_id = $1 AND m.game = 'mafia'`, matchPublicID).
		Scan(&m.PublicID, &m.Status, &m.EntryFee, &m.RakePct, &m.EngineVersion,
			&m.Commit, &seed, &stateBytes, &deadline, &startsAt, &m.ReplayHash)
	if err != nil {
		return mafia.Match{}, err
	}
	m.Title = "Mafia AI Arena"
	m.Seed = seed
	m.RoundDeadline = deadline
	m.StartsAt = startsAt
	if len(stateBytes) > 0 {
		// Authoritative state: a decode error must fail loudly, not silently load a
		// zero-value (phantom empty) match that Act/view would then operate on.
		if err := json.Unmarshal(stateBytes, &m.State); err != nil {
			return mafia.Match{}, err
		}
	}
	players, err := r.loadPlayers(ctx, matchPublicID)
	if err != nil {
		return mafia.Match{}, err
	}
	m.Players = players
	if m.State.Winner != "" {
		m.WinnerTeam = m.State.Winner
	}
	return m, nil
}

func (r *MafiaRepo) loadPlayers(ctx context.Context, matchPublicID string) ([]mafia.Player, error) {
	rows, err := r.db.Query(ctx,
		`SELECT mp.seat, ag.public_id, u.public_id,
		        COALESCE(ms.role, ''), COALESCE(ms.team, ''), COALESCE(ms.alive, true),
		        COALESCE(ms.coins_delta, mp.coins_delta, 0),
		        COALESCE(ag.kind, '') = 'house',
		        COALESCE(ag.name, ''), COALESCE(u.display_name, ''),
		        -- The AGENT's face first, its owner's only as a fallback.
		        --
		        -- This read u.avatar_url alone, which is the owner's PROFILE picture. Two
		        -- consequences, both visible on every table:
		        --
		        --   * House bots never had a face. Migration 0068 gave each one an avatar on
		        --     agents.avatar_url, and nothing read that column — they all belong to
		        --     usr_system, which has no profile picture, so every seat fell back to a
		        --     blank SVG. The avatars have been sitting there unread since.
		        --   * A developer with several agents saw the same picture on all of them,
		        --     because it was never the agent's identity being shown.
		        --
		        -- A seat is an AGENT, so it shows the agent's face. Falling back to the owner
		        -- keeps today's behaviour for anyone who set a profile picture and never set
		        -- one per agent.
		        COALESCE(NULLIF(ag.avatar_url, ''), NULLIF(u.avatar_url, ''), '')
		 FROM match_players mp
		 JOIN agents ag ON ag.id = mp.agent_id
		 JOIN users u ON u.id = mp.owner_user_id
		 LEFT JOIN mafia_seats ms ON ms.match_id = mp.match_id AND ms.seat = mp.seat
		 WHERE mp.match_id = (SELECT id FROM matches WHERE public_id = $1)
		 ORDER BY mp.seat`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mafia.Player
	for rows.Next() {
		var p mafia.Player
		if err := rows.Scan(&p.Seat, &p.AgentPublicID, &p.OwnerPublicID, &p.Role, &p.Team, &p.Alive, &p.CoinsDelta,
			&p.IsHouse, &p.Name, &p.OwnerName, &p.AvatarURL); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *MafiaRepo) JoinSeat(ctx context.Context, matchPublicID string, p mafia.Player) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`SELECT id FROM matches WHERE public_id=$1 AND status='waiting' AND game='mafia' FOR UPDATE`,
			matchPublicID).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return mafia.ErrNotWaiting
		}
		if err != nil {
			return err
		}
		return insertMafiaPlayer(ctx, tx, matchID, p)
	})
}

// Start activates a full table. startsAt is the ABSOLUTE instant play begins and is persisted
// so every surface counts to the same moment; the caller opens the first phase window at it,
// not at now, so the countdown does not eat the first phase's thinking time.
func (r *MafiaRepo) Start(ctx context.Context, matchPublicID string, roles map[int]string, state mf.State, startsAt, deadline time.Time, events []mf.Event) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET status='active', state=$2::jsonb, round_deadline=$3, round_deadline_base=$3, starts_at=$4, started_at=now(), updated_at=now()
			 WHERE public_id=$1 AND status='waiting' AND game='mafia' RETURNING id`,
			matchPublicID, mustJSON(state), deadline, startsAt).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return mafia.ErrNotWaiting
		}
		if err != nil {
			return err
		}
		// Read all seats fully BEFORE issuing the per-seat INSERTs: pgx allows only
		// one in-flight query per connection, so Exec-ing while this cursor is still
		// open fails with "conn busy". Collect first, then write.
		type seatRow struct {
			seat             int
			agentID, ownerID int64
		}
		rows, err := tx.Query(ctx,
			`SELECT mp.seat, mp.agent_id, mp.owner_user_id FROM match_players mp WHERE mp.match_id=$1`, matchID)
		if err != nil {
			return err
		}
		var seats []seatRow
		for rows.Next() {
			var sr seatRow
			if err := rows.Scan(&sr.seat, &sr.agentID, &sr.ownerID); err != nil {
				rows.Close()
				return err
			}
			seats = append(seats, sr)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, sr := range seats {
			role := roles[sr.seat]
			if _, err := tx.Exec(ctx,
				`INSERT INTO mafia_seats (match_id, seat, agent_id, owner_user_id, role, team, alive)
				 VALUES ($1,$2,$3,$4,$5,$6,true)
				 ON CONFLICT (match_id, seat) DO UPDATE SET role=$5, team=$6, alive=true`,
				matchID, sr.seat, sr.agentID, sr.ownerID, role, mf.TeamOf(role)); err != nil {
				return err
			}
		}
		return insertMafiaEvents(ctx, tx, matchID, events)
	})
}

// MarkUnrated excludes a match from ranked statistics. Scoped to game='mafia' and to
// tables that have not finished yet, so it can only ever be applied at start time by
// the path that seated the house bots.
func (r *MafiaRepo) MarkUnrated(ctx context.Context, matchPublicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE matches SET rated = false, updated_at = now()
		 WHERE public_id = $1 AND game = 'mafia' AND finished_at IS NULL`, matchPublicID)
	return err
}

func (r *MafiaRepo) Advance(ctx context.Context, matchPublicID string, state mf.State, deadline *time.Time, alive map[int]bool, events []mf.Event) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET state=$2::jsonb, round_deadline=$3, round_deadline_base=$3, updated_at=now()
			 WHERE public_id=$1 AND status='active' AND game='mafia' RETURNING id`,
			matchPublicID, mustJSON(state), deadline).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return mafia.ErrNotActive
		}
		if err != nil {
			return err
		}
		for seat, ok := range alive {
			if _, err := tx.Exec(ctx,
				`UPDATE mafia_seats SET alive=$3 WHERE match_id=$1 AND seat=$2`, matchID, seat, ok); err != nil {
				return err
			}
		}
		return insertMafiaEvents(ctx, tx, matchID, events)
	})
}

func (r *MafiaRepo) Finish(ctx context.Context, matchPublicID string, state mf.State, winnerTeam, replayHash string, players []mafia.Player, events []mf.Event) error {
	return r.tx(ctx, func(tx pgx.Tx) error {
		var matchID int64
		err := tx.QueryRow(ctx,
			`UPDATE matches SET status='finished', state=$2::jsonb, round_deadline=NULL,
			     finished_at=now(), updated_at=now(), replay_hash=$3,
			     winner_agent_id = (
			       SELECT agent_id FROM mafia_seats ms
			       WHERE ms.match_id = matches.id AND ms.team = $4 AND ms.alive = true
			       LIMIT 1
			     )
			 WHERE public_id=$1 AND status='active' AND game='mafia' RETURNING id`,
			matchPublicID, mustJSON(state), replayHash, winnerTeam).Scan(&matchID)
		if errors.Is(err, pgx.ErrNoRows) {
			return mafia.ErrNotActive
		}
		if err != nil {
			return err
		}
		for _, p := range players {
			if _, err := tx.Exec(ctx,
				`UPDATE mafia_seats SET alive=$3, coins_delta=$4, role=$5, team=$6
				 WHERE match_id=$1 AND seat=$2`,
				matchID, p.Seat, p.Alive, p.CoinsDelta, p.Role, p.Team); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx,
				`UPDATE match_players SET coins_delta=$3 WHERE match_id=$1 AND seat=$2`,
				matchID, p.Seat, p.CoinsDelta); err != nil {
				return err
			}
		}
		if err := insertMafiaEvents(ctx, tx, matchID, events); err != nil {
			return err
		}
		// Same match.finished fact goofspiel emits, so Eye can audit the money path.
		var bid int64
		var rakePct int
		var winner string
		if err := tx.QueryRow(ctx,
			`SELECT bid, rake_pct, COALESCE((SELECT public_id FROM agents WHERE id = m.winner_agent_id), '')
			 FROM matches m WHERE m.id = $1`, matchID).Scan(&bid, &rakePct, &winner); err != nil {
			return err
		}
		seats := make([]map[string]any, 0, len(players))
		for _, p := range players {
			seats = append(seats, map[string]any{
				"agent_id": p.AgentPublicID, "seat": p.Seat, "coins_delta": p.CoinsDelta,
			})
		}
		payload, err := json.Marshal(map[string]any{
			"match_id": matchPublicID, "game": "mafia", "winner_agent": winner,
			"winner_team": winnerTeam, "bid": bid, "rake_pct": rakePct,
			"pool": bid * int64(len(players)), "seats": seats,
		})
		if err != nil {
			return err
		}
		_, err = InsertEventTx(ctx, tx, eventbus.TypeMatchFinished, payload)
		return err
	})
}

func (r *MafiaRepo) ListActiveExpired(ctx context.Context, game string, now time.Time, limit int) ([]string, error) {
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

// LoadEventsTimed is LoadEvents plus each event's wall-clock write time.
//
// Kept SEPARATE from LoadEvents on purpose: the plain events feed game logic and the
// replay hash, and mafia.Event must stay byte-identical between memory and a DB
// round-trip (see the hash-stability guarantee). Timing is presentation data for
// replay pacing, so it travels alongside rather than inside the event.
func (r *MafiaRepo) LoadEventsTimed(ctx context.Context, matchPublicID string) ([]mafia.TimedEvent, error) {
	rows, err := r.db.Query(ctx,
		`SELECT seq, type, payload, created_at FROM match_events
		 WHERE match_id = (SELECT id FROM matches WHERE public_id=$1)
		 ORDER BY seq ASC`, matchPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mafia.TimedEvent
	for rows.Next() {
		var seq int
		var typ string
		var payload []byte
		var at time.Time
		if err := rows.Scan(&seq, &typ, &payload, &at); err != nil {
			return nil, err
		}
		p, err := mf.DecodePayload(mf.EventType(typ), payload)
		if err != nil {
			return nil, err
		}
		out = append(out, mafia.TimedEvent{
			Event: mf.Event{Seq: seq, Type: mf.EventType(typ), Payload: p},
			At:    at,
		})
	}
	return out, rows.Err()
}

func (r *MafiaRepo) LoadEvents(ctx context.Context, matchPublicID string, afterSeq int) ([]mf.Event, error) {
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
	var out []mf.Event
	for rows.Next() {
		var seq int
		var typ string
		var payload []byte
		if err := rows.Scan(&seq, &typ, &payload); err != nil {
			return nil, err
		}
		// Restore the concrete payload type (not a generic map) so consumers like
		// BuildView's per-seat redaction can inspect payload fields after the DB
		// round-trip.
		p, err := mf.DecodePayload(mf.EventType(typ), payload)
		if err != nil {
			return nil, err
		}
		out = append(out, mf.Event{Seq: seq, Type: mf.EventType(typ), Payload: p})
	}
	return out, rows.Err()
}

func (r *MafiaRepo) LiveMatches(ctx context.Context) ([]mafia.LiveMatch, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, m.status,
		        COALESCE((m.state->>'day')::int, 0),
		        COALESCE(m.state->>'phase', 'night'),
		        COALESCE(m.state->>'winner', ''),
		        (SELECT COUNT(*) FROM mafia_seats ms WHERE ms.match_id = m.id AND ms.alive = true),
		        (SELECT array_agg(ag.public_id ORDER BY mp.seat)
		           FROM match_players mp JOIN agents ag ON ag.id = mp.agent_id
		          WHERE mp.match_id = m.id),
		        COALESCE(m.bid, 0),
		        (SELECT COUNT(*) FROM match_players mp2 WHERE mp2.match_id = m.id)
		 FROM matches m
		 WHERE m.game = 'mafia' AND m.status IN ('active','waiting')
		 ORDER BY m.started_at DESC NULLS LAST, m.created_at DESC
		 LIMIT 20`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []mafia.LiveMatch
	for rows.Next() {
		var lm mafia.LiveMatch
		var agents []string
		var status string
		var seated int
		if err := rows.Scan(&lm.MatchID, &status, &lm.Day, &lm.Phase, &lm.Winner, &lm.Alive, &agents,
			&lm.EntryFee, &seated); err != nil {
			return nil, err
		}
		lm.Title = "Mafia AI Arena"
		lm.Agents = agents
		lm.Players = mf.RosterSize
		// EntryFee lets a spectator surface tell a STAKED table from a free practice
		// one. Without it, a table that is one developer's agent plus eleven house bots
		// at zero stakes was published on the public Live Arena as an ordinary 12-agent
		// staked match — the same deception the scripted demo table was removed for.
		if status == "waiting" {
			lm.Phase = "waiting"
			// A waiting table has not dealt yet, so "alive" is meaningless: report the
			// seats actually taken. It previously claimed the full roster was alive, so
			// a 3-of-12 lobby rendered as "0/12 alive".
			lm.Alive = seated
		}
		out = append(out, lm)
	}
	return out, rows.Err()
}

func (r *MafiaRepo) CancelWaiting(ctx context.Context, matchPublicID, creatorAgentPublicID string) error {
	tag, err := r.db.Exec(ctx,
		`UPDATE matches SET status='aborted', updated_at=now()
		 WHERE public_id=$1 AND status='waiting' AND game='mafia'
		   AND EXISTS (
		     SELECT 1 FROM match_players mp
		     JOIN agents ag ON ag.id = mp.agent_id
		     WHERE mp.match_id = matches.id AND mp.seat = 1 AND ag.public_id = $2
		   )`, matchPublicID, creatorAgentPublicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return mafia.ErrNotWaiting
	}
	return nil
}

func (r *MafiaRepo) ExpireStaleWaiting(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	tag, err := r.db.Exec(ctx,
		`UPDATE matches SET status='aborted', updated_at=now()
		 WHERE id IN (
		   SELECT id FROM matches
		   WHERE game='mafia' AND status='waiting' AND created_at <= $1
		   ORDER BY created_at ASC LIMIT $2
		 )`, cutoff, limit)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

func (r *MafiaRepo) tx(ctx context.Context, fn func(pgx.Tx) error) error {
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

func insertMafiaPlayer(ctx context.Context, tx pgx.Tx, matchID int64, p mafia.Player) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat)
		 VALUES ($1, (SELECT id FROM agents WHERE public_id=$2),
		         (SELECT id FROM users WHERE public_id=$3), $4)`,
		matchID, p.AgentPublicID, p.OwnerPublicID, p.Seat)
	return err
}

func insertMafiaEvents(ctx context.Context, tx pgx.Tx, matchID int64, events []mf.Event) error {
	for _, ev := range events {
		b, err := json.Marshal(ev.Payload)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO match_events (match_id, seq, type, payload) VALUES ($1,$2,$3,$4::jsonb)`,
			matchID, ev.Seq, string(ev.Type), string(b)); err != nil {
			if isUniqueViolation(err) {
				// A racing writer already wrote this (match_id, seq) — surface it as a
				// concurrency conflict so a lockless caller can retry/no-op (OCC).
				return mafia.ErrConcurrentUpdate
			}
			return err
		}
	}
	return nil
}

// AgentSigningKey — see mafia.Repo. Reads the shared agents.signing_pubkey.
func (r *MafiaRepo) AgentSigningKey(ctx context.Context, agentPublicID string) (string, error) {
	return agentSigningKey(ctx, r.db, agentPublicID)
}

// RecordMoveSignature — see mafia.Repo. Persists a per-move authorship proof.
func (r *MafiaRepo) RecordMoveSignature(ctx context.Context, matchPublicID string, seq, seat int, action, signature, pubkey string) error {
	return recordGameMoveSig(ctx, r.db, "mafia", matchPublicID, seq, seat, action, signature, pubkey)
}

// LoadMoveSignatures — see mafia.Repo. Returns all proofs for replay re-verify.
func (r *MafiaRepo) LoadMoveSignatures(ctx context.Context, matchPublicID string) ([]mafia.MoveSig, error) {
	rows, err := loadGameMoveSigs(ctx, r.db, matchPublicID)
	if err != nil {
		return nil, err
	}
	out := make([]mafia.MoveSig, len(rows))
	for i, s := range rows {
		out[i] = mafia.MoveSig{Seq: s.Seq, Seat: s.Seat, Action: s.Action, Signature: s.Signature, Pubkey: s.Pubkey}
	}
	return out, nil
}
