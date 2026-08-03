package walletverify

import (
	"context"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// StepUp verifies a second factor (TOTP) for the acting user; a no-op when 2FA is
// off. Wired from the twofa service via SetStepUp.
type StepUp interface {
	Require(ctx context.Context, userPublicID, code string) error
}

// Handler exposes the wallet-ownership verification surface (user scope): request
// a challenge, then submit the wallet's signature. Identity is taken from the
// token, never the body.
type Handler struct {
	svc    *Service
	authn  *auth.Authenticator
	stepUp StepUp
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// SetStepUp wires the 2FA step-up applied when a wallet is verified/linked (a
// sensitive change to the payout destination). Optional.
func (h *Handler) SetStepUp(s StepUp) { h.stepUp = s }

func (h *Handler) Register(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		user := auth.RequireScope(auth.ScopeUser)
		r.With(user).Post("/v1/wallet/verify/challenge", h.challenge)
		r.With(user).Post("/v1/wallet/verify", h.verify)
		r.With(user).Post("/v1/wallet/verify/unlink", h.unlink)
		// Which wallet is connected in the browser. Display hints only: recording a
		// connection can never make an address payable — that needs the signed
		// challenge above. See connected.go for why the hint is filled and never
		// repointed.
		r.With(user).Post("/v1/wallet/connected", h.recordConnected)
		r.With(user).Get("/v1/wallet/connected", h.connected)
	})
}

func (h *Handler) challenge(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		WalletAddress string `json:"wallet_address"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	msg, nonce, err := h.svc.StartChallenge(r.Context(), p.UserPublicID, in.WalletAddress)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	// The client asks the wallet to sign `message` and returns the base58 signature.
	httpx.JSON(w, http.StatusOK, map[string]any{"message": msg, "nonce": nonce})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		WalletAddress string `json:"wallet_address"`
		Signature     string `json:"signature"` // base58
		TOTPCode      string `json:"totp_code"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	// Step-up: changing the payout destination is sensitive, so when the user has 2FA
	// enabled, require a valid authenticator code in addition to the wallet signature.
	if h.stepUp != nil {
		if err := h.stepUp.Require(r.Context(), p.UserPublicID, in.TOTPCode); err != nil {
			httpx.Error(w, err)
			return
		}
	}
	if err := h.svc.Verify(r.Context(), p.UserPublicID, in.WalletAddress, in.Signature); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"verified": true, "wallet_address": in.WalletAddress})
}

// unlink removes the linked payout wallet, so a user can take their wallet off the
// account entirely. Gated by the same 2FA step-up as verify: both change where money
// can be sent, so both need the second factor when the user has one.
func (h *Handler) unlink(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		TOTPCode string `json:"totp_code"`
	}
	// Body is required (send `{}` when 2FA is off) — DecodeJSON rejects an empty body.
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if h.stepUp != nil {
		if err := h.stepUp.Require(r.Context(), p.UserPublicID, in.TOTPCode); err != nil {
			httpx.Error(w, err)
			return
		}
	}
	if err := h.svc.Unlink(r.Context(), p.UserPublicID); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"unlinked": true})
}

// recordConnected saves the wallet the browser just connected, so the dashboard can
// name it after a reload and an operator can see it in the admin panel.
func (h *Handler) recordConnected(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		WalletAddress string `json:"wallet_address"`
		Provider      string `json:"wallet_provider"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	// Deliberately NO 2FA step-up here, unlike verify/unlink. This writes display hints
	// and cannot move or redirect money, so demanding a code would train people to enter
	// one for a harmless action — which is how a step-up prompt stops meaning anything.
	out, err := h.svc.RecordConnected(r.Context(), p.UserPublicID, in.WalletAddress, in.Provider)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, out)
}

// connected reads the wallet on file for this developer.
func (h *Handler) connected(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	out, err := h.svc.Connected(r.Context(), p.UserPublicID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	httpx.JSON(w, http.StatusOK, out)
}
