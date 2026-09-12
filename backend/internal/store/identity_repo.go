package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/agent-arena/arena/internal/identity"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdentityRepo is the pgx-backed implementation of identity.Repo. All SQL is
// parameterized (injection-safe). store is the only package that imports the DB
// driver; identity depends on the interface, not on this type.
//
// NOTE: these queries are hand-written parameterized SQL. The project target is
// to generate them with sqlc (see sqlc.yaml + internal/store/queries); they were
// written by hand here to keep the stage buildable without running codegen, and
// can be replaced by generated equivalents without changing the interface.
type IdentityRepo struct{ db *pgxpool.Pool }

// NewIdentityRepo wires the repo to the connection pool.
func NewIdentityRepo(db *pgxpool.Pool) *IdentityRepo { return &IdentityRepo{db: db} }

// insertSignupKey writes the one-time 'initial' key only when a secret was actually
// generated. Ordinary email/OAuth signup no longer mints one — the dashboard JWT
// sits the agent, and `pyyol login` issues a per-machine key when the CLI needs it.
func insertSignupKey(ctx context.Context, tx pgx.Tx, agentID int64, prefix, hash string) error {
	if prefix == "" || hash == "" {
		return nil
	}
	_, err := tx.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope, label)
		 VALUES ($1, $2, $3, 'agent', 'initial')`,
		agentID, prefix, hash)
	return err
}

var _ identity.Repo = (*IdentityRepo)(nil)

func (r *IdentityRepo) CreateClaim(ctx context.Context, c identity.Claim) error {
	_, err := r.db.Exec(ctx,
		`INSERT INTO claims (claim_token, agent_name, description, status, expires_at)
		 VALUES ($1, $2, $3, 'pending', $4)`,
		c.Token, c.AgentName, nullString(c.Description), c.ExpiresAt)
	return err
}

func (r *IdentityRepo) GetClaim(ctx context.Context, token string) (identity.Claim, error) {
	var c identity.Claim
	err := r.db.QueryRow(ctx,
		`SELECT claim_token, agent_name, COALESCE(description,''), status, expires_at
		 FROM claims WHERE claim_token = $1`, token).
		Scan(&c.Token, &c.AgentName, &c.Description, &c.Status, &c.ExpiresAt)
	if err != nil {
		return identity.Claim{}, err
	}
	return c, nil
}

func (r *IdentityRepo) CompleteClaim(ctx context.Context, in identity.CompleteClaimInput) (identity.Agent, identity.User, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return identity.Agent{}, identity.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	// 1. Find-or-create the owner by their X identity (one human → N agents).
	var userID int64
	var userPublicID string
	err = tx.QueryRow(ctx, `SELECT id, public_id FROM users WHERE x_user_id = $1`, in.XUserID).
		Scan(&userID, &userPublicID)
	if errors.Is(err, pgx.ErrNoRows) {
		userPublicID = in.UserPublicID
		err = tx.QueryRow(ctx,
			`INSERT INTO users (public_id, x_user_id, x_handle) VALUES ($1, $2, $3) RETURNING id`,
			userPublicID, in.XUserID, in.XHandle).Scan(&userID)
	}
	if err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 1b. Ensure the owner has a treasury wallet (deposits land here).
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (user_id, kind, balance)
		 SELECT $1, 'user', 0 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE user_id = $1)`,
		userID); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 2. Create the agent with default limits.
	l := in.Limits
	var agentID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework,
		     status, verification_level, coin_limit_per_match, daily_loss_limit, session_loss_limit,
		     min_wallet_balance, max_concurrent_matches, cooldown_losses, cooldown_seconds, max_bid, auto_join)
		 VALUES ($1,$2,$3,$4,$5,$6,'unverified','new',$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 RETURNING id`,
		in.AgentPublicID, userID, in.AgentName, in.AgentSlug, nullString(in.Description), nullString(in.Framework),
		l.CoinLimitPerMatch, l.DailyLossLimit, l.SessionLossLimit, l.MinWalletBalance,
		l.MaxConcurrentMatches, l.CooldownLosses, l.CooldownSeconds, l.MaxBid, l.AutoJoin).
		Scan(&agentID)
	if err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 3. Optional first API key (empty prefix ⇒ dashboard-only account).
	if err = insertSignupKey(ctx, tx, agentID, in.KeyPrefix, in.KeyHash); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 3b. Open the agent's wallet in the same transaction so every agent always
	//     has exactly one wallet (the ledger's unit of account; see Stage 4).
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (agent_id, kind, balance) VALUES ($1, 'agent', 0)`,
		agentID); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 4. Mark the claim verified (only if still pending — guards a concurrent verify).
	ct, err := tx.Exec(ctx,
		`UPDATE claims SET status='verified', x_user_id=$2, x_handle=$3, agent_id=$4
		 WHERE claim_token=$1 AND status='pending'`,
		in.Token, in.XUserID, in.XHandle, agentID)
	if err != nil {
		return identity.Agent{}, identity.User{}, err
	}
	if ct.RowsAffected() == 0 {
		return identity.Agent{}, identity.User{}, errors.New("claim was already verified")
	}

	if err = tx.Commit(ctx); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	agent := identity.Agent{
		PublicID: in.AgentPublicID, OwnerPublicID: userPublicID, Name: in.AgentName,
		Slug: in.AgentSlug, Description: in.Description, Status: "unverified",
		VerificationLevel: "new", Limits: l,
	}
	user := identity.User{PublicID: userPublicID, XUserID: in.XUserID, XHandle: in.XHandle}
	return agent, user, nil
}

func (r *IdentityRepo) LiveKeyByPrefix(ctx context.Context, prefix string) (identity.KeyRecord, error) {
	var rec identity.KeyRecord
	err := r.db.QueryRow(ctx,
		`SELECT k.key_hash, a.public_id, u.public_id
		 FROM agent_keys k
		 JOIN agents a ON a.id = k.agent_id
		 JOIN users  u ON u.id = a.owner_user_id
		 WHERE k.key_prefix = $1 AND k.revoked_at IS NULL`, prefix).
		Scan(&rec.Hash, &rec.AgentPublicID, &rec.OwnerPublicID)
	if err != nil {
		return identity.KeyRecord{}, err
	}
	return rec, nil
}

// RevokedKeyByPrefix returns the most recently revoked key for a prefix. Prefixes
// are 8 random bytes and the live-key index makes them unique among live keys, but
// nothing stops a prefix appearing more than once across revoked history, so this
// takes the newest revocation deterministically rather than relying on that.
func (r *IdentityRepo) RevokedKeyByPrefix(ctx context.Context, prefix string) (identity.KeyRecord, error) {
	var rec identity.KeyRecord
	err := r.db.QueryRow(ctx,
		`SELECT k.key_hash, a.public_id, u.public_id
		 FROM agent_keys k
		 JOIN agents a ON a.id = k.agent_id
		 JOIN users  u ON u.id = a.owner_user_id
		 WHERE k.key_prefix = $1 AND k.revoked_at IS NOT NULL
		 ORDER BY k.revoked_at DESC
		 LIMIT 1`, prefix).
		Scan(&rec.Hash, &rec.AgentPublicID, &rec.OwnerPublicID)
	if err != nil {
		return identity.KeyRecord{}, err
	}
	return rec, nil
}

func (r *IdentityRepo) TouchKey(ctx context.Context, prefix string) error {
	_, err := r.db.Exec(ctx,
		`UPDATE agent_keys SET last_used_at = now() WHERE key_prefix = $1 AND revoked_at IS NULL`, prefix)
	return err
}

func (r *IdentityRepo) InsertKey(ctx context.Context, agentPublicID, ownerPublicID, prefix, hash string) error {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope)
		 SELECT a.id, $3, $4, 'agent'
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.public_id = $1 AND u.public_id = $2`,
		agentPublicID, ownerPublicID, prefix, hash)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return identity.ErrForbiddenOwner
	}
	return nil
}

func (r *IdentityRepo) ListKeys(ctx context.Context, ownerPublicID string) ([]identity.KeyInfo, error) {
	rows, err := r.db.Query(ctx,
		`SELECT k.key_prefix, a.public_id, k.label, k.created_at, k.last_used_at, k.revoked_at
		 FROM agent_keys k
		 JOIN agents a ON a.id = k.agent_id
		 JOIN users  u ON u.id = a.owner_user_id
		 WHERE u.public_id = $1
		 ORDER BY k.revoked_at IS NOT NULL, k.created_at DESC`, ownerPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []identity.KeyInfo
	for rows.Next() {
		var k identity.KeyInfo
		if err := rows.Scan(&k.Prefix, &k.AgentPublicID, &k.Label, &k.CreatedAt, &k.LastUsedAt, &k.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// IssueKey replaces one label's key and leaves every other label alone. See
// identity.Repo for the contract and migration 0071 for why this is not a
// revoke-everything rotate any more.
func (r *IdentityRepo) IssueKey(ctx context.Context, agentPublicID, ownerPublicID, prefix, hash, label string, maxLive int) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Revoke only THIS label's live key, so re-issuing for one machine replaces that
	// machine's credential. The partial unique index (agent_id, label) WHERE live
	// makes this a hard guarantee under concurrency, not just a hopeful ordering.
	if _, err := tx.Exec(ctx,
		`UPDATE agent_keys SET revoked_at = now()
		 WHERE revoked_at IS NULL AND label = $3 AND agent_id IN (
		     SELECT a.id FROM agents a JOIN users u ON u.id = a.owner_user_id
		     WHERE a.public_id = $1 AND u.public_id = $2)`,
		agentPublicID, ownerPublicID, label); err != nil {
		return err
	}

	// Reclaim the sign-up key's slot — but ONLY if nothing ever authenticated with it.
	//
	// Account creation issues a key labelled 'initial' and shows it exactly once. Most
	// developers never save it: they run `pyyol login`, which used to revoke it as a
	// side effect of revoking everything. Now that issuing is per-machine it would
	// survive forever as a live credential nobody holds.
	//
	// `last_used_at IS NULL` is the discriminator, and it is the whole reason this is
	// safe: a developer who DID save that key and put it in a deployment has a key that
	// has authenticated, so it is left alone. Never-used means nobody is holding it.
	if _, err := tx.Exec(ctx,
		`UPDATE agent_keys SET revoked_at = now()
		 WHERE revoked_at IS NULL AND label = 'initial' AND last_used_at IS NULL
		   AND label <> $3
		   AND agent_id IN (
		     SELECT a.id FROM agents a JOIN users u ON u.id = a.owner_user_id
		     WHERE a.public_id = $1 AND u.public_id = $2)`,
		agentPublicID, ownerPublicID, label); err != nil {
		return err
	}

	// Count what would remain live AFTER that revoke, inside the same transaction, so
	// two concurrent issues cannot both read "one under the cap" and both insert.
	if maxLive > 0 {
		var live int
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM agent_keys k
			 WHERE k.revoked_at IS NULL AND k.agent_id IN (
			     SELECT a.id FROM agents a JOIN users u ON u.id = a.owner_user_id
			     WHERE a.public_id = $1 AND u.public_id = $2)`,
			agentPublicID, ownerPublicID).Scan(&live); err != nil {
			return err
		}
		if live >= maxLive {
			return identity.ErrTooManyKeys
		}
	}

	// Then mint the replacement, re-checking ownership.
	ct, err := tx.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope, label)
		 SELECT a.id, $3, $4, 'agent', $5
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.public_id = $1 AND u.public_id = $2`,
		agentPublicID, ownerPublicID, prefix, hash, label)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return identity.ErrForbiddenOwner
	}
	return tx.Commit(ctx)
}

func (r *IdentityRepo) SetSigningKey(ctx context.Context, agentPublicID, ownerPublicID, pubkey string) error {
	ct, err := r.db.Exec(ctx,
		`UPDATE agents SET signing_pubkey = $3, updated_at = now()
		 WHERE public_id = $1
		   AND owner_user_id = (SELECT id FROM users WHERE public_id = $2)`,
		agentPublicID, ownerPublicID, pubkey)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return identity.ErrForbiddenOwner
	}
	return nil
}

func (r *IdentityRepo) RevokeKey(ctx context.Context, ownerPublicID, prefix string) error {
	ct, err := r.db.Exec(ctx,
		`UPDATE agent_keys SET revoked_at = now()
		 WHERE key_prefix = $2 AND revoked_at IS NULL
		   AND agent_id IN (
		       SELECT a.id FROM agents a JOIN users u ON u.id = a.owner_user_id
		       WHERE u.public_id = $1)`,
		ownerPublicID, prefix)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return identity.ErrNotFound
	}
	return nil
}

func (r *IdentityRepo) UpdateLimits(ctx context.Context, agentPublicID, ownerPublicID string, l identity.Limits) error {
	ct, err := r.db.Exec(ctx,
		`UPDATE agents SET
		     coin_limit_per_match=$3, daily_loss_limit=$4, session_loss_limit=$5,
		     min_wallet_balance=$6, max_bid=$7, max_concurrent_matches=$8,
		     cooldown_losses=$9, cooldown_seconds=$10, auto_join=$11, updated_at=now()
		 WHERE public_id=$1
		   AND owner_user_id = (SELECT id FROM users WHERE public_id=$2)`,
		agentPublicID, ownerPublicID, l.CoinLimitPerMatch, l.DailyLossLimit, l.SessionLossLimit,
		l.MinWalletBalance, l.MaxBid, l.MaxConcurrentMatches, l.CooldownLosses, l.CooldownSeconds, l.AutoJoin)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return identity.ErrForbiddenOwner
	}
	return nil
}

// PrimaryAgentOf returns the owner's oldest non-house agent, or "" when they have none.
//
// House agents are excluded for the same reason they are everywhere else: they are
// platform-run opponents, not the developer's work, and addressing one by default would let
// an owner's guardrail save land on a bot they do not own.
func (r *IdentityRepo) PrimaryAgentOf(ctx context.Context, ownerPublicID string) (string, error) {
	var agent string
	err := r.db.QueryRow(ctx,
		`SELECT COALESCE((
		    SELECT a.public_id FROM agents a
		     WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		       AND a.kind <> 'house'
		     ORDER BY a.created_at, a.id
		     LIMIT 1
		 ), '')`, ownerPublicID).Scan(&agent)
	return agent, err
}

func (r *IdentityRepo) AgentByOwner(ctx context.Context, agentPublicID, ownerPublicID string) (identity.Agent, error) {
	var a identity.Agent
	var l identity.Limits
	err := r.db.QueryRow(ctx,
		`SELECT a.public_id, a.name, a.slug, COALESCE(a.description,''), COALESCE(a.framework,''),
		        a.status, a.verification_level,
		        a.coin_limit_per_match, a.daily_loss_limit, a.session_loss_limit, a.min_wallet_balance,
		        a.max_bid, a.max_concurrent_matches, a.cooldown_losses, a.cooldown_seconds, a.auto_join
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE a.public_id = $1 AND u.public_id = $2`,
		agentPublicID, ownerPublicID).
		Scan(&a.PublicID, &a.Name, &a.Slug, &a.Description, &a.Framework, &a.Status, &a.VerificationLevel,
			&l.CoinLimitPerMatch, &l.DailyLossLimit, &l.SessionLossLimit, &l.MinWalletBalance,
			&l.MaxBid, &l.MaxConcurrentMatches, &l.CooldownLosses, &l.CooldownSeconds, &l.AutoJoin)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.Agent{}, identity.ErrForbiddenOwner
		}
		return identity.Agent{}, err
	}
	a.OwnerPublicID = ownerPublicID
	a.Limits = l
	return a, nil
}

// CreateAccount mints an email+password owner and their first agent in one
// transaction. It mirrors CompleteClaim but keys the owner on email rather than
// an X identity, and always creates a fresh owner (a duplicate email surfaces as
// ErrEmailTaken via the users.email unique constraint).
func (r *IdentityRepo) CreateAccount(ctx context.Context, in identity.CreateAccountInput) (identity.Agent, identity.User, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return identity.Agent{}, identity.User{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	// 1. Create the owner. A duplicate email trips the unique index (23505).
	var userID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO users (public_id, email, password_hash) VALUES ($1, $2, $3) RETURNING id`,
		in.UserPublicID, in.Email, in.PasswordHash).Scan(&userID)
	if err != nil {
		if isUniqueViolation(err) {
			return identity.Agent{}, identity.User{}, identity.ErrEmailTaken
		}
		return identity.Agent{}, identity.User{}, err
	}

	// 1b. Ensure the owner has a treasury wallet (deposits land here).
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (user_id, kind, balance)
		 SELECT $1, 'user', 0 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE user_id = $1)`,
		userID); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 2. Create the agent with the limits and kind the caller asked for.
	//
	// THE KIND IS WRITTEN HERE, at creation, and there is no update path for it. Public
	// sinks filter on an allowlist of kind='external', so what an agent is has to be true
	// from its first row: a match played while the agent was still `external` is already
	// attributed to the developer board, and re-labelling the agent afterwards leaves that
	// match exactly where it was. Empty means external, so every caller that does not set
	// it — X-claim onboarding, Google sign-in, the public sign-up — keeps creating
	// developer agents unchanged.
	l := in.Limits
	kind := in.Kind
	if kind == "" {
		kind = identity.KindExternal
	}
	var agentID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework,
		     status, verification_level, coin_limit_per_match, daily_loss_limit, session_loss_limit,
		     min_wallet_balance, max_concurrent_matches, cooldown_losses, cooldown_seconds, max_bid, auto_join,
		     kind)
		 VALUES ($1,$2,$3,$4,$5,$6,'unverified','new',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		 RETURNING id`,
		in.AgentPublicID, userID, in.AgentName, in.AgentSlug, nullString(in.Description), nullString(in.Framework),
		l.CoinLimitPerMatch, l.DailyLossLimit, l.SessionLossLimit, l.MinWalletBalance,
		l.MaxConcurrentMatches, l.CooldownLosses, l.CooldownSeconds, l.MaxBid, l.AutoJoin,
		kind).
		Scan(&agentID)
	if err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 3. Optional first API key (empty prefix ⇒ dashboard-only account).
	if err = insertSignupKey(ctx, tx, agentID, in.KeyPrefix, in.KeyHash); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	// 3b. Open the agent's wallet in the same transaction.
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (agent_id, kind, balance) VALUES ($1, 'agent', 0)`,
		agentID); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	if err = tx.Commit(ctx); err != nil {
		return identity.Agent{}, identity.User{}, err
	}

	agent := identity.Agent{
		PublicID: in.AgentPublicID, OwnerPublicID: in.UserPublicID, Name: in.AgentName,
		Slug: in.AgentSlug, Description: in.Description, Status: "unverified",
		VerificationLevel: "new", Limits: l,
	}
	user := identity.User{PublicID: in.UserPublicID}
	return agent, user, nil
}

// UpsertGoogleAccount implements identity.Repo. See the interface doc.
func (r *IdentityRepo) UpsertGoogleAccount(ctx context.Context, in identity.GoogleUpsertInput) (identity.GoogleUpsertResult, error) {
	// 1. Already linked to this Google identity → log in.
	var uPub, aPub, aName string
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id, a.public_id, a.name
		 FROM users u JOIN agents a ON a.owner_user_id = u.id
		 WHERE u.google_sub = $1
		 ORDER BY a.id LIMIT 1`, in.GoogleSub).Scan(&uPub, &aPub, &aName)
	if err == nil {
		return identity.GoogleUpsertResult{UserPublicID: uPub, AgentPublicID: aPub, AgentName: aName}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return identity.GoogleUpsertResult{}, err
	}

	// 2. A pre-existing, unlinked email account (Google verified this email) → link it.
	if in.Email != "" {
		var uid int64
		e2 := r.db.QueryRow(ctx,
			`SELECT u.id, u.public_id, COALESCE(a.public_id,''), COALESCE(a.name,'')
			 FROM users u LEFT JOIN agents a ON a.owner_user_id = u.id
			 WHERE u.email = $1 AND u.google_sub IS NULL
			 ORDER BY a.id LIMIT 1`, in.Email).Scan(&uid, &uPub, &aPub, &aName)
		if e2 == nil {
			if _, err := r.db.Exec(ctx,
				`UPDATE users SET google_sub = $1 WHERE id = $2 AND google_sub IS NULL`, in.GoogleSub, uid); err != nil {
				return identity.GoogleUpsertResult{}, err
			}
			return identity.GoogleUpsertResult{UserPublicID: uPub, AgentPublicID: aPub, AgentName: aName}, nil
		}
		if !errors.Is(e2, pgx.ErrNoRows) {
			return identity.GoogleUpsertResult{}, e2
		}
	}

	// 3. Create a fresh account: user + treasury wallet + agent + first key + agent wallet.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return identity.GoogleUpsertResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO users (public_id, email, google_sub) VALUES ($1, $2, $3) RETURNING id`,
		in.UserPublicID, nullString(in.Email), in.GoogleSub).Scan(&userID)
	if err != nil {
		if isUniqueViolation(err) {
			return identity.GoogleUpsertResult{}, identity.ErrEmailTaken
		}
		return identity.GoogleUpsertResult{}, err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (user_id, kind, balance)
		 SELECT $1, 'user', 0 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE user_id = $1)`, userID); err != nil {
		return identity.GoogleUpsertResult{}, err
	}
	l := in.Limits
	var agentID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework,
		     status, verification_level, coin_limit_per_match, daily_loss_limit, session_loss_limit,
		     min_wallet_balance, max_concurrent_matches, cooldown_losses, cooldown_seconds, max_bid, auto_join)
		 VALUES ($1,$2,$3,$4,$5,$6,'unverified','new',$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 RETURNING id`,
		in.AgentPublicID, userID, in.AgentName, in.AgentSlug, nullString(""), nullString(""),
		l.CoinLimitPerMatch, l.DailyLossLimit, l.SessionLossLimit, l.MinWalletBalance,
		l.MaxConcurrentMatches, l.CooldownLosses, l.CooldownSeconds, l.MaxBid, l.AutoJoin).
		Scan(&agentID)
	if err != nil {
		return identity.GoogleUpsertResult{}, err
	}
	if err = insertSignupKey(ctx, tx, agentID, in.KeyPrefix, in.KeyHash); err != nil {
		return identity.GoogleUpsertResult{}, err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (agent_id, kind, balance) VALUES ($1, 'agent', 0)`, agentID); err != nil {
		return identity.GoogleUpsertResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return identity.GoogleUpsertResult{}, err
	}
	return identity.GoogleUpsertResult{
		UserPublicID: in.UserPublicID, AgentPublicID: in.AgentPublicID, AgentName: in.AgentName, Created: true,
	}, nil
}

// UpsertGitHubAccount implements identity.Repo. See the interface doc. Deliberately
// mirrors UpsertGoogleAccount step-for-step, keyed on github_id instead of google_sub.
func (r *IdentityRepo) UpsertGitHubAccount(ctx context.Context, in identity.GitHubUpsertInput) (identity.GitHubUpsertResult, error) {
	// 1. Already linked to this GitHub identity → log in.
	var uPub, aPub, aName string
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id, a.public_id, a.name
		 FROM users u JOIN agents a ON a.owner_user_id = u.id
		 WHERE u.github_id = $1
		 ORDER BY a.id LIMIT 1`, in.GitHubID).Scan(&uPub, &aPub, &aName)
	if err == nil {
		return identity.GitHubUpsertResult{UserPublicID: uPub, AgentPublicID: aPub, AgentName: aName}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return identity.GitHubUpsertResult{}, err
	}

	// 2. A pre-existing, unlinked email account (GitHub verified this email) → link it.
	if in.Email != "" {
		var uid int64
		e2 := r.db.QueryRow(ctx,
			`SELECT u.id, u.public_id, COALESCE(a.public_id,''), COALESCE(a.name,'')
			 FROM users u LEFT JOIN agents a ON a.owner_user_id = u.id
			 WHERE u.email = $1 AND u.github_id IS NULL
			 ORDER BY a.id LIMIT 1`, in.Email).Scan(&uid, &uPub, &aPub, &aName)
		if e2 == nil {
			if _, err := r.db.Exec(ctx,
				`UPDATE users SET github_id = $1 WHERE id = $2 AND github_id IS NULL`, in.GitHubID, uid); err != nil {
				return identity.GitHubUpsertResult{}, err
			}
			return identity.GitHubUpsertResult{UserPublicID: uPub, AgentPublicID: aPub, AgentName: aName}, nil
		}
		if !errors.Is(e2, pgx.ErrNoRows) {
			return identity.GitHubUpsertResult{}, e2
		}
	}

	// 3. Create a fresh account: user + treasury wallet + agent + first key + agent wallet.
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return identity.GitHubUpsertResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO users (public_id, email, github_id) VALUES ($1, $2, $3) RETURNING id`,
		in.UserPublicID, nullString(in.Email), in.GitHubID).Scan(&userID)
	if err != nil {
		if isUniqueViolation(err) {
			return identity.GitHubUpsertResult{}, identity.ErrEmailTaken
		}
		return identity.GitHubUpsertResult{}, err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (user_id, kind, balance)
		 SELECT $1, 'user', 0 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE user_id = $1)`, userID); err != nil {
		return identity.GitHubUpsertResult{}, err
	}
	l := in.Limits
	var agentID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework,
		     status, verification_level, coin_limit_per_match, daily_loss_limit, session_loss_limit,
		     min_wallet_balance, max_concurrent_matches, cooldown_losses, cooldown_seconds, max_bid, auto_join)
		 VALUES ($1,$2,$3,$4,$5,$6,'unverified','new',$7,$8,$9,$10,$11,$12,$13,$14,$15)
		 RETURNING id`,
		in.AgentPublicID, userID, in.AgentName, in.AgentSlug, nullString(""), nullString(""),
		l.CoinLimitPerMatch, l.DailyLossLimit, l.SessionLossLimit, l.MinWalletBalance,
		l.MaxConcurrentMatches, l.CooldownLosses, l.CooldownSeconds, l.MaxBid, l.AutoJoin).
		Scan(&agentID)
	if err != nil {
		return identity.GitHubUpsertResult{}, err
	}
	if err = insertSignupKey(ctx, tx, agentID, in.KeyPrefix, in.KeyHash); err != nil {
		return identity.GitHubUpsertResult{}, err
	}
	if _, err = tx.Exec(ctx,
		`INSERT INTO wallets (agent_id, kind, balance) VALUES ($1, 'agent', 0)`, agentID); err != nil {
		return identity.GitHubUpsertResult{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return identity.GitHubUpsertResult{}, err
	}
	return identity.GitHubUpsertResult{
		UserPublicID: in.UserPublicID, AgentPublicID: in.AgentPublicID, AgentName: in.AgentName, Created: true,
	}, nil
}

// CredentialsByEmail returns the auth record for a password-enabled owner. The
// LEFT JOIN yields the owner's single agent when present (one-agent-per-user is
// enforced by uq_agents_owner, migration 0016).
func (r *IdentityRepo) CredentialsByEmail(ctx context.Context, email string) (identity.AuthRecord, error) {
	var rec identity.AuthRecord
	var agentPublic, agentName *string
	err := r.db.QueryRow(ctx,
		`SELECT u.public_id, u.password_hash, a.public_id, a.name
		 FROM users u
		 LEFT JOIN agents a ON a.owner_user_id = u.id
		 WHERE u.email = $1 AND u.password_hash IS NOT NULL
		 ORDER BY a.id
		 LIMIT 1`, email).
		Scan(&rec.UserPublicID, &rec.PasswordHash, &agentPublic, &agentName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.AuthRecord{}, identity.ErrNotFound
		}
		return identity.AuthRecord{}, err
	}
	if agentPublic != nil {
		rec.AgentPublicID = *agentPublic
	}
	if agentName != nil {
		rec.AgentName = *agentName
	}
	return rec, nil
}

// ownerAgentSubquery selects the id of the owner's (earliest) agent. A user may
// own more than one agent; the dashboard treats the earliest as the primary one
// (same tie-break as CredentialsByEmail).
const ownerAgentSubquery = `SELECT a.id FROM agents a JOIN users u ON u.id = a.owner_user_id
	 WHERE u.public_id = $1 ORDER BY a.id LIMIT 1`

// UpdateAgentProfile writes the owner's agent display identity. nil fields are
// left unchanged (COALESCE keeps the existing value). Returns ErrForbiddenOwner
// when the caller owns no agent (the UPDATE matches no row).
func (r *IdentityRepo) UpdateAgentProfile(ctx context.Context, ownerPublicID string, displayName, bio, avatarURL *string) (identity.AgentProfile, error) {
	var prof identity.AgentProfile
	err := r.db.QueryRow(ctx,
		`UPDATE agents SET
		     display_name = COALESCE($2, display_name),
		     bio          = COALESCE($3, bio),
		     avatar_url   = COALESCE($4, avatar_url),
		     updated_at   = now()
		 WHERE id = (`+ownerAgentSubquery+`)
		 RETURNING public_id, COALESCE(display_name,''), COALESCE(bio,''), COALESCE(avatar_url,'')`,
		ownerPublicID, displayName, bio, avatarURL).
		Scan(&prof.AgentPublicID, &prof.DisplayName, &prof.Bio, &prof.AvatarURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.AgentProfile{}, identity.ErrForbiddenOwner
		}
		return identity.AgentProfile{}, err
	}
	return prof, nil
}

// AgentProfileByOwner returns the owner's agent display identity.
func (r *IdentityRepo) AgentProfileByOwner(ctx context.Context, ownerPublicID string) (identity.AgentProfile, error) {
	var prof identity.AgentProfile
	err := r.db.QueryRow(ctx,
		`SELECT a.public_id, COALESCE(a.display_name,''), COALESCE(a.bio,''), COALESCE(a.avatar_url,'')
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE u.public_id = $1 ORDER BY a.id LIMIT 1`,
		ownerPublicID).
		Scan(&prof.AgentPublicID, &prof.DisplayName, &prof.Bio, &prof.AvatarURL)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.AgentProfile{}, identity.ErrForbiddenOwner
		}
		return identity.AgentProfile{}, err
	}
	return prof, nil
}

// SetGameConfig upserts per-game behaviour for the owner's agent. behavior is a
// JSON document; it is sent as text and cast to jsonb (pgx would otherwise send
// []byte as bytea). Returns ErrForbiddenOwner when the caller owns no agent.
func (r *IdentityRepo) SetGameConfig(ctx context.Context, ownerPublicID, game string, behavior []byte) error {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO agent_game_config (agent_id, game_type, behavior, updated_at)
		 SELECT a.id, $2, $3::jsonb, now()
		 FROM agents a JOIN users u ON u.id = a.owner_user_id
		 WHERE u.public_id = $1 ORDER BY a.id LIMIT 1
		 ON CONFLICT (agent_id, game_type)
		 DO UPDATE SET behavior = EXCLUDED.behavior, updated_at = now()`,
		ownerPublicID, game, string(behavior))
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return identity.ErrForbiddenOwner
	}
	return nil
}

// CreateMagicLink stores a single-use sign-in token hash for the account with
// this email. A missing email inserts nothing and returns found=false (the
// caller behaves as if a link was sent, so emails are not enumerable).
func (r *IdentityRepo) CreateMagicLink(ctx context.Context, tokenHash, email string, expiresAt time.Time) (bool, error) {
	ct, err := r.db.Exec(ctx,
		`INSERT INTO magic_links (token_hash, user_id, email, expires_at)
		 SELECT $1, u.id, $2, $3 FROM users u WHERE u.email = $2`,
		tokenHash, email, expiresAt)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// ConsumeMagicLink atomically marks a valid (unconsumed, unexpired) token used
// and returns its owner and (earliest) agent, or ErrNotFound.
func (r *IdentityRepo) ConsumeMagicLink(ctx context.Context, tokenHash string) (identity.MagicLink, error) {
	var ml identity.MagicLink
	var agentPublic *string
	err := r.db.QueryRow(ctx,
		`WITH consumed AS (
		     UPDATE magic_links SET consumed_at = now()
		     WHERE token_hash = $1 AND consumed_at IS NULL AND expires_at > now()
		     RETURNING user_id)
		 SELECT u.public_id, a.public_id
		 FROM consumed c
		 JOIN users u ON u.id = c.user_id
		 LEFT JOIN agents a ON a.owner_user_id = u.id
		 ORDER BY a.id
		 LIMIT 1`,
		tokenHash).
		Scan(&ml.UserPublicID, &agentPublic)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.MagicLink{}, identity.ErrNotFound
		}
		return identity.MagicLink{}, err
	}
	if agentPublic != nil {
		ml.AgentPublicID = *agentPublic
	}
	return ml, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// UpsertUserFromPrivy find-or-creates the owner behind a verified Privy identity,
// keyed STRICTLY on the cryptographically-proven privy_user_id (a DID). Resolution
// order: (1) an existing user already linked to this privy_user_id; (2) a brand-new
// owner. Every path guarantees a treasury wallet exists, in one transaction so a
// concurrent first login can't create two wallets/users.
//
// SECURITY: the email/wallet in the profile are UNVERIFIED client hints — only the
// Privy user id is proven by auth.PrivyVerifier. They are therefore NEVER used to
// find or link an existing account (doing so let an attacker with any valid Privy
// token claim a victim's account by passing profile.email=victim@… — the account
// takeover fixed here), and the email hint is NEVER written to the unique `email`
// column (which would let an attacker squat a victim's email and block their later
// password signup). Unifying a Privy login with a pre-existing email/password
// account is a separate, authenticated link flow — not an implicit side effect of a
// login carrying a self-asserted email.
func (r *IdentityRepo) UpsertUserFromPrivy(ctx context.Context, in identity.PrivyUpsertInput) (string, bool, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", false, err
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op after commit

	p := in.Profile

	// 1. Already linked to this Privy identity.
	var (
		userID   int64
		publicID string
	)
	err = tx.QueryRow(ctx,
		`SELECT id, public_id FROM users WHERE privy_user_id = $1`, in.PrivyUserID).
		Scan(&userID, &publicID)
	switch {
	case err == nil:
		if err = updatePrivyHints(ctx, tx, userID, p); err != nil {
			return "", false, err
		}
		if err = ensureUserWallet(ctx, tx, userID); err != nil {
			return "", false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return "", false, err
		}
		return publicID, false, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return "", false, err
	}

	// 2. Brand-new owner. Email is intentionally left NULL (see SECURITY above); the
	//    only unique key on the insert is privy_user_id, so a unique violation here
	//    can only mean a concurrent first login for the same Privy id won the race.
	//    Resolve idempotently to that winner instead of erroring.
	err = tx.QueryRow(ctx,
		`INSERT INTO users (public_id, privy_user_id, wallet_address, wallet_provider, display_name, avatar_url)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		in.UserPublicID, in.PrivyUserID, nullString(p.WalletAddress),
		nullString(p.WalletProvider), nullString(p.DisplayName), nullString(p.AvatarURL)).
		Scan(&userID)
	if err != nil {
		if isUniqueViolation(err) {
			// The winning tx has committed its user+wallet (that is why the unique
			// index rejected us), so a fresh read finds it complete.
			_ = tx.Rollback(ctx)
			var (
				raceUserID   int64
				racePublicID string
			)
			if e := r.db.QueryRow(ctx,
				`SELECT id, public_id FROM users WHERE privy_user_id = $1`, in.PrivyUserID).
				Scan(&raceUserID, &racePublicID); e != nil {
				return "", false, e
			}
			return racePublicID, false, nil
		}
		return "", false, err
	}
	if err = ensureUserWallet(ctx, tx, userID); err != nil {
		return "", false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", false, err
	}
	return in.UserPublicID, true, nil
}

// updatePrivyHints overwrites the profile hint columns only when a non-empty hint
// is supplied (COALESCE(NULLIF(...))), so a later login missing a field never
// blanks a previously-captured value. Email is intentionally NOT touched here —
// it is a unique key set only at create/link time (step 2/3) to avoid a hint
// collision breaking an existing login.
func updatePrivyHints(ctx context.Context, tx pgx.Tx, userID int64, p identity.PrivyProfile) error {
	_, err := tx.Exec(ctx,
		`UPDATE users SET
		     wallet_address  = COALESCE(NULLIF($2,''), wallet_address),
		     wallet_provider = COALESCE(NULLIF($3,''), wallet_provider),
		     display_name    = COALESCE(NULLIF($4,''), display_name),
		     avatar_url      = COALESCE(NULLIF($5,''), avatar_url),
		     updated_at      = now()
		 WHERE id = $1`,
		userID, p.WalletAddress, p.WalletProvider, p.DisplayName, p.AvatarURL)
	return err
}

// ensureUserWallet opens the owner's treasury wallet if absent (deposits land
// here). Idempotent — safe to call on every login.
func ensureUserWallet(ctx context.Context, tx pgx.Tx, userID int64) error {
	_, err := tx.Exec(ctx,
		`INSERT INTO wallets (user_id, kind, balance)
		 SELECT $1, 'user', 0 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE user_id = $1)`,
		userID)
	return err
}

// CreatePlatformAgent creates an agent under an EXISTING owner, creating no user account.
//
// Every other path in this file mints a user and an agent together, because every other path
// is somebody signing up. The platform's own benchmark agents are not somebody: there is no
// person behind them, no email that should receive mail, and no password that should exist.
// Minting a throwaway account per benchmark seat produced exactly what you would expect —
// `lab+78611-0@pyyol.test` rows sitting in the users table looking like developers.
//
// So they hang off `usr_system`, the same owner the house bots already use (migration 0017).
// One platform identity, no credentials, and a users table that only ever contains people.
func (r *IdentityRepo) CreatePlatformAgent(ctx context.Context, in identity.PlatformAgentInput) (identity.Agent, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return identity.Agent{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// The owner must already exist. Created here on demand it would be a second place that
	// defines the system identity, and the two would disagree the first time one changed.
	var ownerID int64
	if err := tx.QueryRow(ctx,
		`SELECT id FROM users WHERE public_id = $1`, in.OwnerPublicID).Scan(&ownerID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return identity.Agent{}, fmt.Errorf("platform owner %q does not exist", in.OwnerPublicID)
		}
		return identity.Agent{}, err
	}

	l := in.Limits
	var agentID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug, description, framework,
		     status, verification_level, coin_limit_per_match, daily_loss_limit, session_loss_limit,
		     min_wallet_balance, max_concurrent_matches, cooldown_losses, cooldown_seconds, max_bid,
		     auto_join, kind)
		 VALUES ($1,$2,$3,$4,$5,$6,'unverified','new',$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		 RETURNING id`,
		in.AgentPublicID, ownerID, in.AgentName, in.AgentSlug, nullString(in.Description),
		nullString(in.Framework), l.CoinLimitPerMatch, l.DailyLossLimit, l.SessionLossLimit,
		l.MinWalletBalance, l.MaxConcurrentMatches, l.CooldownLosses, l.CooldownSeconds,
		l.MaxBid, l.AutoJoin, in.Kind).Scan(&agentID); err != nil {
		return identity.Agent{}, err
	}

	// The key and the wallet, exactly as CreateAccount issues them. A benchmark agent
	// authenticates and holds a wallet like any other seat — what differs is who owns it and
	// which boards its results reach, not how it plays.
	if _, err := tx.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope, label)
		 VALUES ($1, $2, $3, 'agent', 'initial')`,
		agentID, in.KeyPrefix, in.KeyHash); err != nil {
		return identity.Agent{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO wallets (agent_id, kind, balance) VALUES ($1, 'agent', 0)`,
		agentID); err != nil {
		return identity.Agent{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identity.Agent{}, err
	}
	return identity.Agent{
		PublicID: in.AgentPublicID, OwnerPublicID: in.OwnerPublicID, Name: in.AgentName,
		Slug: in.AgentSlug, Description: in.Description, Status: "unverified", Limits: in.Limits,
	}, nil
}

func (r *IdentityRepo) UserStatus(ctx context.Context, userPublicID string) (string, error) {
	var status string
	err := r.db.QueryRow(ctx,
		`SELECT status FROM users WHERE public_id = $1`, userPublicID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", identity.ErrNotFound
		}
		return "", err
	}
	return status, nil
}

func (r *IdentityRepo) SetUserStatus(ctx context.Context, userPublicID, next string) (prev string, err error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := tx.QueryRow(ctx,
		`SELECT status FROM users WHERE public_id = $1 FOR UPDATE`, userPublicID).Scan(&prev); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", identity.ErrNotFound
		}
		return "", err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE users SET status = $2, updated_at = now() WHERE public_id = $1`,
		userPublicID, next); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return prev, nil
}

func (r *IdentityRepo) ListBannedUserIDs(ctx context.Context) ([]string, error) {
	rows, err := r.db.Query(ctx, `SELECT public_id FROM users WHERE status = 'banned'`)
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

func (r *IdentityRepo) AgentIDsByOwner(ctx context.Context, userPublicID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id FROM agents a
		 JOIN users u ON u.id = a.owner_user_id
		 WHERE u.public_id = $1`, userPublicID)
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
