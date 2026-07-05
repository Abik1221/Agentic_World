// Package auth resolves credentials into a scoped Principal and provides the
// route guards that enforce the agent-vs-user firewall. It depends on a small
// KeyResolver interface (satisfied by the identity module) plus a JWT verifier,
// never on the modules themselves — so there are no import cycles.
package auth

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/platform"
)

// Scope is the privilege tier of a credential. The separation between agent and
// user scope is the core security firewall: an agent key can play within its
// limits but can never change limits or move money out.
type Scope string

const (
	ScopeAgent Scope = "agent"
	ScopeUser  Scope = "user"
	// ScopePlatform is a service-to-service credential minted by the Super Admin
	// (an Ed25519 "Platform" token, verified with the config-bus public key). It
	// is treated as an administrator: it may reach the admin endpoints without
	// being in the ADMIN_USER_IDS user allowlist.
	ScopePlatform Scope = "platform"
)

// Principal is the authenticated caller attached to the request context.
type Principal struct {
	Scope         Scope
	UserPublicID  string
	AgentPublicID string // populated only for agent scope
}

// KeyResolver maps a raw agent API key to its Principal. Implemented by identity.
type KeyResolver interface {
	ResolveAgentKey(ctx context.Context, rawKey string) (*Principal, error)
}

// Authenticator authenticates requests; route guards (RequireScope) authorize them.
type Authenticator struct {
	keys     KeyResolver
	jwt      *JWT
	platform *PlatformVerifier // service-to-service "Platform" tokens; nil ⇒ disabled
	log      *slog.Logger
}

// NewAuthenticator wires the credential resolvers. platform may be nil (no
// PLATFORM_ADMIN_PUBLIC_KEY configured), in which case any Platform token is
// rejected while the existing Bearer paths (agent key / user JWT) are unaffected.
func NewAuthenticator(keys KeyResolver, jwt *JWT, platform *PlatformVerifier, log *slog.Logger) *Authenticator {
	return &Authenticator{keys: keys, jwt: jwt, platform: platform, log: log}
}

type ctxKey int

const principalKey ctxKey = iota

// Middleware performs optional authentication: if a Bearer credential is present
// it MUST be valid (else 401) and the resulting Principal is attached to context;
// if absent, the request proceeds unauthenticated so public routes still work.
// Authorization is enforced separately by RequireScope.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Service-to-service admin: an "Authorization: Platform <token>" header is
		// an Ed25519 token minted by the Super Admin. If present it MUST verify
		// (else 401); it never falls through to the Bearer paths.
		if tok := platformCredential(r); tok != "" {
			p, err := a.platform.Verify(tok)
			if err != nil {
				a.log.Debug("platform authentication failed", "error", err, "path", r.URL.Path)
				httpx.Error(w, httpx.ErrUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), principalKey, p)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		raw := bearer(r)
		if raw == "" {
			next.ServeHTTP(w, r)
			return
		}
		p, err := a.resolve(r.Context(), raw)
		if err != nil {
			a.log.Debug("authentication failed", "error", err, "path", r.URL.Path)
			httpx.Error(w, httpx.ErrUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), principalKey, p)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolve picks the credential type by shape: an agent API key (prefixed) is
// looked up via the resolver; anything else is treated as a user JWT.
func (a *Authenticator) resolve(ctx context.Context, raw string) (*Principal, error) {
	if strings.HasPrefix(raw, platform.PrefixKey+"_") {
		return a.keys.ResolveAgentKey(ctx, raw)
	}
	return a.jwt.Parse(raw)
}

// RequireScope returns a guard that admits only callers holding exactly the given
// scope. Missing principal → 401; wrong scope → 403 (the limit-firewall response
// for an agent key hitting a user-only route).
func RequireScope(scope Scope) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := PrincipalFromContext(r.Context())
			if p == nil {
				httpx.Error(w, httpx.ErrUnauthorized)
				return
			}
			if p.Scope != scope {
				code := "forbidden_scope"
				msg := "This credential is not allowed to access this resource."
				if scope == ScopeUser && p.Scope == ScopeAgent {
					code, msg = "agent_cannot_modify_limits", "Agent keys cannot perform owner-only actions. Use your dashboard credential."
				}
				httpx.Error(w, httpx.NewError(http.StatusForbidden, code, msg))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// RequireScopeAny returns a guard admitting any caller whose scope is in the
// given set (missing principal → 401, other scope → 403). It is the additive
// relaxation used on the admin write routes so a Platform token can reach them
// alongside the existing user credential; per-handler admin allowlist checks
// still apply.
func RequireScopeAny(scopes ...Scope) func(http.Handler) http.Handler {
	allowed := make(map[Scope]bool, len(scopes))
	for _, s := range scopes {
		allowed[s] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := PrincipalFromContext(r.Context())
			if p == nil {
				httpx.Error(w, httpx.ErrUnauthorized)
				return
			}
			if !allowed[p.Scope] {
				httpx.Error(w, httpx.NewError(http.StatusForbidden, "forbidden_scope", "This credential is not allowed to access this resource."))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// IsAdmin reports whether the principal may perform administrator actions: a
// Platform (Super Admin) token always qualifies; otherwise the user's public id
// must be in the ADMIN_USER_IDS allowlist. This is the single predicate the
// per-handler admin checks share.
func IsAdmin(p *Principal, allowlist map[string]bool) bool {
	if p == nil {
		return false
	}
	if p.Scope == ScopePlatform {
		return true
	}
	return allowlist[p.UserPublicID]
}

// RequirePlatformOrAdmin returns a guard admitting a valid Platform token OR a
// user in the admin allowlist; everyone else gets 403 (missing principal → 401).
// Used for the read-only admin endpoints.
func RequirePlatformOrAdmin(allowlist map[string]bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := PrincipalFromContext(r.Context())
			if p == nil {
				httpx.Error(w, httpx.ErrUnauthorized)
				return
			}
			if !IsAdmin(p, allowlist) {
				httpx.Error(w, httpx.ErrForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// PrincipalFromContext returns the authenticated principal, or nil.
func PrincipalFromContext(ctx context.Context) *Principal {
	if p, ok := ctx.Value(principalKey).(*Principal); ok {
		return p
	}
	return nil
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) > len(p) && strings.EqualFold(h[:len(p)], p) {
		return strings.TrimSpace(h[len(p):])
	}
	return ""
}
