package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/tournament"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TournamentRepo is the pgx implementation of tournament.Repo.
type TournamentRepo struct{ db *pgxpool.Pool }

func NewTournamentRepo(db *pgxpool.Pool) *TournamentRepo { return &TournamentRepo{db: db} }

var _ tournament.Repo = (*TournamentRepo)(nil)

func (r *TournamentRepo) Create(ctx context.Context, in tournament.CreateInput) (string, error) {
	var pub string
	err := r.db.QueryRow(ctx,
		`INSERT INTO tournaments (public_id, name, sponsor, prize_pool)
		 VALUES ($1, $2, $3, $4) RETURNING public_id`,
		in.PublicID, in.Name, nullString(in.Sponsor), in.PrizePool).Scan(&pub)
	return pub, err
}

func (r *TournamentRepo) Get(ctx context.Context, publicID string) (tournament.Tournament, error) {
	var t tournament.Tournament
	err := r.db.QueryRow(ctx,
		`SELECT t.public_id, t.name, COALESCE(t.sponsor, ''), t.prize_pool, t.status,
		        COALESCE(wa.public_id, ''),
		        (SELECT COUNT(*) FROM tournament_entries te WHERE te.tournament_id = t.id)
		 FROM tournaments t
		 LEFT JOIN agents wa ON wa.id = t.winner_agent_id
		 WHERE t.public_id = $1`, publicID).
		Scan(&t.PublicID, &t.Name, &t.Sponsor, &t.PrizePool, &t.Status, &t.Winner, &t.Entries)
	if errors.Is(err, pgx.ErrNoRows) {
		return tournament.Tournament{}, httpx.ErrNotFound
	}
	return t, err
}

func (r *TournamentRepo) Enter(ctx context.Context, publicID, agentPublicID string) error {
	var status string
	err := r.db.QueryRow(ctx, `SELECT status FROM tournaments WHERE public_id = $1`, publicID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "open" {
		return tournament.ErrClosed
	}
	_, err = r.db.Exec(ctx,
		`INSERT INTO tournament_entries (tournament_id, agent_id)
		 SELECT t.id, a.id FROM tournaments t, agents a
		 WHERE t.public_id = $1 AND a.public_id = $2
		 ON CONFLICT DO NOTHING`, publicID, agentPublicID)
	return err
}

func (r *TournamentRepo) Eligible(ctx context.Context, agentPublicID string) (bool, string, error) {
	var level string
	var flagged bool
	err := r.db.QueryRow(ctx,
		`SELECT a.verification_level,
		        EXISTS (SELECT 1 FROM fraud_flags f WHERE f.agent_id = a.id AND f.active)
		 FROM agents a WHERE a.public_id = $1`, agentPublicID).Scan(&level, &flagged)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", httpx.ErrNotFound
	}
	if err != nil {
		return false, "", err
	}
	if flagged {
		return false, "agent has an active fraud flag", nil
	}
	if level != "tournament_ready" {
		return false, "agent is not tournament_ready (level=" + level + ")", nil
	}
	return true, "", nil
}

func (r *TournamentRepo) Finalize(ctx context.Context, publicID, winnerAgentPublicID string) (tournament.Tournament, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return tournament.Tournament{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id, pool int64
	var status string
	var winnerID *int64
	err = tx.QueryRow(ctx,
		`SELECT id, prize_pool, status, winner_agent_id FROM tournaments WHERE public_id = $1 FOR UPDATE`,
		publicID).Scan(&id, &pool, &status, &winnerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return tournament.Tournament{}, false, httpx.ErrNotFound
	}
	if err != nil {
		return tournament.Tournament{}, false, err
	}

	t := tournament.Tournament{PublicID: publicID, PrizePool: pool, Status: "finished"}
	firstTime := status != "finished"

	if firstTime {
		if _, err := tx.Exec(ctx,
			`UPDATE tournaments SET status = 'finished', finished_at = now(),
			        winner_agent_id = (SELECT id FROM agents WHERE public_id = $2)
			 WHERE public_id = $1`, publicID, winnerAgentPublicID); err != nil {
			return tournament.Tournament{}, false, err
		}
		t.Winner = winnerAgentPublicID
	} else if winnerID != nil {
		// Already finished: resolve the recorded champion.
		if err := tx.QueryRow(ctx, `SELECT public_id FROM agents WHERE id = $1`, *winnerID).Scan(&t.Winner); err != nil {
			return tournament.Tournament{}, false, err
		}
	}
	return t, firstTime, tx.Commit(ctx)
}
