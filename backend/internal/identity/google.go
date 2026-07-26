package identity

import (
	"context"
	"fmt"
	"strings"

	"github.com/agent-arena/arena/internal/platform"
)

// GoogleUpsertInput carries everything the repo needs to find-or-create the owner
// for a verified Google identity. New-account fields (public ids, agent, key) are
// used only when a fresh account must be created.
type GoogleUpsertInput struct {
	GoogleSub     string // Google's stable subject id (the link key)
	Email         string // verified Google email
	UserPublicID  string
	AgentPublicID string
	AgentName     string
	AgentSlug     string
	KeyPrefix     string
	KeyHash       string
	Limits        Limits
}

// GoogleUpsertResult is what the repo returns after the upsert.
type GoogleUpsertResult struct {
	UserPublicID  string
	AgentPublicID string
	AgentName     string
	Created       bool // true when a brand-new account was created (→ send to onboarding)
}

// GoogleLoginResult is returned after a verified Google sign-in.
type GoogleLoginResult struct {
	DashboardToken string
	APIKey         string // the raw agent key — ONLY on a newly-created account, shown once
	AgentID        string
	AgentName      string
	UserPublicID   string
	Created        bool
}

// SignUpOrLoginGoogle find-or-creates an account for a verified Google identity and
// returns a fresh dashboard session. `sub`/`email`/`name` come from a verified GIS
// ID token (see auth.GoogleVerifier). A new account also gets an agent + first key.
func (s *Service) SignUpOrLoginGoogle(ctx context.Context, sub, email, name string) (GoogleLoginResult, error) {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return GoogleLoginResult{}, errInvalid("google subject is required")
	}
	normEmail, _ := normalizeEmail(email) // best-effort; Google emails are already valid

	key, err := generateKey(s.pepper)
	if err != nil {
		return GoogleLoginResult{}, err
	}
	res, err := s.repo.UpsertGoogleAccount(ctx, GoogleUpsertInput{
		GoogleSub:     sub,
		Email:         normEmail,
		UserPublicID:  platform.NewID(platform.PrefixUser),
		AgentPublicID: platform.NewID(platform.PrefixAgent),
		AgentName:     googleAgentName(name, normEmail),
		AgentSlug:     slugify(googleAgentName(name, normEmail)),
		KeyPrefix:     key.Prefix,
		KeyHash:       key.Hash,
		Limits:        DefaultLimits(),
	})
	if err != nil {
		return GoogleLoginResult{}, err
	}
	dash, err := s.jwt.Issue(res.UserPublicID)
	if err != nil {
		return GoogleLoginResult{}, err
	}
	out := GoogleLoginResult{
		DashboardToken: dash,
		AgentID:        res.AgentPublicID,
		AgentName:      res.AgentName,
		UserPublicID:   res.UserPublicID,
		Created:        res.Created,
	}
	if res.Created {
		out.APIKey = key.Raw // the new account's first key, surfaced once
	}
	return out, nil
}

// googleAgentName derives a valid default agent name from the Google display name or
// email local-part, falling back to a stable random-ish handle.
func googleAgentName(name, email string) string {
	cand := strings.TrimSpace(name)
	if cand == "" && email != "" {
		if at := strings.IndexByte(email, '@'); at > 0 {
			cand = email[:at]
		}
	}
	// keep letters/digits/_/-; collapse the rest to '-'
	var b strings.Builder
	for _, r := range cand {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) < 3 {
		out = fmt.Sprintf("player-%s", platform.NewID(platform.PrefixAgent)[:6])
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return out
}
