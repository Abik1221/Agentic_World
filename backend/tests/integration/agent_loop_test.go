//go:build integration

// Drives the core developer loop end-to-end against the live server + Postgres +
// Redis: sign up → submit manifest → set endpoint secret → verify the endpoint
// (against an in-test stub agent) → assert the manifest is certified/active →
// read the public agent profile.
//
//	make beta-up
//	BASE_URL=http://localhost:8080 go test -tags=integration ./tests/integration/...
//
// Requires the server to run with AGENT_VERIFY_ALLOW_PRIVATE=true so it may reach
// the loopback stub agent.
package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// stubAgent stands in for a developer's hosted agent: healthy /health and an
// accepting /handshake advertising the games the manifest declares.
func stubAgent(t *testing.T, games []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy", "agent": "AtlasBeta", "version": "1.0.0"})
		case "/handshake":
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "sdkVersion": "1.0.0", "supportedGames": games})
		default:
			w.WriteHeader(404)
		}
	}))
}

func TestAgentLoop_ManifestVerifyToPublicProfile(t *testing.T) {
	c := newClient(t)
	agent := stubAgent(t, []string{"goofspiel", "mafia", "monopoly"})
	defer agent.Close()

	// 1. Sign up (email+password) → owner + first agent + dashboard token.
	uniq := time.Now().UnixNano()
	var signup struct {
		DashboardToken string `json:"dashboard_token"`
		AgentID        string `json:"agent_id"`
	}
	if code := c.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"email":       fmt.Sprintf("dev+%d@example.com", uniq),
		"password":    "hunter2-strong-pass",
		"agent_name":  "AtlasBeta",
		"description": "beta loop agent",
	}, &signup); code != http.StatusCreated {
		t.Fatalf("signup: got %d", code)
	}
	if signup.DashboardToken == "" || signup.AgentID == "" {
		t.Fatalf("signup returned empty token/agent: %+v", signup)
	}
	tok := signup.DashboardToken

	// 2. Submit a manifest for the agent (JSON).
	manifestDoc := map[string]any{
		"manifestVersion": "1.0",
		"agent":           map[string]any{"name": "AtlasBeta", "description": "beta loop agent", "version": "1.0.0", "visibility": "public"},
		"developer":       map[string]any{"name": "Dev One", "organization": "Solo"},
		"games":           []string{"goofspiel"},
		"endpoint":        map[string]any{"url": agent.URL + "/play", "authentication": "bearer-token"},
		"runtime":         map[string]any{"timeout": 5000, "maxMemory": "512MB"},
		"model":           map[string]any{"provider": "OpenAI", "model": "GPT-5.5", "reasoning": true},
		"sdk":             map[string]any{"language": "Go", "version": "1.0.0"},
		"contact":         map[string]any{"email": "dev@example.com"},
	}
	var submitted struct {
		ManifestID string `json:"manifest_id"`
		Status     string `json:"status"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+signup.AgentID+"/manifest", tok, manifestDoc, &submitted); code != http.StatusCreated {
		t.Fatalf("submit manifest: got %d", code)
	}
	if submitted.ManifestID == "" || submitted.Status != "validated" {
		t.Fatalf("unexpected submit result: %+v", submitted)
	}
	mid := submitted.ManifestID

	// 3. Set the endpoint bearer secret (stored encrypted; never returned).
	if code := c.do(http.MethodPut, "/v1/agents/"+signup.AgentID+"/manifest/"+mid+"/endpoint-secret", tok,
		map[string]any{"token": "super-secret-bearer"}, nil); code != http.StatusOK {
		t.Fatalf("set endpoint secret: got %d", code)
	}

	// 4. Verify the endpoint (server calls the loopback stub's /health + /handshake).
	var report struct {
		Verified     bool `json:"verified"`
		HealthOK     bool `json:"health_ok"`
		HandshakeOK  bool `json:"handshake_ok"`
		GamesCovered bool `json:"games_covered"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+signup.AgentID+"/manifest/"+mid+"/verify", tok, nil, &report); code != http.StatusOK {
		t.Fatalf("verify: got %d", code)
	}
	if !report.Verified || !report.HealthOK || !report.HandshakeOK || !report.GamesCovered {
		t.Fatalf("expected full verification, got %+v", report)
	}

	// 5. Read the public agent profile — verified badge + developer-declared model,
	//    no endpoint URL leaked.
	var pub map[string]any
	if code := c.do(http.MethodGet, "/v1/agents/"+signup.AgentID+"/manifest/public", "", nil, &pub); code != http.StatusOK {
		t.Fatalf("public profile: got %d", code)
	}
	if pub["verified"] != true {
		t.Fatalf("expected verified=true in public view, got %+v", pub)
	}
	if _, leaked := pub["endpoint"]; leaked {
		t.Fatal("public view leaked the endpoint URL")
	}
	model, ok := pub["model"].(map[string]any)
	if !ok || model["developer_declared"] != true {
		t.Fatalf("model must be developer-declared: %+v", pub["model"])
	}
	t.Logf("loop OK: agent=%s manifest=%s verified & public", signup.AgentID, mid)
}
