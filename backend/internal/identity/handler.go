package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/middleware"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the identity HTTP API and mounts it with the correct auth
// scopes. registerRL rate-limits account creation (register + signup, 5/hour/IP)
// and loginRL rate-limits password logins (brute-force defense); both are
// injected so the handler stays decoupled from the limiter backend.
type Handler struct {
	stakes       StakeSource // cheapest ranked stake, for the max_bid warning
	svc          *Service
	authn        *auth.Authenticator
	privy        *auth.PrivyVerifier  // nil ⇒ Privy login disabled (503)
	google       *auth.GoogleVerifier // nil/unconfigured ⇒ Google login disabled (503)
	github       *auth.GitHubVerifier // nil/unconfigured ⇒ GitHub login disabled (503)
	registerRL   func(http.Handler) http.Handler
	loginRL      func(http.Handler) http.Handler
	keysRL       func(http.Handler) http.Handler
	dev          bool // non-prod: surface the magic-link token in the response (no email wired)
	xClaim       bool // X-claim (tweet) onboarding available (needs a real verifier)
	emailEnabled bool // an email sender is wired so magic-link delivery actually works
	// twoFAStatus reports whether the user has 2FA enabled, surfaced in /v1/me so a
	// profile/security page can render the preference. Injected to avoid coupling
	// identity to the twofa package; nil ⇒ the field is simply omitted.
	twoFAStatus func(ctx context.Context, userPublicID string) (bool, error)
	refresh     *auth.RefreshService // nil ⇒ refresh tokens disabled (access-token only)
	// accountRL throttles password attempts per ACCOUNT, alongside the per-IP loginRL.
	//
	// The two catch different attacks and neither substitutes for the other. Per-IP stops
	// one host working through a password list; it is blind to the same list being spread
	// one-guess-per-host across a botnet, which never trips any single IP bucket. Keying
	// on the identity under attack bounds the total attempts a given account can absorb
	// no matter how many sources they come from.
	//
	// Nil ⇒ per-IP only (unchanged behaviour).
	accountRL func(ctx context.Context, identifier string) (ok bool, retryAfter time.Duration)
	// admins is the ADMIN_USER_IDS allowlist, for the one admin-scoped route this handler
	// serves (POST /v1/admin/agents). Nil/empty is safe: RequirePlatformOrAdmin still
	// admits a valid Platform token, and admits nobody else.
	admins map[string]bool
	// kicker closes live agent sockets on ban. Nil ⇒ persist + revoke still run.
	kicker AgentKicker
}

// AgentKicker closes one live agent socket. Satisfied by *agentgw.Gateway.
type AgentKicker interface {
	Kick(agentID, reason string)
}

// SetAdmins installs the ADMIN_USER_IDS allowlist used by the admin create-agent route.
// Additive to the Platform token, exactly as every other admin surface treats it.
func (h *Handler) SetAdmins(userIDs []string) {
	m := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		if id = strings.TrimSpace(id); id != "" {
			m[id] = true
		}
	}
	h.admins = m
}

// SetAccountRateLimit installs the per-account credential throttle used by password
// login and the magic-link request. Nil leaves login with per-IP limiting only.
func (h *Handler) SetAccountRateLimit(fn func(ctx context.Context, identifier string) (bool, time.Duration)) {
	h.accountRL = fn
}

// allowAccount reports whether this identifier has credential-attempt budget left.
//
// Normalises the identifier so casing and stray whitespace cannot buy a second budget
// for the same account, and treats an empty one as allowed — a blank email is rejected
// by the service anyway, and spending budget on it would let anyone exhaust a shared
// bucket by posting nothing.
func (h *Handler) allowAccount(ctx context.Context, identifier string) bool {
	if h.accountRL == nil {
		return true
	}
	id := strings.ToLower(strings.TrimSpace(identifier))
	if id == "" {
		return true
	}
	ok, _ := h.accountRL(ctx, id)
	return ok
}

// SetTwoFAStatus wires the 2FA-enabled lookup surfaced in the profile (GET /v1/me).
func (h *Handler) SetTwoFAStatus(fn func(ctx context.Context, userPublicID string) (bool, error)) {
	h.twoFAStatus = fn
}

func NewHandler(svc *Service, authn *auth.Authenticator, privy *auth.PrivyVerifier, registerRL, loginRL func(http.Handler) http.Handler, dev, xClaim, emailEnabled bool) *Handler {
	noop := func(n http.Handler) http.Handler { return n }
	if registerRL == nil {
		registerRL = noop // no-op fallback
	}
	if loginRL == nil {
		loginRL = noop
	}
	return &Handler{svc: svc, authn: authn, privy: privy, registerRL: registerRL, loginRL: loginRL, keysRL: noop, dev: dev, xClaim: xClaim, emailEnabled: emailEnabled}
}

// SetKeysRateLimit installs a per-user limiter on API-key creation (credential
// minting). Nil keeps the no-op passthrough.
func (h *Handler) SetKeysRateLimit(mw func(http.Handler) http.Handler) {
	if mw != nil {
		h.keysRL = mw
	}
}

// Register is an httpx.Mount: it attaches all identity routes with their guards.
func (h *Handler) Register(r chi.Router) {
	// Public account creation (rate-limited 5/hour/IP): X-claim onboarding and
	// the normal email + password sign-up share the same create-account limit.
	r.Group(func(r chi.Router) {
		r.Use(h.registerRL)
		r.Post("/v1/register", h.register)
		r.Get("/v1/register/verify", h.verify)
		r.Post("/v1/auth/signup", h.signup)
	})

	// Password login + passwordless magic-link request (rate-limited to blunt
	// credential-stuffing and account enumeration).
	r.Group(func(r chi.Router) {
		r.Use(h.loginRL)
		r.Post("/v1/auth/login", h.login)
		r.Post("/v1/auth/refresh", h.refreshSession)
		r.Post("/v1/auth/logout", h.logout)
		r.Post("/v1/auth/magic-link", h.requestMagicLink)
		// Privy token exchange: verify Privy's access token, find-or-create the
		// owner, return a dashboard session. Rate-limited alongside login.
		r.Post("/v1/auth/privy", h.privyLogin)
		r.Post("/v1/auth/google", h.googleLogin)
		r.Post("/v1/auth/github", h.githubLogin)
	})
	// Magic-link verify consumes a single-use token (public; the token is the
	// credential), so it is not IP-rate-limited.
	r.Get("/v1/auth/magic-link/verify", h.verifyMagicLink)

	// Authenticated routes: attach the principal, then guard by scope.
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		// ADMIN account creation. The same account creation the public sign-up performs,
		// with one extra field the public path has no business accepting: the agent's KIND.
		//
		// It is a separate route rather than a privileged field on /v1/auth/signup because
		// signup is deliberately public and unauthenticated (see the pinned public route
		// surface). Teaching it to read a principal that is normally absent, in order to
		// decide whether to honour one field, is how an "only when authenticated" check
		// becomes an "authenticated check that was skipped".
		//
		// The guard is auth.RequirePlatformOrAdmin — the same one every other admin surface
		// uses. There is no second credential path, no shared secret, and no env-var escape
		// hatch: a caller either presents the Super Admin's Platform token or is in
		// ADMIN_USER_IDS.
		r.With(auth.RequirePlatformOrAdmin(h.admins)).Post("/v1/admin/agents", h.adminCreateAgent)
		r.With(auth.RequirePlatformOrAdmin(h.admins)).Post("/v1/admin/users/{id}/ban", h.banUser)
		r.With(auth.RequirePlatformOrAdmin(h.admins)).Post("/v1/admin/users/{id}/unban", h.unbanUser)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/config", h.updateConfig)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/me", h.me)
		// The CLI handoff. Owner-scoped: it re-expresses authority the caller already proved.
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/auth/cli-token", h.cliToken)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/agent/keys", h.listKeys)
		r.With(auth.RequireScope(auth.ScopeUser), h.keysRL).Post("/v1/agent/keys", h.createKey)
		r.With(auth.RequireScope(auth.ScopeUser)).Delete("/v1/agent/keys/{prefix}", h.revokeKey)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/signing-key", h.setSigningKey)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/profile", h.updateProfile)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/game-config", h.setGameConfig)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/notifications", h.notifications)
		// /v1/agent/stats is served by the profiles module (Stage 7, real data).
	})
}

// xClaimGuard fails closed when X-claim onboarding isn't available (prod without
// a real X verifier). It steers callers to the email/password path instead of
// silently auto-verifying a fake identity via the dev verifier.
func (h *Handler) xClaimGuard(w http.ResponseWriter) bool {
	if h.xClaim {
		return true
	}
	httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "x_onboarding_unavailable",
		"Onboarding via X is not available here. Create your account with email and password (POST /v1/auth/signup)."))
	return false
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	if !h.xClaimGuard(w) {
		return
	}
	var in struct {
		AgentName   string `json:"agent_name"`
		Description string `json:"description"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	claim, err := h.svc.Register(r.Context(), in.AgentName, in.Description)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"claim_token": claim.Token,
		"expires_at":  claim.ExpiresAt,
		"instructions": "Post a public tweet containing this claim token, then poll " +
			"GET /v1/register/verify?claim_token=" + claim.Token,
	})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	if !h.xClaimGuard(w) {
		return
	}
	token := r.URL.Query().Get("claim_token")
	if token == "" {
		httpx.Error(w, errInvalid("claim_token query parameter is required"))
		return
	}
	captchaToken := r.URL.Query().Get("captcha")
	if captchaToken == "" {
		captchaToken = r.Header.Get("X-Captcha-Token")
	}

	res, err := h.svc.VerifyClaim(r.Context(), token, captchaToken, clientIP(r))
	if err != nil {
		httpx.Error(w, err) // includes the 202 "claim_pending" polling state
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"api_key":         res.APIKey,
		"agent_id":        res.AgentID,
		"dashboard_token": res.DashboardToken,
	})
}

// signup creates a normal email + password account and its first agent, then
// returns a ready-to-use dashboard session. No unused 'initial' key is minted.
func (h *Handler) signup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		AgentName   string `json:"agent_name"`
		Description string `json:"description"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	res, err := h.svc.SignUp(r.Context(), in.Email, in.Password, in.AgentName, in.Description)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	out := map[string]any{
		"dashboard_token": res.DashboardToken,
		"refresh_token":   h.issueRefresh(r.Context(), res.UserPublicID),
		"agent_id":        res.AgentID,
		"agent_name":      res.AgentName,
	}
	if res.APIKey != "" {
		out["api_key"] = res.APIKey
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// adminCreateAgent creates an account whose agent carries an explicit kind.
//
// This exists for ONE reason: the platform's own benchmark agents must be `harness` from
// the moment they are created. gamelab used to sign them up on the public path, which
// makes them `external` — so a benchmark run landed on the public DEVELOPER leaderboard,
// rated, as though the platform were a competitor, and /harness stayed empty because no
// harness-kind seats existed for it to fit.
//
// Everything AFTER creation is deliberately identical to a developer's flow. The agent
// submits a manifest, has its endpoint verified by the platform, is issued an agent-scope
// key, funds a wallet, and plays through the same lobby and queue with every decision
// completion-bound. That sameness is the point — a benchmark run on a private code path
// would measure the private code path. The only thing this route changes is who the agent
// is declared to BE, which is the one judgement a developer cannot be allowed to make
// about themselves.
//
// Response shape mirrors signup exactly, so the caller's onboarding code is shared.
func (h *Handler) adminCreateAgent(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		AgentName   string `json:"agent_name"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	// Required, not defaulted. An admin route whose kind silently defaults to `external`
	// would answer 201 for a typo'd kind and hand back exactly the agent this route exists
	// to stop being created.
	if strings.TrimSpace(in.Kind) == "" {
		httpx.Error(w, errInvalid("kind is required"))
		return
	}
	kind := strings.TrimSpace(in.Kind)

	// PLATFORM agents create no user account.
	//
	// This route used to run every kind through SignUpAs, which mints a user with an email
	// and a password. For the platform's own benchmark seats that produced a throwaway
	// account each — `lab+78611-0@pyyol.test` and its siblings sitting in the users table
	// beside real developers, and their matches attributed to the DEVELOPER board.
	//
	// A benchmark seat has no person behind it. It hangs off `usr_system`, the identity the
	// house bots have used since migration 0017, and email/password on the request are
	// ignored rather than rejected: an admin script that still sends them keeps working, and
	// nothing it sends can bring an account into existence.
	var res SignUpResult
	var err error
	if kind == KindExternal {
		res, err = h.svc.SignUpAs(r.Context(), in.Email, in.Password, in.AgentName, in.Description, kind)
	} else {
		res, err = h.svc.CreatePlatformAgent(r.Context(), in.AgentName, in.Description, kind)
	}
	if err != nil {
		httpx.Error(w, err)
		return
	}
	out := map[string]any{
		"api_key":    res.APIKey, // shown exactly once
		"agent_id":   res.AgentID,
		"agent_name": res.AgentName,
		"kind":       kind,
	}
	// Session tokens only where there is a session to hold them. A platform agent has no
	// owner who can log in, and handing back a dashboard token for one would be issuing a
	// login to an account nobody has.
	if res.DashboardToken != "" {
		out["dashboard_token"] = res.DashboardToken
		out["refresh_token"] = h.issueRefresh(r.Context(), res.UserPublicID)
	}
	httpx.JSON(w, http.StatusCreated, out)
}

// login authenticates an email + password and returns a fresh dashboard session.
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	// Checked BEFORE the password is verified. LogIn does a bcrypt comparison, which is
	// deliberately expensive, so letting unlimited attempts reach it turns the login
	// endpoint into a CPU-exhaustion lever as well as a guessing oracle.
	if !h.allowAccount(r.Context(), in.Email) {
		httpx.Error(w, httpx.ErrRateLimited)
		return
	}
	res, err := h.svc.LogIn(r.Context(), in.Email, in.Password)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"dashboard_token": res.DashboardToken,
		"refresh_token":   h.issueRefresh(r.Context(), res.UserPublicID),
		"agent_id":        res.AgentID,
		"agent_name":      res.AgentName,
	})
}

// SetGoogle wires the Google ID-token verifier (enables POST /v1/auth/google).
func (h *Handler) SetGoogle(v *auth.GoogleVerifier) { h.google = v }

// googleLogin verifies a Google Identity Services ID token (the `credential` from a
// "Sign in with Google" button), find-or-creates the account, and returns a dashboard
// session. `created` is true for a brand-new account so the client sends it to
// onboarding; an existing user goes straight to the dashboard.
func (h *Handler) googleLogin(w http.ResponseWriter, r *http.Request) {
	if h.google == nil || !h.google.Enabled() {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "google_unavailable",
			"Google login is not configured here. Use email/password (POST /v1/auth/login)."))
		return
	}
	var in struct {
		Credential string `json:"credential"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Credential == "" {
		httpx.Error(w, errInvalid("credential is required"))
		return
	}
	claims, err := h.google.Verify(r.Context(), in.Credential)
	if err != nil {
		httpx.Error(w, httpx.NewError(http.StatusUnauthorized, "invalid_google_token", "Google sign-in verification failed."))
		return
	}
	// An UNVERIFIED Google email must never be trusted for identity.
	//
	// UpsertGoogleAccount links google_sub onto whatever local account matches the
	// email and returns a session for it. Passing an unverified claim through
	// therefore meant anyone who could mint a Google identity asserting someone
	// else's address could take over that account. `email_verified` was already
	// parsed off the ID token and simply never read; we now require it, and drop the
	// address (rather than the login) when it is unverified, so the user still gets
	// an account keyed on the immutable `sub` — just no automatic email linking.
	email := claims.Email
	if !claims.EmailVerified {
		email = ""
	}
	res, err := h.svc.SignUpOrLoginGoogle(r.Context(), claims.Sub, email, claims.Name)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	out := map[string]any{
		"dashboard_token": res.DashboardToken,
		"refresh_token":   h.issueRefresh(r.Context(), res.UserPublicID),
		"agent_id":        res.AgentID,
		"agent_name":      res.AgentName,
		"created":         res.Created,
	}
	if res.APIKey != "" {
		out["api_key"] = res.APIKey // new account's first key, shown once
	}
	httpx.JSON(w, http.StatusOK, out)
}

// SetGitHub wires the GitHub OAuth verifier (enables POST /v1/auth/github).
func (h *Handler) SetGitHub(v *auth.GitHubVerifier) { h.github = v }

// githubLogin completes the GitHub OAuth code exchange (the `code` the browser came
// back with), find-or-creates the account, and returns a dashboard session — the same
// response shape as googleLogin, so the frontend session handling is identical.
func (h *Handler) githubLogin(w http.ResponseWriter, r *http.Request) {
	if h.github == nil || !h.github.Enabled() {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "github_unavailable",
			"GitHub login is not configured here. Use email/password (POST /v1/auth/login)."))
		return
	}
	var in struct {
		Code        string `json:"code"`
		RedirectURI string `json:"redirect_uri"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Code == "" {
		httpx.Error(w, errInvalid("code is required"))
		return
	}
	claims, err := h.github.Exchange(r.Context(), in.Code, in.RedirectURI)
	if err != nil {
		httpx.Error(w, httpx.NewError(http.StatusUnauthorized, "invalid_github_code", "GitHub sign-in verification failed."))
		return
	}
	// Same trust boundary as Google: only a VERIFIED email may link this GitHub login
	// onto an existing local account. The stable numeric id keys the account either way.
	email := claims.Email
	if !claims.EmailVerified {
		email = ""
	}
	res, err := h.svc.SignUpOrLoginGitHub(r.Context(), claims.ID, claims.Login, email, claims.Name)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	out := map[string]any{
		"dashboard_token": res.DashboardToken,
		"refresh_token":   h.issueRefresh(r.Context(), res.UserPublicID),
		"agent_id":        res.AgentID,
		"agent_name":      res.AgentName,
		"created":         res.Created,
	}
	if res.APIKey != "" {
		out["api_key"] = res.APIKey // new account's first key, shown once
	}
	httpx.JSON(w, http.StatusOK, out)
}

// privyLogin exchanges a Privy access token for a dashboard session. The token is
// verified cryptographically (auth.PrivyVerifier); the optional profile block is
// non-authoritative display data captured at login. On first login the owner and
// their treasury wallet are created automatically.
func (h *Handler) privyLogin(w http.ResponseWriter, r *http.Request) {
	if !h.privy.Enabled() {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "privy_unavailable",
			"Privy login is not configured here. Use email/password (POST /v1/auth/login)."))
		return
	}
	var in struct {
		Token   string `json:"token"`
		Profile struct {
			Email          string `json:"email"`
			WalletAddress  string `json:"wallet_address"`
			WalletProvider string `json:"wallet_provider"`
			DisplayName    string `json:"display_name"`
			AvatarURL      string `json:"avatar_url"`
		} `json:"profile"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.Token == "" {
		httpx.Error(w, errInvalid("token is required"))
		return
	}
	id, err := h.privy.Verify(in.Token)
	if err != nil {
		httpx.Error(w, httpx.NewError(http.StatusUnauthorized, "invalid_privy_token", "Privy token verification failed."))
		return
	}
	res, err := h.svc.UpsertFromPrivy(r.Context(), id.UserID, PrivyProfile{
		Email:          in.Profile.Email,
		WalletAddress:  in.Profile.WalletAddress,
		WalletProvider: in.Profile.WalletProvider,
		DisplayName:    in.Profile.DisplayName,
		AvatarURL:      in.Profile.AvatarURL,
	})
	if err != nil {
		httpx.Error(w, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	httpx.JSON(w, status, map[string]any{
		"dashboard_token": res.DashboardToken,
		"refresh_token":   h.issueRefresh(r.Context(), res.UserPublicID),
		"user_id":         res.UserPublicID,
		"created":         res.Created,
	})
}

func (h *Handler) updateConfig(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AgentID              string `json:"agent_id"`
		CoinLimitPerMatch    int64  `json:"coin_limit_per_match"`
		DailyLossLimit       int64  `json:"daily_loss_limit"`
		SessionLossLimit     int64  `json:"session_loss_limit"`
		MinWalletBalance     int64  `json:"min_wallet_balance"`
		MaxBid               int64  `json:"max_bid"`
		MaxConcurrentMatches int    `json:"max_concurrent_matches"`
		CooldownLosses       int    `json:"cooldown_losses"`
		CooldownSeconds      int    `json:"cooldown_seconds"`
		AutoJoin             bool   `json:"auto_join"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	// An omitted agent_id means "my agent", not an error.
	//
	// This 400'd, and the browser's only source for the id was a cookie written at login —
	// so a developer on a second device, or after clearing cookies, or on a session restored
	// from a refresh token, pressed Save on their guardrails and got "agent_id is required"
	// surfaced as "Failed to save config". The server knows which agents the account owns.
	if in.AgentID == "" {
		primary, err := h.svc.PrimaryAgentOf(r.Context(), p.UserPublicID)
		if err != nil {
			httpx.Error(w, err)
			return
		}
		if primary == "" {
			httpx.Error(w, errInvalid("this account has no agent yet"))
			return
		}
		in.AgentID = primary
	}
	limits := Limits{
		CoinLimitPerMatch: in.CoinLimitPerMatch, DailyLossLimit: in.DailyLossLimit,
		SessionLossLimit: in.SessionLossLimit, MinWalletBalance: in.MinWalletBalance,
		MaxBid: in.MaxBid, MaxConcurrentMatches: in.MaxConcurrentMatches,
		CooldownLosses: in.CooldownLosses, CooldownSeconds: in.CooldownSeconds, AutoJoin: in.AutoJoin,
	}
	if err := h.svc.UpdateConfig(r.Context(), p.UserPublicID, in.AgentID, limits); err != nil {
		httpx.Error(w, err)
		return
	}
	// Tell the developer NOW if these limits make ranked play impossible.
	//
	// max_bid below the cheapest tier has exactly one effect: the agent can never join a ranked
	// table. It does not restrict practice, because a zero-fee table skips the limit checks
	// entirely. So there is no configuration this silently enables — only one it silently
	// disables, and until now the first sign was a 409 at join time, long after the setting was
	// saved and forgotten. That is the same shape as the stake floor: the check existed, just not
	// where the person who needed it would meet it.
	//
	// A WARNING and not a rejection, for the reason migration 0070 gives for not backfilling
	// stored limits: this is the owner's money and their risk cap to choose. Refusing the write
	// would override a deliberate decision; staying silent would hide an accidental one. Saying
	// so does neither.
	out := map[string]any{"status": "updated"}
	if h.stakes != nil {
		if lowest, ok := h.stakes.LowestEnabledCoins(r.Context(), "goofspiel"); ok && lowest > 0 && limits.MaxBid < lowest {
			out["warning"] = fmt.Sprintf(
				"max_bid is %d, below the cheapest ranked stake of %d coins. This agent can still "+
					"play practice tables, but it will be rejected from every ranked match until "+
					"max_bid (and coin_limit_per_match) are at least %d.",
				limits.MaxBid, lowest, lowest)
			out["ranked_playable"] = false
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

// StakeSource exposes the cheapest ranked stake, so the limits endpoint can tell a developer
// when their own cap has locked them out of ranked play. Satisfied by *gamestakes.Service.
type StakeSource interface {
	LowestEnabledCoins(ctx context.Context, game string) (int64, bool)
}

// SetStakeSource wires the tier table. Optional; without it the warning is simply omitted.
func (h *Handler) SetStakeSource(src StakeSource) { h.stakes = src }

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	// A dashboard JWT is user-scope: AgentPublicID is empty. The cookie the
	// browser wrote at login is not proof of ownership — it is display. Resolve
	// the account's primary agent from the user id the token actually carries
	// so deploy/verify always address THIS owner's agent.
	agentID := p.AgentPublicID
	if agentID == "" && p.UserPublicID != "" {
		if id, err := h.svc.PrimaryAgentOf(r.Context(), p.UserPublicID); err == nil {
			agentID = id
		}
	}
	out := map[string]any{
		"user_id":  p.UserPublicID,
		"agent_id": agentID,
	}
	// Security preference: whether the user has opted into 2FA (never defaulted on).
	if h.twoFAStatus != nil {
		if enabled, err := h.twoFAStatus(r.Context(), p.UserPublicID); err == nil {
			out["two_factor_enabled"] = enabled
		}
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	keys, err := h.svc.ListKeys(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	if keys == nil {
		keys = []KeyInfo{}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"keys": keys})
}

// createKey issues an agent key for ONE machine, named by `label`. Re-issuing with
// the same label replaces that machine's key; every other machine keeps working.
//
// `label` is optional on the wire on purpose. Older clients (a pinned `pyyol`, a
// cached dashboard bundle) send only agent_id, and this endpoint used to revoke every
// live key for the agent — so rejecting them would break upgrades while accepting
// them silently would put every old client back in one shared slot. Instead an absent
// label falls back to a per-client-KIND name derived from the User-Agent, which is
// stable across runs: an old CLI keeps replacing "pyyol cli (unnamed)" and can no
// longer evict a container's key.
func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AgentID string `json:"agent_id"`
		Label   string `json:"label"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.AgentID == "" {
		httpx.Error(w, errInvalid("agent_id is required"))
		return
	}
	label := in.Label
	if strings.TrimSpace(label) == "" {
		label = fallbackKeyLabel(r.UserAgent())
	}
	raw, err := h.svc.IssueKey(r.Context(), p.UserPublicID, in.AgentID, label)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"api_key": raw, "label": label})
}

// fallbackKeyLabel names a key whose issuer did not label it, from the client kind.
// Deliberately NOT unique per request: the point is that one client kind occupies one
// slot, so an unlabelled issuer replaces its own key instead of accumulating keys or
// evicting somebody else's.
func fallbackKeyLabel(userAgent string) string {
	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "pyyol"):
		return "pyyol cli (unnamed)"
	case strings.Contains(ua, "mozilla"), strings.Contains(ua, "safari"), strings.Contains(ua, "chrome"):
		return "dashboard (unnamed)"
	default:
		return "unnamed client"
	}
}

func (h *Handler) setSigningKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AgentID string `json:"agent_id"`
		Pubkey  string `json:"pubkey"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.AgentID == "" {
		httpx.Error(w, errInvalid("agent_id is required"))
		return
	}
	if err := h.svc.SetSigningKey(r.Context(), p.UserPublicID, in.AgentID, in.Pubkey); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "registered"})
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	prefix := NormalizeKeyPrefix(chi.URLParam(r, "prefix"))
	if prefix == "" {
		httpx.Error(w, ErrNotFound)
		return
	}
	if err := h.svc.RevokeKey(r.Context(), p.UserPublicID, prefix); err != nil {
		httpx.Error(w, err)
		return
	}
	// 200 + JSON, not 204: some BFF hops treat an empty 204 as a failed parse
	// and the dashboard then keeps the row. The client keys off `revoked`.
	httpx.JSON(w, http.StatusOK, map[string]any{"revoked": true, "prefix": prefix})
}

// updateProfile persists the owner's agent display identity. Fields are optional
// pointers: an omitted field is left unchanged. Identity is taken from the token.
func (h *Handler) updateProfile(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		DisplayName *string `json:"display_name"`
		Bio         *string `json:"bio"`
		AvatarURL   *string `json:"avatar_url"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	prof, err := h.svc.UpdateProfile(r.Context(), p.UserPublicID, in.DisplayName, in.Bio, in.AvatarURL)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, profileJSON(prof))
}

// setGameConfig persists per-game behaviour for the owner's agent.
func (h *Handler) setGameConfig(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Game     string          `json:"game"`
		Behavior json.RawMessage `json:"behavior"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.SetGameConfig(r.Context(), p.UserPublicID, in.Game, in.Behavior); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// notifications returns the owner's outstanding setup prompts (server-derived).
func (h *Handler) notifications(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	httpx.JSON(w, http.StatusOK, map[string]any{
		"notifications": h.svc.Notifications(r.Context(), p.UserPublicID),
	})
}

// requestMagicLink issues a single-use passwordless sign-in token. In non-prod
// envs the token is returned in the response (dev_token) for testing, mirroring
// how other dev-only affordances are gated; PROD must deliver it by email
// (delivery is not yet wired — do not enable this path in prod expecting mail).
func (h *Handler) requestMagicLink(w http.ResponseWriter, r *http.Request) {
	// Fail closed when there is no way to deliver the link: in prod without an
	// email sender wired, the token is never surfaced (dev-only) and no email goes
	// out, so returning {"sent":true} would be a lie. Steer callers to password
	// login instead of silently dropping their sign-in request.
	if !h.dev && !h.emailEnabled {
		httpx.Error(w, httpx.NewError(http.StatusServiceUnavailable, "magic_link_unavailable",
			"Email sign-in links are not available here. Sign in with your email and password (POST /v1/auth/login)."))
		return
	}
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	// Same per-account budget as password login. Without it a magic-link endpoint is a
	// mailbox-flooding tool aimed at one address — and because each request mints a fresh
	// valid token, it also widens the window in which any one of them can be guessed.
	if !h.allowAccount(r.Context(), in.Email) {
		httpx.Error(w, httpx.ErrRateLimited)
		return
	}
	token, sent, err := h.svc.RequestMagicLink(r.Context(), in.Email)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	resp := map[string]any{"sent": sent}
	if h.dev && token != "" {
		resp["dev_token"] = token
	}
	httpx.JSON(w, http.StatusOK, resp)
}

// verifyMagicLink consumes a token and returns a fresh dashboard session.
func (h *Handler) verifyMagicLink(w http.ResponseWriter, r *http.Request) {
	res, err := h.svc.VerifyMagicLink(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"dashboard_token": res.DashboardToken,
		"refresh_token":   h.issueRefresh(r.Context(), res.UserPublicID),
		"agent_id":        res.AgentID,
	})
}

// profileJSON renders an AgentProfile in the shape the dashboard expects.
func profileJSON(p AgentProfile) map[string]any {
	return map[string]any{
		"agent_id":     p.AgentPublicID,
		"display_name": p.DisplayName,
		"bio":          p.Bio,
		"avatar_url":   p.AvatarURL,
	}
}

// stats is a Stage 1 stub; real numbers arrive in Stage 7 (ratings/profiles).

// clientIP extracts a best-effort client IP for captcha/rate-limit purposes.
func clientIP(r *http.Request) string {
	if id := middleware.ClientIP(r); id != "" {
		return id
	}
	return r.RemoteAddr
}

// SetRefresh wires the rotating refresh-token service (nil keeps access-token-only).
func (h *Handler) SetRefresh(rs *auth.RefreshService) { h.refresh = rs }

// SetKicker wires the agent-socket kick used when an account is banned.
func (h *Handler) SetKicker(k AgentKicker) { h.kicker = k }

// banUser persists the ban, revokes every refresh family, and kicks live sockets.
func (h *Handler) banUser(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	prev, err := h.svc.Ban(r.Context(), id)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	revoked := 0
	if h.refresh != nil {
		n, rerr := h.refresh.RevokeAllForUser(r.Context(), id)
		if rerr == nil {
			revoked = n
		}
	}
	kicked := 0
	if h.kicker != nil {
		for _, aid := range h.svc.AgentIDsOf(r.Context(), id) {
			h.kicker.Kick(aid, "account banned")
			kicked++
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":            "banned",
		"previous":          prev,
		"sessions_revoked":  revoked,
		"sockets_kicked":    kicked,
	})
}

// unbanUser restores access. Existing tokens stay revoked; the user signs in again.
func (h *Handler) unbanUser(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(chi.URLParam(r, "id"))
	prev, err := h.svc.Unban(r.Context(), id)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"status":   "active",
		"previous": prev,
	})
}

// issueRefresh mints a refresh token for a freshly-authenticated user. Best-effort:
// if refresh is disabled or minting fails, the session still works as a short-lived
// access token — we never fail the login over the refresh token.
func (h *Handler) issueRefresh(ctx context.Context, userPublicID string) string {
	if h.refresh == nil || userPublicID == "" {
		return ""
	}
	tok, err := h.refresh.Issue(ctx, userPublicID)
	if err != nil {
		return ""
	}
	return tok
}

// refreshSession rotates a refresh token → a fresh access JWT + a new refresh token.
// A 401 means the session is genuinely over (expired/revoked/reused) → sign in again.
func (h *Handler) refreshSession(w http.ResponseWriter, r *http.Request) {
	if h.refresh == nil {
		httpx.Error(w, httpx.NewError(http.StatusNotFound, "not_found", "refresh not available"))
		return
	}
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	access, next, uid, err := h.refresh.Rotate(r.Context(), in.RefreshToken)
	if err != nil {
		httpx.Error(w, httpx.NewError(http.StatusUnauthorized, "refresh_invalid", "Your session expired. Please sign in again."))
		return
	}
	if err := h.svc.rejectIfBanned(r.Context(), uid); err != nil {
		_, _ = h.refresh.RevokeAllForUser(r.Context(), uid)
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"dashboard_token": access,
		"refresh_token":   next,
	})
}

// cliToken mints a SEPARATE, independently-revocable session for a terminal.
//
// # The bug this fixes
//
// The web CLI-login page handed the CLI whatever `session.dashboardToken` held — and in the
// browser that is the literal sentinel "cookie:user", never a JWT. The real token lives in an
// HttpOnly cookie that JS cannot read (deliberately: a JS-readable credential is an XSS-exfil
// path), and inside the browser the BFF swaps the sentinel for the real cookie on every call.
// The CLI is not a browser. It stored "cookie:user" and sent it as a Bearer, so EVERY
// owner-scoped command answered 401 — publish first, and therefore certification, and
// therefore every match including sandbox. A freshly logged-in CLI user could not play at all.
//
// # Why a new session rather than the browser's
//
// Forwarding the browser's access token would expire in about an hour. Forwarding its refresh
// token is worse: refresh ROTATES, so browser and CLI would share one token and whichever
// spent it first would silently log the other out.
//
// RefreshService.Issue starts a NEW FAMILY, and revocation is per-family. So the terminal gets
// a session that lives alongside the browser's, can be revoked on its own, and refreshes on
// its own — which is exactly what `pyyol logout` on one machine should mean.
//
// Owner-scoped: the caller must already hold a valid user JWT, which through the BFF means a
// live session cookie. This endpoint never widens authority — it re-expresses authority the
// caller already proved, in a form a terminal can hold.
func (h *Handler) cliToken(w http.ResponseWriter, r *http.Request) {
	// PrincipalFromContext returns a POINTER that is nil when nothing authenticated the
	// request. Dereferencing it straight away — which the first version of this did — turns
	// the unauthenticated case into a panic instead of a 401, in the one handler whose whole
	// job is to be careful about authority. The route is behind RequireScope(ScopeUser) so it
	// should be unreachable; a guard that is only correct while another guard holds is not a
	// guard.
	p := auth.PrincipalFromContext(r.Context())
	owner := ""
	if p != nil {
		owner = p.UserPublicID
	}
	if owner == "" {
		httpx.Error(w, httpx.NewError(http.StatusUnauthorized, "unauthenticated", "Authentication is required."))
		return
	}
	access, err := h.svc.IssueDashboardToken(owner)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// A refresh token is what keeps the CLI signed in past the access token's short TTL.
	// Best-effort: without it the CLI still works until the access token expires, which is a
	// far better outcome than refusing to hand over any credential at all.
	httpx.JSON(w, http.StatusOK, map[string]any{
		"dashboard_token": access,
		"refresh_token":   h.issueRefresh(r.Context(), owner),
	})
}

// logout revokes the whole refresh-token family (best-effort; always 200).
func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	_ = httpx.DecodeJSON(w, r, &in)
	if h.refresh != nil && in.RefreshToken != "" {
		_ = h.refresh.Revoke(r.Context(), in.RefreshToken)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"ok": true})
}
