package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/walletverify"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WalletVerifyRepo is the pgx implementation of walletverify.Repo.
type WalletVerifyRepo struct{ db *pgxpool.Pool }

func NewWalletVerifyRepo(db *pgxpool.Pool) *WalletVerifyRepo { return &WalletVerifyRepo{db: db} }

var _ walletverify.Repo = (*WalletVerifyRepo)(nil)

// SaveChallenge upserts the single pending challenge for a user.
func (r *WalletVerifyRepo) SaveChallenge(ctx context.Context, userPublicID, walletAddress, nonce string, expiresAt time.Time) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO wallet_verify_challenges (user_public_id, wallet_address, nonce, expires_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (user_public_id) DO UPDATE
		   SET wallet_address = EXCLUDED.wallet_address, nonce = EXCLUDED.nonce, expires_at = EXCLUDED.expires_at`,
		userPublicID, walletAddress, nonce, expiresAt)
	return err
}

func (r *WalletVerifyRepo) GetChallenge(ctx context.Context, userPublicID string) (walletverify.Challenge, bool, error) {
	var c walletverify.Challenge
	err := r.db.QueryRow(ctx,
		`SELECT wallet_address, nonce, expires_at FROM wallet_verify_challenges WHERE user_public_id = $1`,
		userPublicID).Scan(&c.WalletAddress, &c.Nonce, &c.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return walletverify.Challenge{}, false, nil
	}
	if err != nil {
		return walletverify.Challenge{}, false, err
	}
	return c, true, nil
}

func (r *WalletVerifyRepo) MarkVerified(ctx context.Context, userPublicID, walletAddress string, at time.Time) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET verified_wallet_address = $2, wallet_verified_at = $3 WHERE public_id = $1`,
		userPublicID, walletAddress, at)
	return err
}

func (r *WalletVerifyRepo) ClearChallenge(ctx context.Context, userPublicID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM wallet_verify_challenges WHERE user_public_id = $1`, userPublicID)
	return err
}
