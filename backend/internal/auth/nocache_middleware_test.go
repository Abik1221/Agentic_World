package auth

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The scope guards were already covered, but they were never the whole story: a route
// can authenticate with Middleware alone and never touch a scope guard. Every wallet
// route did exactly that — /v1/wallet, /v1/wallet/history, /v1/user/wallet and
// /v1/user/wallet/history mount bare authn.Middleware — so balances and ledger history
// went out with no cache directive at all. These pin the fix to authentication, so a
// future route cannot opt out of it by simply not using a scope guard.
func newTestAuthenticator() *Authenticator {
	return NewAuthenticator(
		nil,
		NewJWT("test-signing-key-not-a-secret", time.Hour),
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
}

func TestMiddlewareMarksAuthedResponsesPrivate(t *testing.T) {
	a := newTestAuthenticator()
	tok, err := a.jwt.Issue("usr_cache_probe")
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/wallet", nil)
	req.Header.Set("Authorization", "Bearer "+tok)

	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("valid credential rejected: %d", rec.Code)
	}
	cc := rec.Header().Get("Cache-Control")
	if !strings.Contains(cc, "no-store") || !strings.Contains(cc, "private") {
		t.Fatalf("Cache-Control = %q, want no-store + private", cc)
	}
	vary := strings.Join(rec.Header().Values("Vary"), ",")
	if !strings.Contains(vary, "Authorization") || !strings.Contains(vary, "Cookie") {
		t.Fatalf("Vary = %q, must key on Authorization and Cookie", vary)
	}
}

// A request carrying no credential is not caller-scoped, so it keeps whatever caching
// the handler chooses. Marking these no-store would make genuinely public, cacheable
// responses uncacheable for everyone.
func TestMiddlewareLeavesUnauthenticatedResponsesAlone(t *testing.T) {
	a := newTestAuthenticator()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/public-thing", nil)

	a.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rec, req)

	if cc := rec.Header().Get("Cache-Control"); cc != "" {
		t.Fatalf("unauthenticated response got Cache-Control %q, want none", cc)
	}
}
