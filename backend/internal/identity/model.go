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

// DefaultLimits mirror the column defaults in migration 0002.
func DefaultLimits() Limits {
	return Limits{
		CoinLimitPerMatch: 100, DailyLossLimit: 500, SessionLossLimit: 1000,
		MinWalletBalance: 50, MaxBid: 100, MaxConcurrentMatches: 1,
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
