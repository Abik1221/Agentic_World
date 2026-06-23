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
	keys KeyResolver
	jwt  *JWT
	log  *slog.Logger
}

func NewAuthenticator(keys KeyResolver, jwt *JWT, log *slog.Logger) *Authenticator {
	return &Authenticator{keys: keys, jwt: jwt, log: log}
}

type ctxKey int

const principalKey ctxKey = iota

// Middleware performs optional authentication: if a Bearer credential is present
// it MUST be valid (else 401) and the resulting Principal is attached to context;
// if absent, the request proceeds unauthenticated so public routes still work.
// Authorization is enforced separately by RequireScope.
func (a *Authenticator) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
