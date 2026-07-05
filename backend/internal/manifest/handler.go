package manifest

import (
	"io"
	"net/http"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/go-chi/chi/v5"
)

// maxManifestBytes caps a submitted manifest document. Manifests are small
// metadata; anything larger is rejected before it is read into memory.
const maxManifestBytes = 64 << 10 // 64 KiB

// Handler exposes the manifest HTTP API under the authenticated owner (user)
// scope. It mirrors identity.Handler's construction/mount conventions.
type Handler struct {
	svc   *Service
	authn *auth.Authenticator
}

func NewHandler(svc *Service, authn *auth.Authenticator) *Handler {
	return &Handler{svc: svc, authn: authn}
}

// Register is an httpx.Mount. Public read routes need no auth; management routes
// require a user-scope principal (the owner), with agent ownership enforced in
// the service.
func (h *Handler) Register(r chi.Router) {
	// Public: profile/spectator surfacing of an agent's active manifest. Omits the
	// endpoint URL and contact email; the model block is tagged developer-declared.
	r.Get("/v1/agents/{agent_id}/manifest/public", h.public)

	r.Group(func(r chi.Router) {
		r.Use(h.authn.Middleware)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agents/{agent_id}/manifest", h.submit)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/agents/{agent_id}/manifest", h.active)
		r.With(auth.RequireScope(auth.ScopeUser)).Get("/v1/agents/{agent_id}/manifest/versions", h.versions)
		r.With(auth.RequireScope(auth.ScopeUser)).Put("/v1/agents/{agent_id}/manifest/{manifest_id}/endpoint-secret", h.setSecret)
		r.With(auth.RequireScope(auth.ScopeUser)).Post("/v1/agents/{agent_id}/manifest/{manifest_id}/verify", h.verify)
	})
}

func (h *Handler) setSecret(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	var in struct {
		Token string `json:"token"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.Error(w, err)
		return
	}
	err := h.svc.SetEndpointSecret(r.Context(), p.UserPublicID,
		chi.URLParam(r, "agent_id"), chi.URLParam(r, "manifest_id"), in.Token)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]string{"status": "stored"})
}

func (h *Handler) public(w http.ResponseWriter, r *http.Request) {
	m, err := h.svc.PublicActive(r.Context(), chi.URLParam(r, "agent_id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, publicView(m))
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	report, err := h.svc.Verify(r.Context(), p.UserPublicID,
		chi.URLParam(r, "agent_id"), chi.URLParam(r, "manifest_id"))
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, report)
}

func (h *Handler) submit(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agentID := chi.URLParam(r, "agent_id")

	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxManifestBytes))
	if err != nil {
		httpx.Error(w, httpx.NewError(http.StatusRequestEntityTooLarge, "manifest_too_large",
			"The manifest document is too large."))
		return
	}

	m, err := h.svc.Submit(r.Context(), p.UserPublicID, agentID, r.Header.Get("Content-Type"), raw)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, view(m))
}

func (h *Handler) active(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agentID := chi.URLParam(r, "agent_id")
	m, err := h.svc.Active(r.Context(), p.UserPublicID, agentID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view(m))
}

func (h *Handler) versions(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalFromContext(r.Context())
	agentID := chi.URLParam(r, "agent_id")
	ms, err := h.svc.Versions(r.Context(), p.UserPublicID, agentID)
	if err != nil {
		httpx.Error(w, err)
		return
	}
	out := make([]map[string]any, len(ms))
	for i, m := range ms {
		out[i] = view(m)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"versions": out})
}

// view renders a Manifest for the API. The model block is always tagged
// developer-declared: the platform cannot verify a remote API's model.
func view(m Manifest) map[string]any {
	out := map[string]any{
		"manifest_id":      m.PublicID,
		"agent_id":         m.AgentPublicID,
		"manifest_version": m.ManifestVersion,
		"agent_version":    m.AgentVersion,
		"name":             m.Name,
		"description":      m.Description,
		"visibility":       m.Visibility,
		"developer": map[string]any{
			"name":         m.DeveloperName,
			"organization": m.Organization,
		},
		"games": m.Games,
		"endpoint": map[string]any{
			"url":            m.EndpointURL,
			"authentication": m.AuthType,
		},
		"runtime": map[string]any{
			"timeout":    m.RuntimeTimeoutMs,
			"max_memory": m.RuntimeMaxMemory,
		},
		"sdk": map[string]any{
			"language": m.SDKLanguage,
			"version":  m.SDKVersion,
		},
		"contact": map[string]any{"email": m.ContactEmail},
		"status":  m.Status,
	}
	if !m.CreatedAt.IsZero() {
		out["created_at"] = m.CreatedAt
	}
	if m.Model != nil {
		out["model"] = map[string]any{
			"provider":           m.Model.Provider,
			"model":              m.Model.Model,
			"reasoning":          m.Model.Reasoning,
			"developer_declared": true,
		}
	}
	return out
}

// publicView renders a manifest for an UNAUTHENTICATED audience (agent profile,
// spectator UI). It deliberately omits the endpoint URL (attack surface) and
// contact email (PII), and surfaces the verification badge + developer-declared
// model attribution.
func publicView(m Manifest) map[string]any {
	out := map[string]any{
		"agent_id":         m.AgentPublicID,
		"name":             m.Name,
		"description":      m.Description,
		"agent_version":    m.AgentVersion,
		"manifest_version": m.ManifestVersion,
		"visibility":       m.Visibility,
		"developer": map[string]any{
			"name":         m.DeveloperName,
			"organization": m.Organization,
		},
		"games": m.Games,
		"sdk": map[string]any{
			"language": m.SDKLanguage,
			"version":  m.SDKVersion,
		},
		"status":   m.Status,
		"verified": m.Status == StatusVerified,
	}
	if m.Model != nil {
		// "Built using GPT-5.5" / "Powered by Claude" — always developer-declared.
		out["model"] = map[string]any{
			"provider":           m.Model.Provider,
			"model":              m.Model.Model,
			"reasoning":          m.Model.Reasoning,
			"developer_declared": true,
		}
	}
	return out
}
