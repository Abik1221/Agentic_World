package walletverify

import (
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// Handler exposes the wallet-ownership verification surface (user scope): request
// a challenge, then submit the wallet's signature. Identity is taken from the
// token, never the body.
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
		r.With(user).Post("/v1/wallet/verify/challenge", h.challenge)
		r.With(user).Post("/v1/wallet/verify", h.verify)
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
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	if err := h.svc.Verify(r.Context(), p.UserPublicID, in.WalletAddress, in.Signature); err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"verified": true, "wallet_address": in.WalletAddress})
}
