package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RefreshRepo is the pgx implementation of auth.RefreshRepo (refresh_tokens table,
// migration 0055). The raw secret is never stored — only its SHA-256 in token_hash.
type RefreshRepo struct{ db *pgxpool.Pool }

func NewRefreshRepo(db *pgxpool.Pool) *RefreshRepo { return &RefreshRepo{db: db} }

var _ auth.RefreshRepo = (*RefreshRepo)(nil)

func (r *RefreshRepo) Create(ctx context.Context, row auth.RefreshRow) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO refresh_tokens (id, user_public_id, family_id, token_hash, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		row.ID, row.UserPublicID, row.FamilyID, row.TokenHash, row.ExpiresAt)
	return err
}

func (r *RefreshRepo) Get(ctx context.Context, id string) (auth.RefreshRow, error) {
	var row auth.RefreshRow
	var used, revoked *time.Time
	err := r.db.QueryRow(ctx,
		`SELECT id, user_public_id, family_id, token_hash, expires_at, used_at, revoked_at
		 FROM refresh_tokens WHERE id = $1`, id).
		Scan(&row.ID, &row.UserPublicID, &row.FamilyID, &row.TokenHash, &row.ExpiresAt, &used, &revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.RefreshRow{}, auth.ErrRefreshInvalid
	}
	if err != nil {
		return auth.RefreshRow{}, err
	}
	row.UsedAt, row.RevokedAt = used, revoked
	return row, nil
}

func (r *RefreshRepo) MarkUsed(ctx context.Context, id string, at time.Time) error {
	// Single-use: only set used_at if it isn't already (guards a rotation race).
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET used_at = $2 WHERE id = $1 AND used_at IS NULL`, id, at)
	return err
}

func (r *RefreshRepo) RevokeFamily(ctx context.Context, familyID string, at time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = $2 WHERE family_id = $1 AND revoked_at IS NULL`, familyID, at)
	return err
}
