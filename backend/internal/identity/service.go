package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/movesig"
	"github.com/agent-arena/arena/internal/platform"
)

// magicLinkTTL bounds how long a passwordless sign-in token stays valid.
const magicLinkTTL = 15 * time.Minute

// validGames is the set of games an agent can be tuned for (mirrors the
// agent_game_config.game_type CHECK constraint in migration 0019).
var validGames = map[string]bool{"mafia": true, "goofspiel": true}

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
	UserPublicID   string
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
	return VerifyResult{APIKey: key.Raw, AgentID: agent.PublicID, DashboardToken: dash, UserPublicID: owner.PublicID}, nil
}

// SignUpResult is returned when a new email+password account is created. The API
// key is shown exactly once, mirroring the X-claim VerifyResult.
type SignUpResult struct {
	APIKey         string
	AgentID        string
	AgentName      string
	DashboardToken string
	UserPublicID   string
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
	return SignUpResult{APIKey: key.Raw, AgentID: agent.PublicID, AgentName: agent.Name, DashboardToken: dash, UserPublicID: owner.PublicID}, nil
}

// LoginResult is returned on a successful email + password login. No API key is
// returned (it is shown only once, at sign-up); the owner rotates it from the
// dashboard if they need it again.
type LoginResult struct {
	DashboardToken string
	AgentID        string
	AgentName      string
	UserPublicID   string
}

// LogIn authenticates an email + password and mints a fresh dashboard session.
// Unknown email and wrong password collapse to the same opaque error, and the
// unknown-email path still pays the bcrypt cost so response time does not reveal
// which emails are registered.
func (s *Service) LogIn(ctx context.Context, email, password string) (LoginResult, error) {
	// A login address that fails PUBLIC-SIGNUP validation is still looked up literally.
	//
	// normalizeEmail requires a dotted domain, which is the right rule for sign-up — it keeps
	// "user@localhost" out of a public form. But it was applied as a gate on LOGGING IN too,
	// with an early return before any lookup, so an account whose address does not satisfy it
	// could never sign in at all. That is exactly the shape of an internal operator login
	// (`pyyol@admin`), created by cmd/seed-admin rather than through the form: the row was
	// perfect and the sign-in was impossible.
	//
	// This admits no new account. Sign-up validation is untouched, so an address like that
	// still cannot be REGISTERED through the API; the only way one exists is if an operator
	// seeded it deliberately. All this does is let an existing row be found by the exact
	// string it was stored under, and the password check that follows is unchanged.
	literal := strings.ToLower(strings.TrimSpace(email))
	normEmail, ok := normalizeEmail(email)
	if !ok {
		if literal == "" || len(literal) > maxEmailLen {
			equalizeTiming(password, s.pepper)
			return LoginResult{}, ErrInvalidCredentials
		}
		normEmail = literal
	}
	rec, err := s.repo.CredentialsByEmail(ctx, normEmail)
	if err != nil {
		// Fall back to the address EXACTLY as typed.
		//
		// Sign-up now folds Gmail aliases (dots, +tags, googlemail.com) so one inbox
		// cannot hold several accounts. Anyone who registered a dotted address BEFORE
		// that rule existed is stored dotted, and canonicalising their login would
		// stop matching their own row — locking them out of an account holding real
		// coins. Trying the literal form second costs one query on a failed login and
		// nothing on a successful one.
		if literal != normEmail {
			rec, err = s.repo.CredentialsByEmail(ctx, literal)
		}
	}
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
	return LoginResult{DashboardToken: dash, AgentID: rec.AgentPublicID, AgentName: rec.AgentName, UserPublicID: rec.UserPublicID}, nil
}

// IssueKey mints an API key for one machine, named by label, for an agent the
// caller owns. The raw key is returned once.
//
// It revokes exactly ONE thing: that agent's existing live key with the SAME label.
// So re-running `pyyol login` on a laptop replaces the laptop's key, and issuing a
// key for "ci-runner" leaves the laptop — and every container — connected.
//
// This replaced RotateKey, which revoked every live key for the agent. That made one
// key per agent a hard invariant while three separate paths minted unconditionally
// (`pyyol login`, /cli-login, the dashboard button), so the documented deployment
// flow signed the developer's own laptop out as a side effect and nothing said so.
// See migration 0071 for the full account.
func (s *Service) IssueKey(ctx context.Context, ownerPublicID, agentPublicID, label string) (string, error) {
	if _, err := s.repo.AgentByOwner(ctx, agentPublicID, ownerPublicID); err != nil {
		return "", ErrForbiddenOwner
	}
	label, err := NormalizeKeyLabel(label)
	if err != nil {
		return "", err
	}
	key, err := generateKey(s.pepper)
	if err != nil {
		return "", err
	}
	if err := s.repo.IssueKey(ctx, agentPublicID, ownerPublicID, key.Prefix, key.Hash, label, MaxLiveKeysPerAgent); err != nil {
		return "", err
	}
	return key.Raw, nil
}

// RevokeKey revokes one of the caller's keys by its public prefix.
func (s *Service) RevokeKey(ctx context.Context, ownerPublicID, prefix string) error {
	return s.repo.RevokeKey(ctx, ownerPublicID, prefix)
}

// ListKeys returns the owner's agent keys for auditing (prefix + timestamps).
func (s *Service) ListKeys(ctx context.Context, ownerPublicID string) ([]KeyInfo, error) {
	return s.repo.ListKeys(ctx, ownerPublicID)
}

// UpdateConfig writes an agent's spending limits. Only the owner (user scope)
// reaches this; the route guard enforces that, this enforces ownership + validity.
func (s *Service) UpdateConfig(ctx context.Context, ownerPublicID, agentPublicID string, l Limits) error {
	if err := l.Validate(); err != nil {
		return err
	}
	return s.repo.UpdateLimits(ctx, agentPublicID, ownerPublicID, l)
}

// PrimaryAgentOf returns the owner's first agent, or "" when they have none. Used so an
// authenticated caller can omit agent_id and still address their own agent.
func (s *Service) PrimaryAgentOf(ctx context.Context, ownerPublicID string) (string, error) {
	return s.repo.PrimaryAgentOf(ctx, ownerPublicID)
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
		// No live key for this prefix. If a REVOKED one matches the presented secret,
		// the caller is the rightful holder of a key that was turned off — say so, so
		// the SDK can print something actionable instead of a bare rejection. Anyone
		// without the secret still gets the same opaque error as before.
		if rev, revErr := s.repo.RevokedKeyByPrefix(ctx, prefix); revErr == nil &&
			verifySecret(rev.Hash, secret, s.pepper) {
			return nil, ErrRevokedAPIKey
		}
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

// UpdateProfile writes the owner's agent display identity (nil fields left
// unchanged) and returns the saved profile. Identity comes from the token
// (ownerPublicID), never the body.
func (s *Service) UpdateProfile(ctx context.Context, ownerPublicID string, displayName, bio, avatarURL *string) (AgentProfile, error) {
	if displayName != nil {
		trimmed := strings.TrimSpace(*displayName)
		displayName = &trimmed
	}
	return s.repo.UpdateAgentProfile(ctx, ownerPublicID, displayName, bio, avatarURL)
}

// AgentProfile returns the owner's agent display identity.
func (s *Service) AgentProfile(ctx context.Context, ownerPublicID string) (AgentProfile, error) {
	return s.repo.AgentProfileByOwner(ctx, ownerPublicID)
}

// SetGameConfig persists per-game behaviour for the owner's agent. behavior must
// be a JSON object (empty defaults to {}); the game must be a supported title.
func (s *Service) SetGameConfig(ctx context.Context, ownerPublicID, game string, behavior json.RawMessage) error {
	game = strings.ToLower(strings.TrimSpace(game))
	if !validGames[game] {
		return errInvalid("game must be one of: mafia, goofspiel")
	}
	if len(behavior) == 0 {
		behavior = json.RawMessage("{}")
	}
	if !json.Valid(behavior) {
		return errInvalid("behavior must be valid JSON")
	}
	return s.repo.SetGameConfig(ctx, ownerPublicID, game, []byte(behavior))
}

// Notification is a server-derived dashboard prompt (profile-completion, etc.).
type Notification struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	Href   string `json:"href"`
	CTA    string `json:"cta"`
}

// Notifications derives the owner's outstanding setup prompts from persisted
// profile state (no separate notification store). Returns an empty (non-nil)
// slice when there is nothing to prompt, or the owner has no agent yet.
func (s *Service) Notifications(ctx context.Context, ownerPublicID string) []Notification {
	out := []Notification{}
	prof, err := s.repo.AgentProfileByOwner(ctx, ownerPublicID)
	if err != nil {
		return out
	}
	const detail = "Complete this step to finish setting up your agent."
	if strings.TrimSpace(prof.DisplayName) == "" {
		out = append(out, Notification{ID: "profile-name", Kind: "profile",
			Title: "Name your agent", Detail: detail, Href: "/profile", CTA: "Add name"})
	}
	if strings.TrimSpace(prof.AvatarURL) == "" {
		out = append(out, Notification{ID: "profile-avatar", Kind: "profile",
			Title: "Upload an agent photo", Detail: detail, Href: "/profile", CTA: "Upload"})
	}
	return out
}

// RequestMagicLink issues a single-use passwordless sign-in token for the
// account with this email. To avoid leaking which emails are registered, an
// unknown email is not an error and yields no token (sent is still true). The
// raw token is returned ONLY for a known account; the handler surfaces it only
// in non-prod envs — PROD must deliver it by email (not yet wired).
func (s *Service) RequestMagicLink(ctx context.Context, email string) (token string, sent bool, err error) {
	normEmail, ok := normalizeEmail(email)
	if !ok {
		return "", false, errInvalid("a valid email is required")
	}
	raw, err := randToken(24)
	if err != nil {
		return "", false, err
	}
	found, err := s.repo.CreateMagicLink(ctx, hashToken(raw), normEmail, s.clock.Now().Add(magicLinkTTL))
	if err != nil {
		return "", false, err
	}
	if !found {
		return "", true, nil // behave as if a link was sent
	}
	return raw, true, nil
}

// VerifyMagicLink consumes a single-use token and mints a fresh dashboard
// session, reusing the same JWT path as login/signup.
func (s *Service) VerifyMagicLink(ctx context.Context, token string) (LoginResult, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return LoginResult{}, ErrInvalidMagicLink
	}
	ml, err := s.repo.ConsumeMagicLink(ctx, hashToken(token))
	if err != nil {
		return LoginResult{}, ErrInvalidMagicLink
	}
	dash, err := s.jwt.Issue(ml.UserPublicID)
	if err != nil {
		return LoginResult{}, err
	}
	return LoginResult{DashboardToken: dash, AgentID: ml.AgentPublicID, UserPublicID: ml.UserPublicID}, nil
}

// hashToken returns the hex SHA-256 of a raw token; only the hash is persisted,
// so a DB read never reveals a usable sign-in token.
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
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
