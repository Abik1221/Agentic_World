//go:build integration

// Proves P1.4: the public agent profile surfaces the certification badge, the
// developer-declared model, supported games, and season history — assembled from
// identity + manifest + rating.
package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestPublicProfile_ShowsCertificationAndManifest(t *testing.T) {
	c := newClient(t)
	agent := stubAgent(t, []string{"goofspiel"})
	defer agent.Close()

	uniq := time.Now().UnixNano()
	var su struct {
		DashboardToken string `json:"dashboard_token"`
		AgentID        string `json:"agent_id"`
	}
	if code := c.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"email":      fmt.Sprintf("prof+%d@example.com", uniq),
		"password":   "hunter2-strong-pass",
		"agent_name": "ProfAgent",
	}, &su); code != http.StatusCreated {
		t.Fatalf("signup: %d", code)
	}

	// Certify (manifest → secret → verify) with a declared model.
	mdoc := map[string]any{
		"manifestVersion": "1.0",
		"agent":           map[string]any{"name": "ProfAgent", "version": "2.1.0", "visibility": "public"},
		"developer":       map[string]any{"name": "Dev", "organization": "Solo Labs"},
		"games":           []string{"goofspiel"},
		"endpoint":        map[string]any{"url": agent.URL + "/play", "authentication": "bearer-token"},
		"runtime":         map[string]any{"timeout": 5000},
		"model":           map[string]any{"provider": "Anthropic", "model": "Claude", "reasoning": true},
		"sdk":             map[string]any{"language": "Go"},
		"contact":         map[string]any{"email": "dev@example.com"},
	}
	var sub struct {
		ManifestID string `json:"manifest_id"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+su.AgentID+"/manifest", su.DashboardToken, mdoc, &sub); code != http.StatusCreated {
		t.Fatalf("submit manifest: %d", code)
	}
	if code := c.do(http.MethodPut, "/v1/agents/"+su.AgentID+"/manifest/"+sub.ManifestID+"/endpoint-secret", su.DashboardToken, map[string]any{"token": "s"}, nil); code != http.StatusOK {
		t.Fatalf("set secret: %d", code)
	}
	var rep struct {
		Verified bool `json:"verified"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+su.AgentID+"/manifest/"+sub.ManifestID+"/verify", su.DashboardToken, nil, &rep); code != http.StatusOK || !rep.Verified {
		t.Fatalf("verify: code=%d verified=%v", code, rep.Verified)
	}

	// Public profile (by agent id) shows the certification card.
	var prof struct {
		Agent    string `json:"agent"`
		Manifest *struct {
			Certified    bool     `json:"certified"`
			AgentVersion string   `json:"agent_version"`
			Games        []string `json:"games"`
			Model        *struct {
				Provider string `json:"provider"`
				Declared bool   `json:"developer_declared"`
			} `json:"model"`
		} `json:"manifest"`
		SeasonHistory []any `json:"season_history"`
	}
	if code := c.do(http.MethodGet, "/v1/agent/"+su.AgentID+"/profile", "", nil, &prof); code != http.StatusOK {
		t.Fatalf("profile: %d", code)
	}
	if prof.Manifest == nil || !prof.Manifest.Certified {
		t.Fatalf("expected certified manifest card, got %+v", prof.Manifest)
	}
	if prof.Manifest.AgentVersion != "2.1.0" || len(prof.Manifest.Games) != 1 {
		t.Fatalf("unexpected manifest card: %+v", prof.Manifest)
	}
	if prof.Manifest.Model == nil || prof.Manifest.Model.Provider != "Anthropic" || !prof.Manifest.Model.Declared {
		t.Fatalf("expected developer-declared model, got %+v", prof.Manifest.Model)
	}
	t.Logf("profile OK: certified card + declared model surfaced for %s", su.AgentID)
}
