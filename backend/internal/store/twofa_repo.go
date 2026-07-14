package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/twofa"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TwoFARepo persists per-user TOTP enrollment on the users row.
type TwoFARepo struct{ db *pgxpool.Pool }

func NewTwoFARepo(db *pgxpool.Pool) *TwoFARepo { return &TwoFARepo{db: db} }

var _ twofa.Repo = (*TwoFARepo)(nil)

// SaveSecret stores a freshly-generated (encrypted) secret and resets enrollment to
// not-yet-enabled, so re-running setup replaces any half-finished attempt.
func (r *TwoFARepo) SaveSecret(ctx context.Context, userPublicID string, secretEnc []byte) error {
	ct, err := r.db.Exec(ctx,
		`UPDATE users SET totp_secret_enc = $2, totp_enabled = FALSE, totp_confirmed_at = NULL
		 WHERE public_id = $1`, userPublicID, secretEnc)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return httpx.ErrNotFound
	}
	return nil
}

func (r *TwoFARepo) Load(ctx context.Context, userPublicID string) ([]byte, bool, error) {
	var enc []byte
	var enabled bool
	err := r.db.QueryRow(ctx,
		`SELECT totp_secret_enc, totp_enabled FROM users WHERE public_id = $1`, userPublicID).
		Scan(&enc, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, httpx.ErrNotFound
	}
	return enc, enabled, err
}

func (r *TwoFARepo) SetEnabled(ctx context.Context, userPublicID string, at time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET totp_enabled = TRUE, totp_confirmed_at = $2 WHERE public_id = $1`,
		userPublicID, at)
	return err
}

func (r *TwoFARepo) Clear(ctx context.Context, userPublicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET totp_secret_enc = NULL, totp_enabled = FALSE, totp_confirmed_at = NULL
		 WHERE public_id = $1`, userPublicID)
	return err
}
