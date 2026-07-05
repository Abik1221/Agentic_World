package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/social"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SocialRepo is the pgx implementation of social.Repo: follows + notifications.
type SocialRepo struct{ db *pgxpool.Pool }

func NewSocialRepo(db *pgxpool.Pool) *SocialRepo { return &SocialRepo{db: db} }

var _ social.Repo = (*SocialRepo)(nil)

func (r *SocialRepo) Follow(ctx context.Context, userPublicID, agentPublicID string) error {
	var agentID int64
	err := r.db.QueryRow(ctx, `SELECT id FROM agents WHERE public_id = $1`, agentPublicID).Scan(&agentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.ErrNotFound
	}
	if err != nil {
		return err
	}
	_, err = r.db.Exec(ctx,
		`INSERT INTO follows (user_id, agent_id)
		 SELECT u.id, $2 FROM users u WHERE u.public_id = $1
		 ON CONFLICT DO NOTHING`, userPublicID, agentID)
	return err
}

func (r *SocialRepo) Unfollow(ctx context.Context, userPublicID, agentPublicID string) error {
	_, err := r.db.Exec(ctx,
		`DELETE FROM follows
		 WHERE user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND agent_id = (SELECT id FROM agents WHERE public_id = $2)`,
		userPublicID, agentPublicID)
	return err
}

func (r *SocialRepo) MatchParticipants(ctx context.Context, matchPublicID string) ([]social.Participant, error) {
	rows, err := r.db.Query(ctx,
		`SELECT ag.public_id, u.public_id, COALESCE(mp.coins_delta, 0)
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
	var out []social.Participant
	for rows.Next() {
		var p social.Participant
		if err := rows.Scan(&p.AgentPublicID, &p.OwnerPublicID, &p.CoinsDelta); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *SocialRepo) FollowerUserIDs(ctx context.Context, agentPublicID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT u.public_id
		 FROM follows f
		 JOIN users  u  ON u.id = f.user_id
		 JOIN agents a  ON a.id = f.agent_id
		 WHERE a.public_id = $1`, agentPublicID)
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

func (r *SocialRepo) InsertNotification(ctx context.Context, recipientUserPublicID, kind, ref string, payload []byte) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO notifications (recipient_user_id, kind, ref, payload)
		 SELECT u.id, $2, $3, $4::jsonb FROM users u WHERE u.public_id = $1
		 ON CONFLICT (recipient_user_id, kind, ref) DO NOTHING`,
		recipientUserPublicID, kind, ref, string(payload))
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}
