// Package seedadmin creates (or repairs) the operator's own login.
//
// Admin rights are granted by ADMIN_USER_IDS — a list of user public ids checked by
// auth.IsAdmin. That is the right design and it has a chicken-and-egg problem: the id cannot
// go in the allowlist before the account exists, and the account cannot be made through the
// UI as an admin because nobody is one yet. So the first admin on every deployment had to be
// created by hand by someone with database access.
//
// This closes that, and it is shared by two callers so they cannot drift:
//
//   - the server at boot (SEED_ADMIN_EMAIL + SEED_ADMIN_PASSWORD set), which is what makes it
//     work through CI/CD with no extra deploy step and no shell in the distroless image;
//   - cmd/seed-admin, for running it by hand against any database.
//
// The public id is DETERMINISTIC (default usr_pyyoladmin), so ADMIN_USER_IDS can be set once,
// statically, before this ever runs — and the id never moves between deploys.
//
// IDEMPOTENT: run it a thousand times and there is one account. An existing account has its
// password reset and its status reactivated, deliberately — the reason an operator reaches
// for this is almost always "I cannot get in".
//
// It does NOT grant admin rights (ADMIN_USER_IDS does, and that stays an explicit deployment
// decision rather than something a seeding job awards itself), and it creates no agent: an
// operator account is for operating, and an admin who also owns a competing agent is a
// conflict nobody needs.
package seedadmin

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/agent-arena/arena/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultUserID is the operator account's public id. Deterministic on purpose: it is what
// makes ADMIN_USER_IDS configurable before the account exists.
const DefaultUserID = "usr_pyyoladmin"

// Result reports what happened, so a caller can log it precisely.
type Result struct {
	UserPublicID string
	Created      bool
}

// Run upserts the operator account. Every argument is required except userPublicID, which
// defaults to DefaultUserID.
//
// `pepper` MUST be the server's API_KEY_PEPPER: LogIn verifies bcrypt over an HMAC of the
// password with it, so a hash made with any other value can never be verified. Getting this
// wrong produces an account that looks perfect in the database and rejects every login with
// "invalid credentials" — which is exactly what happened the first time this was written
// with a bare bcrypt call, hence identity.HashPassword being the only way in.
func Run(ctx context.Context, pool *pgxpool.Pool, email, password, userPublicID, pepper string) (Result, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	userPublicID = strings.TrimSpace(userPublicID)
	if userPublicID == "" {
		userPublicID = DefaultUserID
	}
	if email == "" || password == "" {
		return Result{}, errors.New("seedadmin: email and password are both required")
	}
	if strings.TrimSpace(pepper) == "" {
		return Result{}, errors.New("seedadmin: the server's API_KEY_PEPPER is required")
	}
	// bcrypt silently truncates at 72 bytes, so a longer passphrase would authenticate on its
	// first 72. Say so rather than quietly weakening it.
	if len(password) > 72 {
		return Result{}, errors.New("seedadmin: password must be 72 bytes or fewer (bcrypt truncates beyond that)")
	}
	if len(password) < 8 {
		return Result{}, errors.New("seedadmin: password must be at least 8 characters")
	}

	hash, err := identity.HashPassword(password, pepper)
	if err != nil {
		return Result{}, fmt.Errorf("seedadmin: hash password: %w", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		userDBID int64
		publicID string
		created  bool
	)
	// Matched on EMAIL first, then on the deterministic id. Email first because that is what
	// the operator types: if an account with this address already exists under some other id,
	// resetting its password is what they mean — a second account they cannot reach is not.
	err = tx.QueryRow(ctx,
		`SELECT id, public_id FROM users WHERE email = $1 OR public_id = $2 LIMIT 1`,
		email, userPublicID).Scan(&userDBID, &publicID)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// status='active' explicitly: a suspended account cannot log in, and a column
		// default is not something to rely on for the one account that unlocks the rest.
		if err := tx.QueryRow(ctx,
			`INSERT INTO users (public_id, email, password_hash, status, segment)
			 VALUES ($1, $2, $3, 'active', 'company')
			 RETURNING id, public_id`,
			userPublicID, email, hash).Scan(&userDBID, &publicID); err != nil {
			return Result{}, fmt.Errorf("seedadmin: insert user: %w", err)
		}
		created = true
	case err != nil:
		return Result{}, fmt.Errorf("seedadmin: lookup user: %w", err)
	default:
		if _, err := tx.Exec(ctx,
			`UPDATE users SET email = $2, password_hash = $3, status = 'active', updated_at = now()
			  WHERE id = $1`,
			userDBID, email, hash); err != nil {
			return Result{}, fmt.Errorf("seedadmin: update user: %w", err)
		}
	}

	// A treasury wallet, because every money read joins one and a missing row reads as a
	// broken account rather than an empty one.
	if _, err := tx.Exec(ctx,
		`INSERT INTO wallets (user_id, kind, balance)
		 SELECT $1, 'user', 0 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE user_id = $1)`,
		userDBID); err != nil {
		return Result{}, fmt.Errorf("seedadmin: ensure treasury wallet: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Result{}, err
	}
	return Result{UserPublicID: publicID, Created: created}, nil
}
