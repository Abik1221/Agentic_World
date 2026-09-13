package identity

import (
	"context"
	"strings"

	"github.com/agent-arena/arena/internal/platform"
)

// AppleUpsertInput carries everything the repo needs to find-or-create the owner for
// a verified Apple identity. New-account fields are used only when a fresh account
// must be created. Mirrors GoogleUpsertInput / GitHubUpsertInput, keyed on apple_sub.
type AppleUpsertInput struct {
	AppleSub      string // Apple's stable subject (the link key)
	Email         string // verified Apple email or Hide My Email relay (may be empty)
	UserPublicID  string
	AgentPublicID string
	AgentName     string
	AgentSlug     string
	KeyPrefix     string
	KeyHash       string
	Limits        Limits
}

// AppleUpsertResult is what the repo returns after the upsert.
type AppleUpsertResult struct {
	UserPublicID  string
	AgentPublicID string
	AgentName     string
	Created       bool // true when a brand-new account was created (→ send to onboarding)
}

// AppleLoginResult is returned after a verified Apple sign-in.
type AppleLoginResult struct {
	DashboardToken string
	APIKey         string // empty: Apple signup does not mint an unused key
	AgentID        string
	AgentName      string
	UserPublicID   string
	Created        bool
}

// SignUpOrLoginApple find-or-creates an account for a verified Apple identity and
// returns a fresh dashboard session. `sub`/`email`/`name` come from a completed
// Apple code exchange + id_token verify (see auth.AppleVerifier). Name is only
// available on the first authorization (Apple form_post `user` field) — pass it
// when present so the default agent name is human; later logins may send "".
func (s *Service) SignUpOrLoginApple(ctx context.Context, sub, email, name string) (AppleLoginResult, error) {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return AppleLoginResult{}, errInvalid("apple subject is required")
	}
	normEmail := ""
	if e, ok := normalizeEmail(email); ok {
		normEmail = e
	}

	agentName := googleAgentName(name, normEmail)

	res, err := s.repo.UpsertAppleAccount(ctx, AppleUpsertInput{
		AppleSub:      sub,
		Email:         normEmail,
		UserPublicID:  platform.NewID(platform.PrefixUser),
		AgentPublicID: platform.NewID(platform.PrefixAgent),
		AgentName:     agentName,
		AgentSlug:     slugify(agentName),
		Limits:        DefaultLimits(),
	})
	if err != nil {
		return AppleLoginResult{}, err
	}
	if !res.Created {
		if err := s.rejectIfBanned(ctx, res.UserPublicID); err != nil {
			return AppleLoginResult{}, err
		}
	}
	dash, err := s.jwt.Issue(res.UserPublicID)
	if err != nil {
		return AppleLoginResult{}, err
	}
	return AppleLoginResult{
		DashboardToken: dash,
		AgentID:        res.AgentPublicID,
		AgentName:      res.AgentName,
		UserPublicID:   res.UserPublicID,
		Created:        res.Created,
	}, nil
}
