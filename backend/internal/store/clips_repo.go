package store

import (
	"context"
	"errors"

	"github.com/agent-arena/arena/internal/clips"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClipsRepo is the pgx implementation of clips.Repo.
type ClipsRepo struct{ db *pgxpool.Pool }

func NewClipsRepo(db *pgxpool.Pool) *ClipsRepo { return &ClipsRepo{db: db} }

var _ clips.Repo = (*ClipsRepo)(nil)

func (r *ClipsRepo) CreateClips(ctx context.Context, matchPublicID string, in []clips.NewClip) ([]clips.Created, error) {
	var out []clips.Created
	for _, c := range in {
		var pub string
		err := r.db.QueryRow(ctx,
			`INSERT INTO clips (public_id, match_id, trigger, round_seq)
			 SELECT $1, m.id, $3, $4 FROM matches m WHERE m.public_id = $2
			 ON CONFLICT (match_id, trigger) DO NOTHING
			 RETURNING public_id`,
			c.PublicID, matchPublicID, c.Trigger, c.RoundSeq).Scan(&pub)
		if errors.Is(err, pgx.ErrNoRows) {
			continue // already existed (idempotent) or match missing
		}
		if err != nil {
			return nil, err
		}
		out = append(out, clips.Created{PublicID: pub, Trigger: c.Trigger, RoundSeq: c.RoundSeq})
	}
	return out, nil
}

func (r *ClipsRepo) SetAsset(ctx context.Context, clipPublicID, assetURL string) error {
	_, err := r.db.Exec(ctx, `UPDATE clips SET asset_url = $2 WHERE public_id = $1`, clipPublicID, assetURL)
	return err
}

func (r *ClipsRepo) Trending(ctx context.Context, limit, offset int) ([]clips.ClipView, error) {
	rows, err := r.db.Query(ctx,
		`SELECT c.public_id, m.public_id, c.trigger, COALESCE(c.round_seq, 0),
		        COALESCE(c.asset_url, ''), c.share_count, c.created_at
		 FROM clips c JOIN matches m ON m.id = c.match_id
		 WHERE c.asset_url IS NOT NULL
		 ORDER BY c.share_count DESC, c.created_at DESC
		 LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []clips.ClipView
	for rows.Next() {
		var c clips.ClipView
		if err := rows.Scan(&c.PublicID, &c.MatchID, &c.Trigger, &c.RoundSeq,
			&c.AssetURL, &c.ShareCount, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
