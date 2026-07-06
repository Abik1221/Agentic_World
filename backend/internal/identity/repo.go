package identity

import (
	"context"
	"time"
)

// Repo is identity's persistence port. The concrete pgx implementation lives in
// internal/store (the only package that touches the DB driver), keeping this
// module testable with a fake and the data layer swappable.
type Repo interface {
	// CreateClaim persists a new pending onboarding claim.
	CreateClaim(ctx context.Context, c Claim) error

	// GetClaim returns the claim for a token, or ErrNotFound.
	GetClaim(ctx context.Context, token string) (Claim, error)

	// CompleteClaim atomically (in one DB transaction): finds-or-creates the user
	// for the verified X identity, creates the agent with default limits, inserts
	// the first API key, and marks the claim verified. Returns the created agent
	// and owner. Idempotent on an already-verified claim (returns the existing agent).
	CompleteClaim(ctx context.Context, in CompleteClaimInput) (Agent, User, error)

	// LiveKeyByPrefix returns the live (non-revoked) key record for a lookup prefix.
	LiveKeyByPrefix(ctx context.Context, prefix string) (KeyRecord, error)

	// TouchKey best-effort updates last_used_at for a prefix.
	TouchKey(ctx context.Context, prefix string) error

	// InsertKey adds a new key to an agent owned by ownerPublicID (ownership checked).
	InsertKey(ctx context.Context, agentPublicID, ownerPublicID, prefix, hash string) error

	// RevokeKey revokes a key by its prefix if it belongs to an agent the user owns.
	RevokeKey(ctx context.Context, ownerPublicID, prefix string) error

	// UpdateLimits writes the agent's limits iff ownerPublicID owns the agent.
	UpdateLimits(ctx context.Context, agentPublicID, ownerPublicID string, l Limits) error

	// AgentByOwner returns an agent iff owned by ownerPublicID.
	AgentByOwner(ctx context.Context, agentPublicID, ownerPublicID string) (Agent, error)

	// SetSigningKey registers the agent's Ed25519 move-signing public key iff
	// ownerPublicID owns the agent.
	SetSigningKey(ctx context.Context, agentPublicID, ownerPublicID, pubkey string) error

	// CreateAccount atomically (in one DB transaction) creates an email+password
	// owner, their first agent (default limits), the owner + agent wallets, and
	// the agent's first API key. Returns ErrEmailTaken if the email already
	// belongs to an account. This is the email/password analogue of CompleteClaim.
	CreateAccount(ctx context.Context, in CreateAccountInput) (Agent, User, error)

	// CredentialsByEmail returns the auth record for a password-enabled account,
	// or ErrNotFound if the email is unknown or has no password set.
	CredentialsByEmail(ctx context.Context, email string) (AuthRecord, error)

	// UpdateAgentProfile writes the owner's agent display identity (nil fields are
	// left unchanged) and returns the saved profile. Returns ErrForbiddenOwner if
	// the caller owns no agent.
	UpdateAgentProfile(ctx context.Context, ownerPublicID string, displayName, bio, avatarURL *string) (AgentProfile, error)

	// AgentProfileByOwner returns the owner's agent display identity, or
	// ErrForbiddenOwner if the caller owns no agent.
	AgentProfileByOwner(ctx context.Context, ownerPublicID string) (AgentProfile, error)

	// SetGameConfig upserts per-game behaviour (a JSON object) for the owner's
	// agent. Returns ErrForbiddenOwner if the caller owns no agent.
	SetGameConfig(ctx context.Context, ownerPublicID, game string, behavior []byte) error

	// CreateMagicLink stores a single-use sign-in token hash for the account with
	// this email, returning whether such an account existed. A missing email is
	// NOT an error (enumeration-safe: the caller behaves as if a link was sent).
	CreateMagicLink(ctx context.Context, tokenHash, email string, expiresAt time.Time) (bool, error)

	// ConsumeMagicLink atomically marks a valid (unconsumed, unexpired) token used
	// and returns its owner (+ agent), or ErrNotFound.
	ConsumeMagicLink(ctx context.Context, tokenHash string) (MagicLink, error)
}

// CreateAccountInput carries everything CreateAccount needs in one atomic call.
type CreateAccountInput struct {
	Email         string
	PasswordHash  string
	UserPublicID  string
	AgentPublicID string
	AgentName     string
	AgentSlug     string
	Description   string
	Framework     string
	KeyPrefix     string
	KeyHash       string
	Limits        Limits
}

// AuthRecord is the row needed to authenticate an email+password login, plus the
// owner's (single) agent so the session can be seeded with it.
type AuthRecord struct {
	UserPublicID  string
	PasswordHash  string
	AgentPublicID string // empty if the owner has no agent yet
	AgentName     string
}

// CompleteClaimInput carries everything CompleteClaim needs in one atomic call.
type CompleteClaimInput struct {
	Token         string
	XUserID       string
	XHandle       string
	AgentPublicID string
	AgentName     string
	AgentSlug     string
	Description   string
	Framework     string
	KeyPrefix     string
	KeyHash       string
	UserPublicID  string // used only when a new user must be created
	Limits        Limits
}
