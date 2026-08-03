//go:build integration

// Proves the P1.2 certification gate: an agent may not enter the ranked queue
// until it has an active, endpoint-verified manifest. Practice/sandbox is not
// gated (a developer can always test before certifying).
package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestCertificationGate_RankedQueue(t *testing.T) {
	c := newClient(t)
	agent := stubAgent(t, []string{"goofspiel"})
	defer agent.Close()

	// Sign up → agent API key (agent scope) + dashboard token (owner scope).
	uniq := time.Now().UnixNano()
	var su struct {
		DashboardToken string `json:"dashboard_token"`
		APIKey         string `json:"api_key"`
		AgentID        string `json:"agent_id"`
	}
	if code := c.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"email":      fmt.Sprintf("gate+%d@example.com", uniq),
		"password":   "hunter2-strong-pass",
		"agent_name": "GateAgent",
	}, &su); code != http.StatusCreated {
		t.Fatalf("signup: %d", code)
	}

	// 1. Uncertified agent → ranked queue is CLOSED (403 agent_not_certified).
	var errBody struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	// goofspiel is a tiered game (migration 0038 seeds Low/Mid/High), so the queue
	// takes a `tier`, not a free-form bid. A valid tier is sent so the ONLY reason
	// for rejection is non-certification (the cert gate runs before affordability).
	if code := c.do(http.MethodPost, "/v1/queue", su.APIKey, map[string]any{"tier": "low"}, &errBody); code != http.StatusForbidden {
		t.Fatalf("uncertified enqueue: expected 403, got %d", code)
	}
	if errBody.Error.Code != "agent_not_certified" {
		t.Fatalf("expected agent_not_certified, got %q", errBody.Error.Code)
	}

	// 2. Certify the agent (manifest → secret → verify).
	mdoc := map[string]any{
		"manifestVersion": "1.0",
		"agent":           map[string]any{"name": "GateAgent", "version": "1.0.0", "visibility": "public"},
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

	// 3. Certified agent → ranked queue is now OPEN (202 accepted).
	//
	// Fund it FIRST, because enqueue runs an affordability preflight. The amount is
	// deliberately far above any tier rather than matched to one: this test is about the
	// CERTIFICATION gate, and affordability must never be able to be the reason it fails.
	//
	// It used to mint exactly 500 with the comment "Low = 100", and both halves were wrong
	// by the time it ran. Migration 0038 seeds goofspiel Low at 100 coins, but the live $5
	// minimum-stake floor (gamestakes.DefaultMinStakeUSDCents, 500 cents at a 1¢ peg) lifts
	// it to 500 — so the stake was 500, the agent's default reserve is 50, and 500 < 550
	// refused the join. A test that pins its funding to a policy number it does not control
	// breaks the moment an operator moves that policy; this one cannot.
	//
	// Platform token, not the developer's own — see platformToken(). Sent with the PLATFORM
	// scheme via doPlatform: as a Bearer it is parsed as a user JWT and rejected 401, which
	// is what was failing this test and the whole E2E job.
	const fundAmount = 100_000 // >> the highest seeded tier (2,000) + any reserve
	if code := c.doPlatform(http.MethodPost, "/v1/admin/mint", map[string]any{"agent": su.AgentID, "amount": fundAmount}, nil); code != http.StatusOK {
		t.Fatalf("mint stake: got %d, want 200", code)
	}
	// The failure body is decoded and reported, not just the status. A bare "expected 202,
	// got 402" says nothing about WHICH preflight refused — and the enqueue path runs four
	// of them (certification, affordability, owner limits, reachability), each with its own
	// code and its own fix.
	var enqErr struct {
		Error struct {
			Code    string         `json:"code"`
			Message string         `json:"message"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if code := c.do(http.MethodPost, "/v1/queue", su.APIKey, map[string]any{"tier": "low"}, &enqErr); code != http.StatusAccepted {
		t.Fatalf("certified enqueue: expected 202, got %d — code=%q message=%q details=%v",
			code, enqErr.Error.Code, enqErr.Error.Message, enqErr.Error.Details)
	}
	_ = c.do(http.MethodDelete, "/v1/queue", su.APIKey, nil, nil) // cleanup
	t.Logf("gate OK: uncertified blocked, certified admitted (agent=%s)", su.AgentID)
}
