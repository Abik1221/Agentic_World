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

// FollowState answers both halves in ONE round trip: does this user follow the agent,
// and how many followers does the agent have.
//
// One query rather than two because the button and the count are rendered together — two
// queries can straddle a concurrent follow and produce "Following" beside a count that
// does not include you, which reads as a bug in the count.
//
// userPublicID may be empty (a signed-out viewer): the count is still public, and the
// relationship is simply false.
func (r *SocialRepo) FollowState(ctx context.Context, userPublicID, agentPublicID string) (bool, int, error) {
	var following bool
	var followers int
	err := r.db.QueryRow(ctx,
		`SELECT
		   EXISTS (
		     SELECT 1 FROM follows f
		      WHERE f.agent_id = a.id
		        AND f.user_id = (SELECT id FROM users WHERE public_id = $2)
		   ),
		   (SELECT COUNT(*) FROM follows f2 WHERE f2.agent_id = a.id)
		 FROM agents a WHERE a.public_id = $1`,
		agentPublicID, userPublicID).Scan(&following, &followers)
	if errors.Is(err, pgx.ErrNoRows) {
		// No such agent. Not an error for a read of this shape — the caller renders an
		// empty state, and 404-ing a follower count would break a profile page that is
		// otherwise fine.
		return false, 0, nil
	}
	return following, followers, err
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
	body := string(payload)
	if body == "" {
		body = "{}"
	}
	ct, err := r.db.Exec(ctx,
		`INSERT INTO notifications (recipient_user_id, kind, ref, payload)
		 SELECT u.id, $2, $3, $4::jsonb FROM users u WHERE u.public_id = $1
		 ON CONFLICT (recipient_user_id, kind, ref) DO NOTHING`,
		recipientUserPublicID, kind, ref, body)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

func (r *SocialRepo) ListNotifications(ctx context.Context, userPublicID string, limit int) ([]social.Notification, error) {
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	rows, err := r.db.Query(ctx,
		`SELECT n.kind, n.ref, n.payload, n.read_at IS NOT NULL, n.created_at
		 FROM notifications n JOIN users u ON u.id = n.recipient_user_id
		 WHERE u.public_id = $1
		 ORDER BY n.created_at DESC LIMIT $2`, userPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []social.Notification
	for rows.Next() {
		var n social.Notification
		if err := rows.Scan(&n.Kind, &n.Ref, &n.Payload, &n.Read, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (r *SocialRepo) MarkAllRead(ctx context.Context, userPublicID string) (int, error) {
	ct, err := r.db.Exec(ctx,
		`UPDATE notifications SET read_at = now()
		 WHERE read_at IS NULL
		   AND recipient_user_id = (SELECT id FROM users WHERE public_id = $1)`, userPublicID)
	if err != nil {
		return 0, err
	}
	return int(ct.RowsAffected()), nil
}
