// Package identity owns users, agents, scoped API keys, and the X-claim
// onboarding flow. It implements auth.KeyResolver so the auth layer can resolve
// agent keys without importing this package's internals.
package identity

import "time"

// Limits are the seven server-enforced spending limits. They live on the agent
// and are mutable ONLY by the owner (user scope) — never by an agent key.
type Limits struct {
	CoinLimitPerMatch    int64
	DailyLossLimit       int64
	SessionLossLimit     int64
	MinWalletBalance     int64
	MaxBid               int64
	MaxConcurrentMatches int
	CooldownLosses       int
	CooldownSeconds      int
	AutoJoin             bool
}

// DefaultLimits are what a brand-new agent is created with.
//
// THIS is the value that matters, not the column default — every INSERT passes these
// explicitly, so migration 0070's `ALTER COLUMN … SET DEFAULT` alone changed nothing for a
// real signup. The two are kept in step because the schema default is the safety net for any
// future writer that omits a column.
//
// A NEW AGENT MUST BE ABLE TO ENTER THE CHEAPEST RANKED TABLE. It could not: the per-match
// limit was 100 while the cheapest paid stake is 500 coins (migration 0038 seeds Low at 100,
// and the $5 minimum-stake floor — gamestakes.DefaultMinStakeUSDCents at a 1¢ peg — lifts
// every paid tier to at least 500). So the very first ranked join every developer attempted
// was refused with "Bid 500 exceeds the per-match limit of 100". Nobody could compete for
// real out of the box. The floor is deliberate economic policy; these numbers were simply
// set before it existed and never revisited.
//
// Derived from that floor rather than picked, so the relationship is visible:
//   - CoinLimitPerMatch 500: exactly one minimum-stake table.
//   - MaxBid 500: the same, so the two cannot contradict each other.
//   - DailyLossLimit 2000: four losses at the minimum stake before the day stops.
//   - SessionLossLimit 1000: two, so a bad session halts sooner than a bad day.
//
// MinWalletBalance stays 50 — a reserve is meant to be small, and it is the one guardrail
// that was never in conflict with anything.
//
// See migration 0070 for why EXISTING agents are deliberately left alone: a stored 100
// cannot be distinguished from a deliberate 100, and widening someone's risk limit without
// being asked is the one direction that is never safe.
func DefaultLimits() Limits {
	return Limits{
		CoinLimitPerMatch: 500, DailyLossLimit: 2000, SessionLossLimit: 1000,
		MinWalletBalance: 50, MaxBid: 500, MaxConcurrentMatches: 1,
		CooldownLosses: 3, CooldownSeconds: 300, AutoJoin: false,
	}
}

// Validate guards against nonsensical limit values before they reach the DB.
func (l Limits) Validate() error {
	switch {
	case l.CoinLimitPerMatch <= 0:
		return errInvalid("coin_limit_per_match must be > 0")
	case l.MaxBid <= 0:
		return errInvalid("max_bid must be > 0")
	case l.MaxBid > l.CoinLimitPerMatch:
		return errInvalid("max_bid cannot exceed coin_limit_per_match")
	case l.MinWalletBalance < 0:
		return errInvalid("min_wallet_balance must be >= 0")
	case l.MaxConcurrentMatches < 1:
		return errInvalid("max_concurrent_matches must be >= 1")
	case l.CooldownLosses < 0 || l.CooldownSeconds < 0:
		return errInvalid("cooldown values must be >= 0")
	}
	return nil
}

// User is the accountable human owner.
type User struct {
	PublicID string
	XHandle  string
	XUserID  string
}

// Agent is a competitor owned by a user.
type Agent struct {
	PublicID          string
	OwnerPublicID     string
	Name              string
	Slug              string
	Description       string
	Framework         string
	Status            string
	VerificationLevel string
	Limits            Limits
}

// Claim is an in-flight X-claim onboarding token.
type Claim struct {
	Token       string
	AgentName   string
	Description string
	Status      string
	ExpiresAt   time.Time
}

// KeyRecord is the data needed to authenticate a presented API key. Returned by
// the repo (implemented in internal/store), hence exported.
type KeyRecord struct {
	AgentPublicID string
	OwnerPublicID string
	Hash          string
}

// KeyInfo is one agent key for the management UI. The secret is never included —
// only the public prefix, ownership, label, and audit timestamps.
//
// Label is the machine or deployment holding the key ("macbook-pro", "ci-runner",
// "fly-io"). It is always present in the JSON, empty for keys minted before
// migration 0071 — the UI must render "" as unnamed rather than hiding the row,
// because an unnamed key is still a live credential the owner may want to revoke.
type KeyInfo struct {
	Prefix        string     `json:"prefix"`
	AgentPublicID string     `json:"agent"`
	Label         string     `json:"label"`
	CreatedAt     time.Time  `json:"created_at"`
	LastUsedAt    *time.Time `json:"last_used_at,omitempty"`
	RevokedAt     *time.Time `json:"revoked_at,omitempty"`
}

// AgentProfile is an agent's public display identity (migration 0019:
// agents.display_name / bio / avatar_url), mutable by the owner (user scope).
type AgentProfile struct {
	AgentPublicID string
	DisplayName   string
	Bio           string
	AvatarURL     string
}

// MagicLink is the owner (and their agent) resolved by consuming a single-use
// passwordless sign-in token.
type MagicLink struct {
	UserPublicID  string
	AgentPublicID string // empty if the owner has no agent yet
}
