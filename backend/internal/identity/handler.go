package identity

import (
	"context"
	"encoding/json"
	"net/http"

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
	svc        *Service
	authn      *auth.Authenticator
	privy      *auth.PrivyVerifier // nil ⇒ Privy login disabled (503)
	registerRL func(http.Handler) http.Handler
	loginRL    func(http.Handler) http.Handler
	keysRL     func(http.Handler) http.Handler
	dev        bool // non-prod: surface the magic-link token in the response (no email wired)
	xClaim     bool // X-claim (tweet) onboarding available (needs a real verifier)
	// twoFAStatus reports whether the user has 2FA enabled, surfaced in /v1/me so a
	// profile/security page can render the preference. Injected to avoid coupling
	// identity to the twofa package; nil ⇒ the field is simply omitted.
	twoFAStatus func(ctx context.Context, userPublicID string) (bool, error)
}

// SetTwoFAStatus wires the 2FA-enabled lookup surfaced in the profile (GET /v1/me).
func (h *Handler) SetTwoFAStatus(fn func(ctx context.Context, userPublicID string) (bool, error)) {
	h.twoFAStatus = fn
}

func NewHandler(svc *Service, authn *auth.Authenticator, privy *auth.PrivyVerifier, registerRL, loginRL func(http.Handler) http.Handler, dev, xClaim bool) *Handler {
	noop := func(n http.Handler) http.Handler { return n }
	if registerRL == nil {
		registerRL = noop // no-op fallback
	}
	if loginRL == nil {
		loginRL = noop
	}
	return &Handler{svc: svc, authn: authn, privy: privy, registerRL: registerRL, loginRL: loginRL, keysRL: noop, dev: dev, xClaim: xClaim}
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
		r.Post("/v1/auth/magic-link", h.requestMagicLink)
		// Privy token exchange: verify Privy's access token, find-or-create the
		// owner, return a dashboard session. Rate-limited alongside login.
		r.Post("/v1/auth/privy", h.privyLogin)
	})
	// Magic-link verify consumes a single-use token (public; the token is the
	// credential), so it is not IP-rate-limited.
	r.Get("/v1/auth/magic-link/verify", h.verifyMagicLink)

	// Authenticated routes: attach the principal, then guard by scope.
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agent/config", h.updateConfig)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/me", h.me)
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
// returns a ready-to-use dashboard session and a one-time API key.
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
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"dashboard_token": res.DashboardToken,
		"api_key":         res.APIKey, // shown exactly once
		"agent_id":        res.AgentID,
		"agent_name":      res.AgentName,
	})
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
	res, err := h.svc.LogIn(r.Context(), in.Email, in.Password)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"dashboard_token": res.DashboardToken,
		"agent_id":        res.AgentID,
		"agent_name":      res.AgentName,
	})
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
	if in.AgentID == "" {
		httpx.Error(w, errInvalid("agent_id is required"))
		return
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
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	out := map[string]any{
		"user_id":  p.UserPublicID,
		"agent_id": p.AgentPublicID, // empty when using dashboard token only
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

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		AgentID string `json:"agent_id"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if in.AgentID == "" {
		httpx.Error(w, errInvalid("agent_id is required"))
		return
	}
	raw, err := h.svc.RotateKey(r.Context(), p.UserPublicID, in.AgentID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]string{"api_key": raw})
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
	prefix := chi.URLParam(r, "prefix")
	if err := h.svc.RevokeKey(r.Context(), p.UserPublicID, prefix); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusNoContent, nil)
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
	var in struct {
		Email string `json:"email"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
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
