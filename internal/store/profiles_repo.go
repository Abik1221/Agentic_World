package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/profiles"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProfilesRepo is the pgx implementation of profiles.Repo. Headline stats are the
// season ratings aggregate; recent matches join the opponent + their season ELO.
type ProfilesRepo struct{ db *pgxpool.Pool }

func NewProfilesRepo(db *pgxpool.Pool) *ProfilesRepo { return &ProfilesRepo{db: db} }

var _ profiles.Repo = (*ProfilesRepo)(nil)

func (r *ProfilesRepo) AgentInfo(ctx context.Context, slug string) (profiles.Agent, error) {
	var a profiles.Agent
	err := r.db.QueryRow(ctx,
		`SELECT a.public_id, a.name, a.slug, COALESCE(u.x_handle, ''), a.status, a.verification_level
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.slug = $1`, slug).
		Scan(&a.PublicID, &a.Name, &a.Slug, &a.XHandle, &a.Status, &a.VerificationLevel)
	if errors.Is(err, pgx.ErrNoRows) {
		return profiles.Agent{}, httpx.ErrNotFound
	}
	return a, err
}

func (r *ProfilesRepo) Stats(ctx context.Context, agentPublicID string, season int) (profiles.Stats, error) {
	var st profiles.Stats
	err := r.db.QueryRow(ctx,
		`SELECT r.elo, r.wins, r.losses, r.ties, r.coins_earned, r.current_streak, r.best_streak
		 FROM ratings r JOIN agents a ON a.id = r.agent_id
		 WHERE a.public_id = $1 AND r.season = $2`, agentPublicID, season).
		Scan(&st.Elo, &st.Wins, &st.Losses, &st.Ties, &st.CoinsEarned, &st.CurrentStreak, &st.BestStreak)
	if errors.Is(err, pgx.ErrNoRows) {
		return profiles.Stats{Elo: 1200}, nil // unrated this season
	}
	return st, err
}

func (r *ProfilesRepo) RecentMatches(ctx context.Context, agentPublicID string, season, limit int) ([]profiles.RecentMatch, error) {
	rows, err := r.db.Query(ctx,
		`SELECT m.public_id, COALESCE(mp.coins_delta, 0), COALESCE(mp.final_score, 0),
		        COALESCE(opp_mp.final_score, 0), opp.public_id, COALESCE(re.elo, 1200), m.finished_at
		 FROM match_players mp
		 JOIN matches m       ON m.id = mp.match_id
		 JOIN match_players opp_mp ON opp_mp.match_id = m.id AND opp_mp.seat <> mp.seat
		 JOIN agents opp      ON opp.id = opp_mp.agent_id
		 JOIN agents me       ON me.id = mp.agent_id
		 LEFT JOIN ratings re ON re.agent_id = opp.id AND re.season = $2
		 WHERE me.public_id = $1 AND m.status = 'finished'
		 ORDER BY m.finished_at DESC NULLS LAST
		 LIMIT $3`, agentPublicID, season, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []profiles.RecentMatch
	for rows.Next() {
		var rm profiles.RecentMatch
		var finished *time.Time
		if err := rows.Scan(&rm.MatchID, &rm.CoinsDelta, &rm.YourScore, &rm.OppScore,
			&rm.Opponent, &rm.OpponentElo, &finished); err != nil {
			return nil, err
		}
		if finished != nil {
			rm.FinishedAt = *finished
		}
		out = append(out, rm)
	}
	return out, rows.Err()
}
