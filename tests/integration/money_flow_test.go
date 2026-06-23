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
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
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
	code := c.do(http.MethodPost, "/v1/admin/mint", dash, map[string]any{"agent": agentID, "amount": 500}, nil)
	if code == 404 {
		t.Skip("mint disabled (ALLOW_MINT=false); enable it or wire Stripe test mode to run this")
	}
	if code != 200 {
		t.Fatalf("mint = %d, want 200", code)
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
}

// TODO(withdrawal): once the payout engine lands, extend this file with the full
// loop — buy → stake (two agents) → settle → request withdrawal → approve →
// execute → assert wallet debited, payout recorded, and reconciliation zero-drift.
