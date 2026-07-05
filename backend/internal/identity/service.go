package identity

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
)

// Service is the identity/onboarding application service. It implements
// auth.KeyResolver.
type Service struct {
	repo     Repo
	verifier ClaimVerifier
	captcha  Captcha
	jwt      *auth.JWT
	clock    platform.Clock
	pepper   string
	claimTTL time.Duration
}

func New(repo Repo, verifier ClaimVerifier, captcha Captcha, jwt *auth.JWT, clock platform.Clock, pepper string, claimTTL time.Duration) *Service {
	return &Service{repo: repo, verifier: verifier, captcha: captcha, jwt: jwt, clock: clock, pepper: pepper, claimTTL: claimTTL}
}

// Register starts onboarding: it issues a claim token the human posts publicly.
func (s *Service) Register(ctx context.Context, name, description string) (Claim, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Claim{}, errInvalid("agent_name is required")
	}
	c := Claim{
		Token:       newClaimToken(),
		AgentName:   name,
		Description: strings.TrimSpace(description),
		Status:      "pending",
		ExpiresAt:   s.clock.Now().Add(s.claimTTL),
	}
	if err := s.repo.CreateClaim(ctx, c); err != nil {
		return Claim{}, err
	}
	return c, nil
}

// VerifyResult is returned once a claim is verified.
type VerifyResult struct {
	APIKey         string // shown exactly once
	AgentID        string
	DashboardToken string // user-scope JWT for the owner
}

// VerifyClaim checks the captcha and the public claim post; on success it creates
// the user (find-or-create), the agent (default limits), and the first API key,
// then returns the key + a dashboard token. Polled by the agent until verified.
func (s *Service) VerifyClaim(ctx context.Context, token, captchaToken, remoteIP string) (VerifyResult, error) {
	claim, err := s.repo.GetClaim(ctx, token)
	if err != nil {
		return VerifyResult{}, ErrNotFound
	}
	switch claim.Status {
	case "verified":
		return VerifyResult{}, httpx.NewError(http.StatusConflict, "claim_already_used", "This claim was already verified; the key was issued once.")
	case "expired":
		return VerifyResult{}, ErrClaimExpired
	}
	if s.clock.Now().After(claim.ExpiresAt) {
		return VerifyResult{}, ErrClaimExpired
	}

	if err := s.captcha.Verify(ctx, captchaToken, remoteIP); err != nil {
		return VerifyResult{}, err
	}

	xUserID, xHandle, err := s.verifier.VerifyClaim(ctx, token)
	if err != nil {
		// A "not found yet" is a normal polling state; bubble it through verbatim.
		var apiErr *httpx.APIError
		if errors.As(err, &apiErr) {
			return VerifyResult{}, apiErr
		}
		return VerifyResult{}, httpx.NewError(http.StatusBadGateway, "claim_verify_unavailable", "Could not verify the claim post right now; retry shortly.")
	}

	key, err := generateKey(s.pepper)
	if err != nil {
		return VerifyResult{}, err
	}

	agent, owner, err := s.repo.CompleteClaim(ctx, CompleteClaimInput{
		Token:         token,
		XUserID:       xUserID,
		XHandle:       xHandle,
		AgentPublicID: platform.NewID(platform.PrefixAgent),
		AgentName:     claim.AgentName,
		AgentSlug:     slugify(claim.AgentName),
		Description:   claim.Description,
		KeyPrefix:     key.Prefix,
		KeyHash:       key.Hash,
		UserPublicID:  platform.NewID(platform.PrefixUser),
		Limits:        DefaultLimits(),
	})
	if err != nil {
		return VerifyResult{}, err
	}

	dash, err := s.jwt.Issue(owner.PublicID)
	if err != nil {
		return VerifyResult{}, err
	}
	return VerifyResult{APIKey: key.Raw, AgentID: agent.PublicID, DashboardToken: dash}, nil
}

// SignUpResult is returned when a new email+password account is created. The API
// key is shown exactly once, mirroring the X-claim VerifyResult.
type SignUpResult struct {
	APIKey         string
	AgentID        string
	AgentName      string
	DashboardToken string
}

// SignUp creates an owner from an email + password plus their first agent, in one
// atomic call, and returns a fresh dashboard session and one-time API key. This
// is the "normal" account-creation path that sits beside X-claim onboarding.
func (s *Service) SignUp(ctx context.Context, email, password, agentName, description string) (SignUpResult, error) {
	normEmail, ok := normalizeEmail(email)
	if !ok {
		return SignUpResult{}, errInvalid("a valid email is required")
	}
	if err := validatePassword(password); err != nil {
		return SignUpResult{}, err
	}
	agentName = strings.TrimSpace(agentName)
	if !validAgentName(agentName) {
		return SignUpResult{}, errInvalid("agent name must be 3–32 characters: letters, digits, _ or -")
	}

	pwHash, err := hashPassword(password, s.pepper)
	if err != nil {
		return SignUpResult{}, err
	}
	key, err := generateKey(s.pepper)
	if err != nil {
		return SignUpResult{}, err
	}

	agent, owner, err := s.repo.CreateAccount(ctx, CreateAccountInput{
		Email:         normEmail,
		PasswordHash:  pwHash,
		UserPublicID:  platform.NewID(platform.PrefixUser),
		AgentPublicID: platform.NewID(platform.PrefixAgent),
		AgentName:     agentName,
		AgentSlug:     slugify(agentName),
		Description:   strings.TrimSpace(description),
		KeyPrefix:     key.Prefix,
		KeyHash:       key.Hash,
		Limits:        DefaultLimits(),
	})
	if err != nil {
		return SignUpResult{}, err
	}

	dash, err := s.jwt.Issue(owner.PublicID)
	if err != nil {
		return SignUpResult{}, err
	}
	return SignUpResult{APIKey: key.Raw, AgentID: agent.PublicID, AgentName: agent.Name, DashboardToken: dash}, nil
}

// LoginResult is returned on a successful email + password login. No API key is
// returned (it is shown only once, at sign-up); the owner rotates it from the
// dashboard if they need it again.
type LoginResult struct {
	DashboardToken string
	AgentID        string
	AgentName      string
}

// LogIn authenticates an email + password and mints a fresh dashboard session.
// Unknown email and wrong password collapse to the same opaque error, and the
// unknown-email path still pays the bcrypt cost so response time does not reveal
// which emails are registered.
func (s *Service) LogIn(ctx context.Context, email, password string) (LoginResult, error) {
	normEmail, ok := normalizeEmail(email)
	if !ok {
		equalizeTiming(password, s.pepper)
		return LoginResult{}, ErrInvalidCredentials
	}
	rec, err := s.repo.CredentialsByEmail(ctx, normEmail)
	if err != nil {
		equalizeTiming(password, s.pepper)
		return LoginResult{}, ErrInvalidCredentials
	}
	if !verifyPassword(rec.PasswordHash, password, s.pepper) {
		return LoginResult{}, ErrInvalidCredentials
	}
	dash, err := s.jwt.Issue(rec.UserPublicID)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{DashboardToken: dash, AgentID: rec.AgentPublicID, AgentName: rec.AgentName}, nil
}

// RotateKey issues a fresh API key for an agent the caller owns. The raw key is
// returned once; existing keys remain valid until explicitly revoked.
func (s *Service) RotateKey(ctx context.Context, ownerPublicID, agentPublicID string) (string, error) {
	if _, err := s.repo.AgentByOwner(ctx, agentPublicID, ownerPublicID); err != nil {
		return "", ErrForbiddenOwner
	}
	key, err := generateKey(s.pepper)
	if err != nil {
		return "", err
	}
	if err := s.repo.InsertKey(ctx, agentPublicID, ownerPublicID, key.Prefix, key.Hash); err != nil {
		return "", err
	}
	return key.Raw, nil
}

// RevokeKey revokes one of the caller's keys by its public prefix.
func (s *Service) RevokeKey(ctx context.Context, ownerPublicID, prefix string) error {
	return s.repo.RevokeKey(ctx, ownerPublicID, prefix)
}

// UpdateConfig writes an agent's spending limits. Only the owner (user scope)
// reaches this; the route guard enforces that, this enforces ownership + validity.
func (s *Service) UpdateConfig(ctx context.Context, ownerPublicID, agentPublicID string, l Limits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	return s.repo.UpdateLimits(ctx, agentPublicID, ownerPublicID, l)
}

// SetSigningKey registers an agent's Ed25519 public key for per-move authenticity.
// Once set, the agent's moves must be signed (verified in the match flow).
func (s *Service) SetSigningKey(ctx context.Context, ownerPublicID, agentPublicID, pubkey string) error {
	if !movesig.ValidPublicKey(pubkey) {
		return ErrInvalidPubKey
	}
	return s.repo.SetSigningKey(ctx, agentPublicID, ownerPublicID, pubkey)
}

// ResolveAgentKey implements auth.KeyResolver: it authenticates a presented API
// key and returns its principal. Any failure collapses to a single opaque error
// (no oracle for attackers about which step failed).
func (s *Service) ResolveAgentKey(ctx context.Context, raw string) (*auth.Principal, error) {
	prefix, secret, err := splitKey(raw)
	if err != nil {
		return nil, ErrInvalidAPIKey
	}
	rec, err := s.repo.LiveKeyByPrefix(ctx, prefix)
	if err != nil {
		return nil, ErrInvalidAPIKey
	}
	if !verifySecret(rec.Hash, secret, s.pepper) {
		return nil, ErrInvalidAPIKey
	}
	_ = s.repo.TouchKey(ctx, prefix) // best-effort last-used tracking
	return &auth.Principal{
		Scope:         auth.ScopeAgent,
		UserPublicID:  rec.OwnerPublicID,
		AgentPublicID: rec.AgentPublicID,
	}, nil
}

// newClaimToken returns a human-postable token like "AA-7K3Q-9XF2".
func newClaimToken() string {
	seg := func() string {
		b := make([]byte, 3)
		_, _ = rand.Read(b)
		return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b)[:4]
	}
	return "AA-" + seg() + "-" + seg()
}
