//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Certification is a precondition for PLAY, not just for staking.
//
// A user's agent certifies on every table — staked or practice. The sandbox stakes
// nothing, so it looks like it should be exempt, but a sandbox match is not inert: it
// writes decision and benchmark rows that feed the P-Index, the model board and the
// deception index. A scripted agent farming free tables would build a public record it
// did not earn, which is the same fraud as winning coins with one, paid in reputation
// instead of currency. Only the platform's own bots are exempt.
//
// This helper exists because a test that wants to PLAY has to get past that gate, and
// the sequence is long enough (manifest → endpoint secret → endpoint verification
// against a live stub) that copying it invites drift. It was extracted from
// TestAgentLoop_ManifestVerifyToPublicProfile, which asserts each step in detail; here
// the steps are only a means to a certified agent.

// certifiedStub stands in for a developer's hosted agent during certification: a healthy
// /health and an accepting /handshake advertising the games the manifest declares.
func certifiedStub(t *testing.T, name string, games []string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "healthy", "agent": name, "version": "1.0.0"})
		case "/handshake":
			_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "sdkVersion": "1.0.0", "supportedGames": games})
		default:
			w.WriteHeader(404)
		}
	}))
}

// certifyAgent takes a freshly signed-up agent through to an active, endpoint-verified
// manifest, so it may enter a table. `tok` is the DASHBOARD token (manifest operations
// are owner-scoped; an agent key cannot perform them).
//
// The caller owns the returned stub and must Close it — but note the endpoint stays
// registered, so anything that re-verifies after Close will fail. Close at test end.
//
// Requires the server to run with AGENT_VERIFY_ALLOW_PRIVATE=true so it may reach the
// loopback stub.
func certifyAgent(t *testing.T, c *client, tok, agentID, name string, games []string) *httptest.Server {
	t.Helper()
	stub := certifiedStub(t, name, games)

	gameList := make([]any, 0, len(games))
	for _, g := range games {
		gameList = append(gameList, g)
	}
	manifest := map[string]any{
		"manifestVersion": "1.0",
		"agent":           map[string]any{"name": name, "description": "integration agent", "version": "1.0.0", "visibility": "public"},
		"developer":       map[string]any{"name": "Dev One", "organization": "Solo"},
		"games":           gameList,
		"endpoint":        map[string]any{"url": stub.URL + "/play", "authentication": "bearer-token"},
		"runtime":         map[string]any{"timeout": 5000, "maxMemory": "512MB"},
		"model":           map[string]any{"provider": "OpenAI", "model": "GPT-5.5", "reasoning": true},
		"sdk":             map[string]any{"language": "Go", "version": "1.0.0"},
		"contact":         map[string]any{"email": "dev@example.com"},
	}
	var submitted struct {
		ManifestID string `json:"manifest_id"`
		Status     string `json:"status"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+agentID+"/manifest", tok, manifest, &submitted); code != http.StatusCreated {
		stub.Close()
		t.Fatalf("certify: submit manifest: got %d", code)
	}
	mid := submitted.ManifestID

	if code := c.do(http.MethodPut, "/v1/agents/"+agentID+"/manifest/"+mid+"/endpoint-secret", tok,
		map[string]any{"token": "super-secret-bearer"}, nil); code != http.StatusOK {
		stub.Close()
		t.Fatalf("certify: set endpoint secret: got %d", code)
	}

	var report struct {
		Verified bool `json:"verified"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+agentID+"/manifest/"+mid+"/verify", tok, nil, &report); code != http.StatusOK {
		stub.Close()
		t.Fatalf("certify: verify: got %d", code)
	}
	if !report.Verified {
		stub.Close()
		t.Fatalf("certify: endpoint not verified: %+v", report)
	}
	return stub
}
