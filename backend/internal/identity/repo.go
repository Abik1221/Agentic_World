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

	// RevokedKeyByPrefix returns the most recently revoked key record for a lookup
	// prefix, or a non-nil error when there is none (the driver's no-rows error, as
	// with LiveKeyByPrefix — callers only distinguish nil from non-nil). Used ONLY to
	// tell a caller who still holds the correct secret that their key was revoked
	// rather than that it never existed.
	RevokedKeyByPrefix(ctx context.Context, prefix string) (KeyRecord, error)

	// TouchKey best-effort updates last_used_at for a prefix.
	TouchKey(ctx context.Context, prefix string) error

	// InsertKey adds a new key to an agent owned by ownerPublicID (ownership checked).
	InsertKey(ctx context.Context, agentPublicID, ownerPublicID, prefix, hash string) error

	// IssueKey atomically revokes the agent's live key with this label (if any) and
	// inserts the replacement, refusing with ErrTooManyKeys when the agent would end
	// up with more than maxLive live keys. Keys with OTHER labels are never touched —
	// that is the whole point: a laptop and a deployment hold different labels and
	// coexist. Ownership is re-checked inside the transaction.
	//
	// One exception: a NEVER-USED 'initial' key (the one shown once at sign-up, which
	// most developers do not save) is also revoked, so the first real login reclaims
	// that slot instead of leaving a live credential nobody holds. An 'initial' key
	// that HAS authenticated is somebody's deployment and is left alone.
	IssueKey(ctx context.Context, agentPublicID, ownerPublicID, prefix, hash, label string, maxLive int) error

	// RevokeKey revokes a key by its prefix if it belongs to an agent the user owns.
	RevokeKey(ctx context.Context, ownerPublicID, prefix string) error

	// ListKeys returns the owner's agent keys (newest first) for the key-management
	// UI, so keys can be audited by last-used and revoked by prefix.
	ListKeys(ctx context.Context, ownerPublicID string) ([]KeyInfo, error)

	// UpdateLimits writes the agent's limits iff ownerPublicID owns the agent.
	UpdateLimits(ctx context.Context, agentPublicID, ownerPublicID string, l Limits) error

	// AgentByOwner returns an agent iff owned by ownerPublicID.
	AgentByOwner(ctx context.Context, agentPublicID, ownerPublicID string) (Agent, error)

	// PrimaryAgentOf returns the owner's first (oldest) non-house agent's public id, or ""
	// when they have none.
	//
	// Lets an authenticated request omit agent_id. The dashboard used to have to supply it
	// from a cookie written at login, so a developer on a second device — or one who had
	// cleared cookies, or whose session was restored from a refresh token — could not save
	// their own guardrails: the request went out with an empty id and came back 400.
	// Ordered by creation, so "primary" does not move when a second agent is added.
	PrimaryAgentOf(ctx context.Context, ownerPublicID string) (string, error)

	// SetSigningKey registers the agent's Ed25519 move-signing public key iff
	// ownerPublicID owns the agent.
	SetSigningKey(ctx context.Context, agentPublicID, ownerPublicID, pubkey string) error

	// CreateAccount atomically (in one DB transaction) creates an email+password
	// owner, their first agent (default limits), the owner + agent wallets, and
	// optionally the agent's first API key. Ordinary developer signup omits the
	// key. Returns ErrEmailTaken if the email already
	// belongs to an account. This is the email/password analogue of CompleteClaim.
	CreateAccount(ctx context.Context, in CreateAccountInput) (Agent, User, error)
	// CreatePlatformAgent attaches an agent to an EXISTING owner and creates no user.
	// See PlatformAgentInput for why the platform's own agents must not mint accounts.
	CreatePlatformAgent(ctx context.Context, in PlatformAgentInput) (Agent, error)

	// UpsertGoogleAccount find-or-creates the account for a verified Google identity.
	// Existing google_sub → returns that user + its agent (created=false). Else if a
	// user with the (verified) email exists and is unlinked, links google_sub to it.
	// Else creates a fresh user + agent + treasury wallet (created=true).
	UpsertGoogleAccount(ctx context.Context, in GoogleUpsertInput) (GoogleUpsertResult, error)

	// UpsertGitHubAccount is the GitHub analogue of UpsertGoogleAccount, keyed on the
	// stable GitHub numeric id (github_id). Same three-step logic: existing link → log
	// in; verified-email match on an unlinked account → link; else create fresh.
	UpsertGitHubAccount(ctx context.Context, in GitHubUpsertInput) (GitHubUpsertResult, error)

	// CredentialsByEmail returns the auth record for a password-enabled account,
	// or ErrNotFound if the email is unknown or has no password set.
	CredentialsByEmail(ctx context.Context, email string) (AuthRecord, error)

	// UserStatus is the account lifecycle (active|suspended|banned). Empty / not
	// found is treated as active by callers that fail open on a missing row.
	UserStatus(ctx context.Context, userPublicID string) (string, error)

	// SetUserStatus writes the lifecycle and returns the status it actually held
	// before. Used by Super Admin ban/unban so the arena — not the admin mirror —
	// is what login and session middleware consult.
	SetUserStatus(ctx context.Context, userPublicID, next string) (prev string, err error)

	// ListBannedUserIDs warms the in-process ban index after a restart.
	ListBannedUserIDs(ctx context.Context) ([]string, error)

	// AgentIDsByOwner lists every agent the user owns, so a ban can kick live
	// sockets instead of leaving them playing until the next heartbeat.
	AgentIDsByOwner(ctx context.Context, userPublicID string) ([]string, error)

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

	// UpsertUserFromPrivy find-or-creates the owner for a verified Privy identity,
	// keyed on privy_user_id (linking an existing same-email account that has no
	// Privy id yet), opens their treasury wallet, and stores the login profile
	// hints. Returns the owner's public id and whether a new user was created.
	// in.UserPublicID is used ONLY when inserting a new user. No agent is created.
	UpsertUserFromPrivy(ctx context.Context, in PrivyUpsertInput) (userPublicID string, created bool, err error)
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
	// Kind is the agent kind to create (see KindExternal / KindHarness). Empty means
	// KindExternal, so every existing caller keeps creating developer agents unchanged.
	//
	// It is an input to CREATION and there is no counterpart on any update path, which is
	// deliberate: the kind decides which surfaces already-written matches belong to, so
	// changing it afterwards repairs the label without moving the evidence.
	Kind string
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

// PlatformAgentInput creates an agent under an existing owner, with NO user account.
//
// Distinct from CreateAccountInput by exactly what it lacks: no email, no password hash, no
// user public id to mint. That absence is the point — the platform's benchmark agents have
// no person behind them, and giving each one a throwaway login is how `lab+…@pyyol.test`
// rows ended up in the users table looking like developers.
type PlatformAgentInput struct {
	// OwnerPublicID must already exist. `usr_system` (migration 0017) is the platform
	// identity the house bots already hang off.
	OwnerPublicID string
	AgentPublicID string
	AgentName     string
	AgentSlug     string
	Description   string
	Framework     string
	KeyPrefix     string
	KeyHash       string
	Limits        Limits
	// Kind is the platform kind being created. Never KindExternal: a developer's agent has
	// an owner who is a person, and routing one through here would hide it under the system
	// account where its owner could never reach it.
	Kind string
}
