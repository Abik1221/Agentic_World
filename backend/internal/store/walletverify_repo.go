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
	// Proving ownership of a wallet ALSO links it as the withdrawal destination:
	// set both verified_wallet_address AND wallet_address (the destination "hint")
	// to the proven wallet. This lets email/password (non-Privy) users establish a
	// payout wallet purely by connecting + signing — no Privy required (M11). Payout
	// still requires verified == hint, so funds only ever reach a proven wallet; the
	// change is gated by the new-address cooldown (and 2FA step-up when enabled).
	_, err := r.db.Exec(ctx,
		`UPDATE users
		 SET verified_wallet_address = $2, wallet_address = $2, wallet_verified_at = $3
		 WHERE public_id = $1`,
		userPublicID, walletAddress, at)
	return err
}

// ClearVerified unlinks the payout wallet: both the proven address and the
// destination hint go back to NULL, so the account reads "no wallet on file" and a
// future withdrawal must re-prove ownership. Pending withdrawals keep their own
// dest_wallet_address, so an in-flight payout is not disturbed.
func (r *WalletVerifyRepo) ClearVerified(ctx context.Context, userPublicID string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users
		 SET verified_wallet_address = NULL, wallet_address = NULL, wallet_verified_at = NULL
		 WHERE public_id = $1`,
		userPublicID)
	return err
}

func (r *WalletVerifyRepo) ClearChallenge(ctx context.Context, userPublicID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM wallet_verify_challenges WHERE user_public_id = $1`, userPublicID)
	return err
}

// LinkedWallet reads the destination hint, the PROVEN address, and the wallet brand.
func (r *WalletVerifyRepo) LinkedWallet(ctx context.Context, userPublicID string) (hint, verified, provider string, err error) {
	err = r.db.QueryRow(ctx,
		`SELECT COALESCE(wallet_address, ''), COALESCE(verified_wallet_address, ''),
		        COALESCE(wallet_provider, '')
		   FROM users WHERE public_id = $1`, userPublicID).
		Scan(&hint, &verified, &provider)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", "", nil
	}
	return hint, verified, provider, err
}

// SetWalletProvider records the wallet brand (phantom, solflare, backpack…).
//
// Free to overwrite because NO money path reads it: it exists so an operator looking at
// an account can see which wallet app is on it, which was previously blank for everyone
// who did not sign in through Privy.
func (r *WalletVerifyRepo) SetWalletProvider(ctx context.Context, userPublicID, provider string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE users SET wallet_provider = $2, updated_at = now() WHERE public_id = $1`,
		userPublicID, provider)
	return err
}

// SetWalletHintIfEmpty fills users.wallet_address only when it is currently unset.
//
// The null-or-blank guard in the WHERE clause is a MONEY control, not a
// micro-optimisation, and it lives in the SQL so no future caller can bypass it by
// forgetting the check. Payout refuses to pay unless wallet_address equals
// verified_wallet_address; repointing the hint from a casual browser connect would
// therefore break withdrawals for someone whose only mistake was connecting a second
// wallet to look at something. Filling an EMPTY hint is safe — there is no pairing yet
// to break — and it is the case that lets a non-Privy developer's profile register that
// they have connected a wallet at all.
func (r *WalletVerifyRepo) SetWalletHintIfEmpty(ctx context.Context, userPublicID, walletAddress string) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`UPDATE users SET wallet_address = $2, updated_at = now()
		  WHERE public_id = $1
		    AND (wallet_address IS NULL OR wallet_address = '')`,
		userPublicID, walletAddress)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}
