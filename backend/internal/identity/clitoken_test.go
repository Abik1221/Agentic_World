package identity

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/auth"
)

// The CLI handoff used to give a terminal the literal string "cookie:user".
//
// That value is the browser's SENTINEL, not a credential: the real token lives in an HttpOnly
// cookie JS cannot read, and inside the browser the BFF swaps the sentinel for it on every
// call. A terminal has no BFF. It stored "cookie:user", sent it as a Bearer, and every
// owner-scoped command answered 401 — `pyyol publish` first, and therefore certification, and
// therefore every match including sandbox. A freshly logged-in developer could not play at all.
//
// These pin the endpoint that replaces it.

func cliTokenHandler(t *testing.T) (*Handler, *auth.JWT) {
	t.Helper()
	j := auth.NewJWT("test-secret-at-least-32-bytes-long!!", time.Hour)
	svc := &Service{jwt: j}
	return &Handler{svc: svc}, j
}

func TestCliTokenRequiresAnAuthenticatedOwner(t *testing.T) {
	h, _ := cliTokenHandler(t)
	rec := httptest.NewRecorder()
	// No principal in context: an unauthenticated caller must not be handed a credential.
	h.cliToken(rec, httptest.NewRequest(http.MethodPost, "/v1/auth/cli-token", nil))

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 — this endpoint mints an owner credential and must "+
			"never do so for an anonymous caller", rec.Code)
	}
}

func TestCliTokenMintsAUsableOwnerToken(t *testing.T) {
	h, j := cliTokenHandler(t)
	const owner = "usr_cli_test"

	req := httptest.NewRequest(http.MethodPost, "/v1/auth/cli-token", nil)
	req = req.WithContext(auth.ContextWithPrincipal(context.Background(), &auth.Principal{
		UserPublicID: owner, Scope: auth.ScopeUser,
	}))
	rec := httptest.NewRecorder()
	h.cliToken(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		DashboardToken string `json:"dashboard_token"`
		RefreshToken   string `json:"refresh_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	// The single most important assertion in this file: what comes back must be a real token,
	// never the sentinel the page used to pass through.
	if out.DashboardToken == "cookie:user" || out.DashboardToken == "" {
		t.Fatalf("minted token = %q; the CLI needs a real credential, not the browser's "+
			"sentinel", out.DashboardToken)
	}
	// A JWT, and one that actually verifies for this owner — a token that parses but does not
	// authorise would move the 401 rather than remove it.
	if strings.Count(out.DashboardToken, ".") != 2 {
		t.Fatalf("minted token is not a JWT: %q", out.DashboardToken)
	}
	p, err := j.Parse(out.DashboardToken)
	if err != nil {
		t.Fatalf("the minted token does not verify: %v", err)
	}
	if p.UserPublicID != owner {
		t.Fatalf("token authorises %q, want %q", p.UserPublicID, owner)
	}
	if p.Scope != auth.ScopeUser {
		t.Fatalf("token scope = %q, want user — owner commands are what it exists for", p.Scope)
	}
}
