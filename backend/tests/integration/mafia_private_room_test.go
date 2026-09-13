//go:build integration

package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

// TestMafiaPrivateRoomWired proves Play-a-friend accepts game=mafia on the live
// e2e stack (and still refuses unknown games), without requiring a host-reachable
// certify stub from inside Docker.
//
// Full create→join→start (12 humans, no house bots) is covered by internal/mafia
// privateroom unit tests. When the harness can sit a playable agent
// (local `pyyol play` against this BASE_URL, or certify with a container-reachable
// stub), the create path returns 201 with an mf_* id.
func TestMafiaPrivateRoomWired(t *testing.T) {
	c := newClient(t)
	if code := c.do(http.MethodGet, "/healthz", "", nil, nil); code != 200 {
		t.Fatalf("/healthz = %d — is the e2e stack up?", code)
	}

	hostTok, hostAgent := signupDash(t, c, "mafia-room-host")
	fundAgent(t, c, hostAgent, 5000)

	var bad struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if code := c.do(http.MethodPost, "/v1/room/create", hostTok, map[string]any{
		"bid": 100, "game": "monopoly",
	}, &bad); code != http.StatusBadRequest {
		t.Fatalf("monopoly room create = %d, want 400", code)
	}
	if bad.Error.Code != "room_game_unsupported" {
		t.Fatalf("monopoly code = %q, want room_game_unsupported", bad.Error.Code)
	}

	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
		RoomID  string `json:"room_id"`
		MatchID string `json:"match_id"`
		Game    string `json:"game"`
	}
	code := c.do(http.MethodPost, "/v1/room/create", hostTok, map[string]any{
		"tier": "low", "game": "mafia",
	}, &body)
	switch code {
	case http.StatusCreated:
		id := body.RoomID
		if id == "" {
			id = body.MatchID
		}
		if body.Game != "mafia" {
			t.Fatalf("game = %q, want mafia", body.Game)
		}
		if len(id) < 3 || id[:3] != "mf_" {
			t.Fatalf("room id = %q, want mf_…", id)
		}
		t.Logf("mafia room created: %s (playable agent in this env)", id)
	case http.StatusConflict, http.StatusUnprocessableEntity:
		// Not playable yet — expected when the agent has no local socket and no
		// hosted verify the container can reach. Still proves game=mafia is wired
		// (the old binary answered 400 room_game_unsupported).
		if body.Error.Code != "agent_not_playable" && body.Error.Code != "agent_not_certified" &&
			body.Error.Code != "verification_pending" {
			t.Fatalf("mafia create = %d code=%q, want agent_not_playable (path wired)", code, body.Error.Code)
		}
		t.Logf("mafia room path wired; sit refused with %s (start pyyol play to create)", body.Error.Code)
	case http.StatusServiceUnavailable:
		if body.Error.Code != "room_game_unavailable" {
			t.Fatalf("unexpected 503 code=%q", body.Error.Code)
		}
		t.Logf("mafia rooms not configured: %s", body.Error.Code)
	case http.StatusBadRequest:
		t.Fatalf("mafia still refused as unsupported (%q) — server image is stale", body.Error.Code)
	default:
		t.Fatalf("mafia room create = %d code=%q", code, body.Error.Code)
	}
}

func signupDash(t *testing.T, c *client, name string) (dashTok, agentID string) {
	t.Helper()
	email := fmt.Sprintf("%s-%d@example.com", name, time.Now().UnixNano())
	var su struct {
		DashboardToken string `json:"dashboard_token"`
		AgentID        string `json:"agent_id"`
	}
	if code := c.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"email": email, "password": "hunter2-strong-pass", "agent_name": name,
	}, &su); code != http.StatusCreated {
		t.Fatalf("signup %s: %d", name, code)
	}
	if su.DashboardToken == "" || su.AgentID == "" {
		t.Fatalf("signup %s returned empty token/agent", name)
	}
	return su.DashboardToken, su.AgentID
}

func fundAgent(t *testing.T, c *client, agentID string, amount int64) {
	t.Helper()
	if code := c.doPlatform(http.MethodPost, "/v1/admin/mint", map[string]any{
		"agent": agentID, "amount": amount,
	}, nil); code != http.StatusOK {
		t.Fatalf("mint %s: %d", agentID, code)
	}
}
