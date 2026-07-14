package twofa

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes TOTP enrollment + status (user scope). Identity is taken from the
// token, never the body. Step-up verification for money movement lives in the
// withdrawal / wallet-verify paths (they call Service.Require), not here.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		r.With(user).Get("/v1/auth/2fa", h.status)
		r.With(user).Post("/v1/auth/2fa/setup", h.setup)
		r.With(user).Post("/v1/auth/2fa/confirm", h.confirm)
		r.With(user).Post("/v1/auth/2fa/disable", h.disable)
		r.With(user).Post("/v1/auth/2fa/recovery-codes", h.regenerate)
	})
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	enabled, err := h.svc.Enabled(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	remaining := 0
	if enabled {
		if n, err := h.svc.RecoveryRemaining(r.Context(), p.UserPublicID); err == nil {
			remaining = n
		}
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"enabled": enabled, "recovery_codes_remaining": remaining})
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	secret, uri, err := h.svc.Setup(r.Context(), p.UserPublicID, p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// The client renders otpauth_uri as a QR (or shows `secret` for manual entry),
	// then POSTs a code to /confirm. 2FA is NOT active until confirmed.
	httpx.JSON(w, http.StatusOK, map[string]any{"secret": secret, "otpauth_uri": uri})
}

func (h *Handler) confirm(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	codes, err := h.svc.Confirm(r.Context(), p.UserPublicID, in.Code)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// recovery_codes are returned ONCE — the client must prompt the user to save them.
	httpx.JSON(w, http.StatusOK, map[string]any{"enabled": true, "recovery_codes": codes})
}

func (h *Handler) regenerate(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Code string `json:"code"` // a current authenticator code
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	codes, err := h.svc.RegenerateRecoveryCodes(r.Context(), p.UserPublicID, in.Code)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

func (h *Handler) disable(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Code string `json:"code"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.Disable(r.Context(), p.UserPublicID, in.Code); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"enabled": false})
}
