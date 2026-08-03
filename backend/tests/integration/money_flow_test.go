//go:build integration

// Package integration drives the real HTTP server (live Postgres + Redis) to
// exercise the money path end to end. Run against a started stack:
//
//	make compose-up
//	BASE_URL=http://localhost:8080 go test -tags=integration ./tests/integration/...
//
// These are black-box HTTP tests — no imports of internal packages — so they
// assert the public contract exactly as an agent/owner would experience it.
package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"
)

func baseURL() string {
	if v := os.Getenv("BASE_URL"); v != "" {
		return v
	}
	return "http://localhost:8080"
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func newClient(t *testing.T) *client {
	return &client{t: t, base: baseURL(), http: &http.Client{Timeout: 10 * time.Second}}
}

// do issues a request (optionally authed) and decodes a JSON body into out.
func (c *client) do(method, path, bearer string, body any, out any) int {
	c.t.Helper()
	return c.doAuth(method, path, "Bearer", bearer, body, out)
}

// doPlatform is do() with the PLATFORM auth scheme.
//
// A Platform token is not a Bearer credential. internal/auth reads it from
// `Authorization: Platform <token>` and deliberately never falls through to the Bearer
// paths — so sending one as a Bearer had it parsed as a user JWT, which failed, and every
// call came back 401.
//
// That is exactly what was happening, and it broke the E2E job: the cert-gate test needs to
// mint a stake and died on `mint stake: got 401`. The money-flow test made the same call and
// SKIPPED on 401 with the message "PLATFORM_ADMIN_PUBLIC_KEY is not the e2e key" — which was
// not true (the seed and the workflow's public key do match), and the skip is what let the
// mistake sit here looking like an environment problem.
func (c *client) doPlatform(method, path string, body any, out any) int {
	c.t.Helper()
	return c.doAuth(method, path, "Platform", platformToken(), body, out)
}

func (c *client) doAuth(method, path, scheme, cred string, body any, out any) int {
	c.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, c.base+path, rdr)
	if err != nil {
		c.t.Fatalf("new request %s %s: %v", method, path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cred != "" {
		req.Header.Set("Authorization", scheme+" "+cred)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

// TestSmoke confirms the stack is up and the API contract is served.
func TestSmoke(t *testing.T) {
	c := newClient(t)
	if code := c.do(http.MethodGet, "/healthz", "", nil, nil); code != 200 {
		t.Fatalf("/healthz = %d, want 200 (is the stack up? `make compose-up`)", code)
	}
	if code := c.do(http.MethodGet, "/readyz", "", nil, nil); code != 200 {
		t.Fatalf("/readyz = %d, want 200", code)
	}
	if code := c.do(http.MethodGet, "/openapi.yaml", "", nil, nil); code != 200 {
		t.Fatalf("/openapi.yaml = %d, want 200", code)
	}
}

// onboard runs the dev onboarding flow (DevClaimVerifier auto-verifies) and
// returns (apiKey, agentID, dashboardToken).
func (c *client) onboard(name string) (string, string, string) {
	c.t.Helper()
	var reg struct {
		ClaimToken string `json:"claim_token"`
	}
	if code := c.do(http.MethodPost, "/v1/register", "", map[string]any{"agent_name": name}, &reg); code != 201 {
		c.t.Fatalf("register = %d, want 201", code)
	}
	var ver struct {
		APIKey         string `json:"api_key"`
		AgentID        string `json:"agent_id"`
		DashboardToken string `json:"dashboard_token"`
	}
	code := c.do(http.MethodGet, "/v1/register/verify?claim_token="+reg.ClaimToken+"&captcha=dev", "", nil, &ver)
	if code != 200 {
		c.t.Skipf("verify = %d (dev claim verifier not enabled in this env); skipping money flow", code)
	}
	return ver.APIKey, ver.AgentID, ver.DashboardToken
}

// TestBuyCreditsWallet exercises the deposit half: mint (non-prod stand-in for a
// settled Stripe top-up) must credit the agent's wallet exactly.
func TestBuyCreditsWallet(t *testing.T) {
	c := newClient(t)
	apiKey, agentID, dash := c.onboard("itest-buyer")

	// Non-prod mint = the test path for "coins bought". Requires ALLOW_MINT=true.
	// Called with a Platform token, not the developer's dashboard token: mint is
	// admin-guarded precisely so a self-registered developer cannot credit their own
	// wallet, so the harness authenticates as the platform would.
	code := c.doPlatform(http.MethodPost, "/v1/admin/mint", map[string]any{"agent": agentID, "amount": 500}, nil)
	if code == 404 {
		t.Skip("mint disabled (ALLOW_MINT=false); enable it or wire Stripe test mode to run this")
	}
	// 401/403 is a FAILURE, not a skip.
	//
	// It used to skip with "PLATFORM_ADMIN_PUBLIC_KEY is not the e2e key", and that guess was
	// wrong — the seed here and the workflow's public key are a matching pair (verified). The
	// real cause was this call sending a Platform token under the Bearer scheme. Skipping on
	// an auth rejection turned a broken harness into a green run, so the one test that did
	// fail on it (cert_gate) looked like the odd one out.
	if code != 200 {
		t.Fatalf("mint = %d, want 200 (a Platform token must be sent as `Authorization: Platform …`)", code)
	}

	var w struct {
		Balance int64 `json:"balance"`
	}
	if code := c.do(http.MethodGet, "/v1/wallet", apiKey, nil, &w); code != 200 {
		t.Fatalf("wallet = %d, want 200", code)
	}
	if w.Balance < 500 {
		t.Fatalf("balance = %d, want >= 500 after mint", w.Balance)
	}

	// THE SAME READ WITH A DASHBOARD TOKEN AND NO ?agent=.
	//
	// `dash` was captured here and never used, which is why this file did not compile and
	// the whole E2E job failed at the build step — so nothing in this package had run for
	// however long that had been true.
	//
	// Filling it in with the case that was actually missing: a user-scoped token carries no
	// agent id, so this used to 400 unless the caller named an agent — and the browser's
	// only source for that name was a cookie written at login. On a second device, after
	// clearing cookies, or on a session restored from a refresh token it was absent, so the
	// Strategy page read no limits (rendering every guardrail as 0) and saving them came
	// back "Failed to save config". The server resolves the caller's own agent now, and
	// this is the black-box proof of it.
	var byUser struct {
		Agent   string `json:"agent"`
		Balance int64  `json:"balance"`
	}
	if code := c.do(http.MethodGet, "/v1/wallet", dash, nil, &byUser); code != 200 {
		t.Fatalf("wallet with a dashboard token and no ?agent= = %d, want 200 — a user must be "+
			"able to read their own wallet without naming the agent", code)
	}
	if byUser.Agent != agentID {
		t.Fatalf("resolved agent = %q, want the caller's own agent %q", byUser.Agent, agentID)
	}
	if byUser.Balance != w.Balance {
		t.Fatalf("balance via dashboard token = %d, via agent key = %d — the same wallet must "+
			"read the same either way", byUser.Balance, w.Balance)
	}
}

// TODO(withdrawal): once the payout engine lands, extend this file with the full
// loop — buy → stake (two agents) → settle → request withdrawal → approve →
// execute → assert wallet debited, payout recorded, and reconciliation zero-drift.
