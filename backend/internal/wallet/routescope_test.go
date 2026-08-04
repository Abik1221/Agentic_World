package wallet

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// THE PRIVILEGE BOUNDARY BETWEEN AN AGENT KEY AND ITS OWNER, asserted at the route table.
//
// An agent API key resolves to a Principal carrying its OWNER's UserPublicID — that is by
// design, it is how an agent's spending is attributed. The consequence is that any handler
// which authorises by reading p.UserPublicID has performed an IDENTITY check and no
// authorisation at all: an agent key passes it exactly as the owner would.
//
// That is how the allocate bug worked, and /v1/user/wallet and /v1/user/wallet/history had
// the same shape — no scope guard, and handlers that only checked `p.UserPublicID != ""`.
// An agent key could read its owner's treasury balance and their whole ledger. Agent keys
// are deployed to containers and CI runners and are expected to leak; the owner's financial
// history is not something a leaked key should disclose.
//
// These tests go through the REAL Register() wiring — the actual Authenticator, the actual
// chi router, a genuine agent API key — because the property being protected lives in the
// route table, not in any function. A unit test of RequireScope would keep passing if
// someone dropped the guard from a route, which is the only way this regresses.

// stubKeys resolves any agent key to an agent-scope principal owned by ownerID, which is
// exactly what the real resolver does for a live key.
type stubKeys struct{ ownerID, agentID string }

func (s stubKeys) ResolveAgentKey(_ context.Context, _ string) (*auth.Principal, error) {
	return &auth.Principal{Scope: auth.ScopeAgent, UserPublicID: s.ownerID, AgentPublicID: s.agentID}, nil
}

const testSigningKey = "test-signing-key-for-route-scope-assertions"

func testRouter(t *testing.T) (chi.Router, string) {
	t.Helper()
	jwt := auth.NewJWT(testSigningKey, time.Hour)
	authn := auth.NewAuthenticator(
		stubKeys{ownerID: "usr_owner", agentID: "ag_1"},
		jwt,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	// svc is nil on purpose: a request that reaches the handler would panic, so any test
	// below that expects a rejection is proving the guard rejected BEFORE the handler ran
	// rather than the handler happening to return an error of its own.
	h := NewHandler(nil, authn, false, nil)
	r := chi.NewRouter()
	h.Register(r)

	userToken, err := jwt.Issue("usr_owner")
	if err != nil {
		t.Fatalf("issue user token: %v", err)
	}
	return r, userToken
}

// agentKey is shaped like a real one so Authenticator.resolve routes it to the key
// resolver rather than parsing it as a JWT.
var agentKey = platform.PrefixKey + "_live_abc123"

func status(t *testing.T, r chi.Router, path, credential string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+credential)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code
}

func TestAgentKeyCannotReadItsOwnersTreasury(t *testing.T) {
	r, _ := testRouter(t)

	for _, path := range []string{"/v1/user/wallet", "/v1/user/wallet/history"} {
		t.Run(path, func(t *testing.T) {
			got := status(t, r, path, agentKey)
			if got != http.StatusForbidden {
				t.Fatalf("%s with an agent key = %d, want %d.\n"+
					"An agent key carries its owner's UserPublicID, so a handler-level "+
					"`p.UserPublicID != \"\"` check admits it. This route needs "+
					"auth.RequireScope(auth.ScopeUser) at the router.", path, got, http.StatusForbidden)
			}
		})
	}
}

func TestOwnerTreasuryRoutesStillAdmitAUserToken(t *testing.T) {
	r, userToken := testRouter(t)

	// A user token must get PAST the guard. It then reaches the handler with a nil service
	// and panics — which is the proof we want: the guard let it through. Anything else
	// (401/403) would mean the fix locked the owner out of their own treasury.
	for _, path := range []string{"/v1/user/wallet", "/v1/user/wallet/history"} {
		t.Run(path, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("%s with a user token did not reach the handler; the scope guard "+
						"rejected the owner", path)
				}
			}()
			_ = status(t, r, path, userToken)
		})
	}
}

func TestAgentWalletRoutesAdmitBothAgentAndOwner(t *testing.T) {
	// /v1/wallet is read by the SDK with an agent key AND by the dashboard with a user
	// token and ?agent=. Narrowing it to one scope would break one of those callers, so
	// the guard is RequireScopeAny — this pins that it was not "tightened" into a
	// regression while the treasury routes next door were being fixed.
	r, userToken := testRouter(t)

	for _, cred := range []struct {
		name, credential string
	}{
		{"agent key", agentKey},
		{"user token", userToken},
	} {
		t.Run(cred.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("/v1/wallet with a %s did not reach the handler; the scope guard "+
						"rejected a legitimate caller", cred.name)
				}
			}()
			_ = status(t, r, "/v1/wallet", cred.credential)
		})
	}
}

func TestUnauthenticatedCannotReachAnyWalletRoute(t *testing.T) {
	r, _ := testRouter(t)

	// authn.Middleware is deliberately OPTIONAL authentication — no credential means the
	// request proceeds unauthenticated so public routes still work. Every wallet route is
	// private, so the scope guard is the only thing standing between an anonymous request
	// and a nil-service panic. 401 proves the guard, not the handler, turned it away.
	for _, path := range []string{
		"/v1/wallet", "/v1/wallet/history", "/v1/user/wallet", "/v1/user/wallet/history",
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s with no credential = %d, want %d", path, rec.Code, http.StatusUnauthorized)
			}
		})
	}
}
