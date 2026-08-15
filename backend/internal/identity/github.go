package identity

import (
	"context"
	"strings"

	"github.com/agent-arena/arena/internal/platform"
)

// GitHubUpsertInput carries everything the repo needs to find-or-create the owner for
// a verified GitHub identity. New-account fields are used only when a fresh account
// must be created. Mirrors GoogleUpsertInput, keyed on the stable GitHub numeric id.
type GitHubUpsertInput struct {
	GitHubID      string // GitHub's stable numeric user id (the link key)
	Email         string // verified GitHub primary email (may be empty)
	UserPublicID  string
	AgentPublicID string
	AgentName     string
	AgentSlug     string
	KeyPrefix     string
	KeyHash       string
	Limits        Limits
}

// GitHubUpsertResult is what the repo returns after the upsert.
type GitHubUpsertResult struct {
	UserPublicID  string
	AgentPublicID string
	AgentName     string
	Created       bool // true when a brand-new account was created (→ send to onboarding)
}

// GitHubLoginResult is returned after a verified GitHub sign-in.
type GitHubLoginResult struct {
	DashboardToken string
	APIKey         string // the raw agent key — ONLY on a newly-created account, shown once
	AgentID        string
	AgentName      string
	UserPublicID   string
	Created        bool
}

// SignUpOrLoginGitHub find-or-creates an account for a verified GitHub identity and
// returns a fresh dashboard session. `id`/`login`/`email`/`name` come from a completed
// GitHub OAuth exchange (see auth.GitHubVerifier). A new account also gets an agent +
// first key. Deliberately parallel to SignUpOrLoginGoogle.
func (s *Service) SignUpOrLoginGitHub(ctx context.Context, id, login, email, name string) (GitHubLoginResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return GitHubLoginResult{}, errInvalid("github id is required")
	}
	// A blank email is allowed through (GitHub may withhold it, and `id` is the real
	// link key); a malformed one is dropped rather than stored, exactly as for Google,
	// so a rejected address never becomes an invisible, unmergeable NULL row.
	normEmail := ""
	if e, ok := normalizeEmail(email); ok {
		normEmail = e
	}

	// Reuse Google's name derivation: same rules (letters/digits/_/-, 3–32 chars,
	// fall back to the email local-part then a random handle). Seed it with the GitHub
	// login, which is already a valid handle in the common case.
	agentName := githubAgentName(login, name, normEmail)

	key, err := generateKey(s.pepper)
	if err != nil {
		return GitHubLoginResult{}, err
	}
	res, err := s.repo.UpsertGitHubAccount(ctx, GitHubUpsertInput{
		GitHubID:      id,
		Email:         normEmail,
		UserPublicID:  platform.NewID(platform.PrefixUser),
		AgentPublicID: platform.NewID(platform.PrefixAgent),
		AgentName:     agentName,
		AgentSlug:     slugify(agentName),
		KeyPrefix:     key.Prefix,
		KeyHash:       key.Hash,
		Limits:        DefaultLimits(),
	})
	if err != nil {
		return GitHubLoginResult{}, err
	}
	dash, err := s.jwt.Issue(res.UserPublicID)
	if err != nil {
		return GitHubLoginResult{}, err
	}
	out := GitHubLoginResult{
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

// githubAgentName derives a valid default agent name, preferring the GitHub login,
// then the display name, then the email local-part. Reuses googleAgentName for the
// sanitisation/fallback rules so both providers produce identically-shaped names.
func githubAgentName(login, name, email string) string {
	if seed := strings.TrimSpace(login); seed != "" {
		return googleAgentName(seed, email)
	}
	return googleAgentName(name, email)
}
