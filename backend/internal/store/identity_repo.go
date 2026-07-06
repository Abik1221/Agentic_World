package store

import (
	"context"
	"errors"
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

	// 3. Issue the first API key.
	if _, err = tx.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope) VALUES ($1, $2, $3, 'agent')`,
		agentID, in.KeyPrefix, in.KeyHash); err != nil {
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

	// 3. Issue the first API key.
	if _, err = tx.Exec(ctx,
		`INSERT INTO agent_keys (agent_id, key_prefix, key_hash, scope) VALUES ($1, $2, $3, 'agent')`,
		agentID, in.KeyPrefix, in.KeyHash); err != nil {
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
