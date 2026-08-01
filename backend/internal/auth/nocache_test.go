package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A credential-scoped response must never sit in a shared cache. Without an explicit
// directive an intermediary may cache heuristically, and a CDN or corporate proxy that
// did so would serve one developer's profile, wallet or match telemetry to whoever
// asked for the same URL next. It leaks silently, which is why this is asserted at the
// guard rather than trusted to each handler.
func TestAuthedResponsesAreNotSharedCacheable(t *testing.T) {
	guards := map[string]func(http.Handler) http.Handler{
		"RequireScope":           RequireScope(ScopeUser),
		"RequireScopeAny":        RequireScopeAny(ScopeUser, ScopePlatform),
		"RequirePlatformOrAdmin": RequirePlatformOrAdmin(map[string]bool{"usr_1": true}),
	}

	for name, guard := range guards {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil).
			WithContext(context.WithValue(context.Background(), principalKey,
				&Principal{Scope: ScopeUser, UserPublicID: "usr_1"}))

		guard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("%s: guard rejected a valid principal (%d)", name, rec.Code)
		}
		cc := rec.Header().Get("Cache-Control")
		if !strings.Contains(cc, "no-store") {
			t.Fatalf("%s: Cache-Control = %q, must contain no-store", name, cc)
		}
		if !strings.Contains(cc, "private") {
			t.Fatalf("%s: Cache-Control = %q, must contain private", name, cc)
		}
		vary := strings.Join(rec.Header().Values("Vary"), ",")
		if !strings.Contains(vary, "Authorization") || !strings.Contains(vary, "Cookie") {
			t.Fatalf("%s: Vary = %q, must key on Authorization and Cookie", name, vary)
		}
	}
}

// A rejected request must not be cached either — a cached 403 for one caller would be
// served to a caller who IS allowed.
func TestRejectionsAreNotCacheableAsSuccess(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/anything", nil).
		WithContext(context.WithValue(context.Background(), principalKey,
			&Principal{Scope: ScopeAgent, AgentPublicID: "ag_1"}))

	RequireScope(ScopeUser)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("handler ran despite a wrong-scope credential")
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong-scope credential got %d, want 403", rec.Code)
	}
}
