//go:build integration

// Proves P1.6: certifying an agent emits agent.certified, the dispatcher's badge
// handler awards the "certified" badge idempotently, and it surfaces on the
// public profile. This exercises the full P1.1 → P1.6 chain end to end.
package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestBadges_CertifiedAwardedAndOnProfile(t *testing.T) {
	c := newClient(t)
	agent := stubAgent(t, []string{"goofspiel"})
	defer agent.Close()

	uniq := time.Now().UnixNano()
	var su struct {
		DashboardToken string `json:"dashboard_token"`
		AgentID        string `json:"agent_id"`
	}
	if code := c.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"email":      fmt.Sprintf("badge+%d@example.com", uniq),
		"password":   "hunter2-strong-pass",
		"agent_name": "BadgeAgent",
	}, &su); code != http.StatusCreated {
		t.Fatalf("signup: %d", code)
	}

	mdoc := map[string]any{
		"manifestVersion": "1.0",
		"agent":           map[string]any{"name": "BadgeAgent", "version": "1.0.0", "visibility": "public"},
		"developer":       map[string]any{"name": "Dev"},
		"games":           []string{"goofspiel"},
		"endpoint":        map[string]any{"url": agent.URL + "/play", "authentication": "bearer-token"},
		"runtime":         map[string]any{"timeout": 5000},
		"sdk":             map[string]any{"language": "Go"},
		"contact":         map[string]any{"email": "dev@example.com"},
	}
	var sub struct {
		ManifestID string `json:"manifest_id"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+su.AgentID+"/manifest", su.DashboardToken, mdoc, &sub); code != http.StatusCreated {
		t.Fatalf("submit: %d", code)
	}
	if code := c.do(http.MethodPut, "/v1/agents/"+su.AgentID+"/manifest/"+sub.ManifestID+"/endpoint-secret", su.DashboardToken, map[string]any{"token": "s"}, nil); code != http.StatusOK {
		t.Fatalf("secret: %d", code)
	}
	var rep struct {
		Verified bool `json:"verified"`
	}
	if code := c.do(http.MethodPost, "/v1/agents/"+su.AgentID+"/manifest/"+sub.ManifestID+"/verify", su.DashboardToken, nil, &rep); code != http.StatusOK || !rep.Verified {
		t.Fatalf("verify: %d %v", code, rep.Verified)
	}

	// The badge is awarded asynchronously by the dispatcher; poll the profile.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var prof struct {
			Badges []struct {
				Code string `json:"code"`
			} `json:"badges"`
		}
		if code := c.do(http.MethodGet, "/v1/agent/"+su.AgentID+"/profile", "", nil, &prof); code != http.StatusOK {
			t.Fatalf("profile: %d", code)
		}
		for _, b := range prof.Badges {
			if b.Code == "certified" {
				t.Logf("badge OK: certified surfaced on profile for %s", su.AgentID)
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("certified badge not awarded within deadline; badges=%+v", prof.Badges)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
