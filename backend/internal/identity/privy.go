package identity

import (
	"context"
	"strings"

	"github.com/agent-arena/arena/internal/platform"
)

// PrivyProfile is the display data the client attaches at Privy login (email,
// connected/embedded wallet, social display fields). It is NON-AUTHORITATIVE:
// only the Privy user id is cryptographically proven (see auth.PrivyVerifier).
// Wallet addresses stored here are login hints; a withdrawal destination is
// re-validated before any payout (Beta wallet pipeline P3).
type PrivyProfile struct {
	Email          string
	WalletAddress  string
	WalletProvider string // phantom|solflare|backpack|privy(embedded)|...
	DisplayName    string
	AvatarURL      string
}

// PrivyUpsertInput carries everything the repo needs to find-or-create the owner
// for a verified Privy identity in one atomic call.
type PrivyUpsertInput struct {
	PrivyUserID  string
	UserPublicID string // used ONLY when a new user must be created
	Profile      PrivyProfile
}

// PrivyLoginResult is returned after a successful Privy token exchange.
type PrivyLoginResult struct {
	DashboardToken string
	UserPublicID   string
	Created        bool
}

// UpsertFromPrivy exchanges a verified Privy identity for a dashboard session: it
// find-or-creates the owner keyed STRICTLY on the cryptographically-proven Privy
// user id, opens their treasury wallet, stores the login profile hints, and mints
// the existing user-scope JWT. No agent is created — a Privy user is a first-class
// owner who becomes a developer later, on demand.
//
// SECURITY: linking is by privy_user_id ONLY. A new Privy user is inserted with a
// NULL email and privy_user_id as the sole unique key; the client-supplied email
// is a non-authoritative display hint and is NEVER used to attach the token to a
// pre-existing account. Do NOT reintroduce "link an existing same-email account" —
// that would let anyone with a Privy token for any email take over the matching
// account. The privyUserID must already be verified by auth.PrivyVerifier.
func (s *Service) UpsertFromPrivy(ctx context.Context, privyUserID string, p PrivyProfile) (PrivyLoginResult, error) {
	privyUserID = strings.TrimSpace(privyUserID)
	if privyUserID == "" {
		return PrivyLoginResult{}, errInvalid("privy user id is required")
	}
	// Email is stored only if it is a valid address; a bad hint is dropped, not
	// rejected (Privy already authenticated the user — we don't block login on it).
	if e, ok := normalizeEmail(p.Email); ok {
		p.Email = e
	} else {
		p.Email = ""
	}
	p.WalletAddress = strings.TrimSpace(p.WalletAddress)
	p.WalletProvider = strings.TrimSpace(p.WalletProvider)
	p.DisplayName = strings.TrimSpace(p.DisplayName)
	p.AvatarURL = strings.TrimSpace(p.AvatarURL)

	userPublicID, created, err := s.repo.UpsertUserFromPrivy(ctx, PrivyUpsertInput{
		PrivyUserID:  privyUserID,
		UserPublicID: platform.NewID(platform.PrefixUser),
		Profile:      p,
	})
	if err != nil {
		return PrivyLoginResult{}, err
	}
	if !created {
		if err := s.rejectIfBanned(ctx, userPublicID); err != nil {
			return PrivyLoginResult{}, err
		}
	}
	dash, err := s.jwt.Issue(userPublicID)
	if err != nil {
		return PrivyLoginResult{}, err
	}
	return PrivyLoginResult{DashboardToken: dash, UserPublicID: userPublicID, Created: created}, nil
}
