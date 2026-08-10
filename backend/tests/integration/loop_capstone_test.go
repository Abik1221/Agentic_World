//go:build integration

// Capstone: a solo developer, with nobody else online, plays a full match against
// a platform house bot (the sandbox — free, unranked, always available: P1.7's
// beta answer) and then fetches the match's PUBLIC, shareable replay (P1.5). This
// exercises the whole play→finish→replay loop on a real finished match.
package integration

import (
	"fmt"
	"net/http"
	"testing"
	"time"
)

func TestFullLoop_SandboxPlayToPublicReplay(t *testing.T) {
	c := newClient(t)

	// Solo dev signs up.
	uniq := time.Now().UnixNano()
	var su struct {
		APIKey         string `json:"api_key"`
		AgentID        string `json:"agent_id"`
		DashboardToken string `json:"dashboard_token"`
	}
	if code := c.do(http.MethodPost, "/v1/auth/signup", "", map[string]any{
		"email":      fmt.Sprintf("play+%d@example.com", uniq),
		"password":   "hunter2-strong-pass",
		"agent_name": "PlayAgent",
	}, &su); code != http.StatusCreated {
		t.Fatalf("signup: %d", code)
	}
	key := su.APIKey

	// Certify before playing. This test used to assert "no certification needed to
	// practice" and start a sandbox match straight after signup — which stopped being
	// true when CreateSandbox began calling CheckEligible like every other table.
	//
	// That was the right change, not a regression to work around: a sandbox match writes
	// decision and benchmark rows that feed the P-Index, the model board and the deception
	// index, so an uncertified agent farming free tables would build a public record it did
	// not earn. The sandbox is free of STAKES, not of identity.
	stub := certifyAgent(t, c, su.DashboardToken, su.AgentID, "PlayAgent", []string{"goofspiel"})
	defer stub.Close()

	// Start a sandbox match vs a house bot — the solo dev's guaranteed opponent.
	var start struct {
		MatchID string `json:"match_id"`
		Mode    string `json:"mode"`
	}
	if code := c.do(http.MethodPost, "/v1/sandbox/match", key, map[string]any{"difficulty": "medium"}, &start); code != http.StatusCreated {
		t.Fatalf("sandbox start: %d", code)
	}
	if start.MatchID == "" {
		t.Fatalf("no match id: %+v", start)
	}

	// Play our seat to completion; the house bot auto-plays its seat.
	statePath := "/v1/match/" + start.MatchID + "/state"
	actionPath := "/v1/match/" + start.MatchID + "/action"
	finished := false
	for i := 0; i < 60 && !finished; i++ {
		var st struct {
			Status   string `json:"status"`
			Round    int    `json:"round"`
			YourTurn bool   `json:"your_turn"`
			You      struct {
				Hand []int `json:"hand"`
			} `json:"you"`
			LegalActions struct {
				PlayCardFrom []int `json:"play_card_from"`
			} `json:"legal_actions"`
		}
		if code := c.do(http.MethodGet, statePath, key, nil, &st); code != http.StatusOK {
			t.Fatalf("state: %d", code)
		}
		if st.Status == "finished" {
			finished = true
			break
		}
		if st.YourTurn {
			card := 0
			if len(st.LegalActions.PlayCardFrom) > 0 {
				card = st.LegalActions.PlayCardFrom[0]
			} else if len(st.You.Hand) > 0 {
				card = st.You.Hand[0]
			}
			// Best-effort: an occasional rejected action (race with resolve) is fine;
			// we re-read state next loop.
			_ = c.do(http.MethodPost, actionPath, key, map[string]any{"round": st.Round, "card": card}, nil)
		} else {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !finished {
		t.Fatal("sandbox match did not finish within the loop budget")
	}

	// Public, shareable replay (no auth) — the source of truth for the finished match.
	var replay struct {
		MatchID    string `json:"match_id"`
		Status     string `json:"status"`
		ReplayHash string `json:"replay_hash"`
		Events     []any  `json:"events"`
	}
	if code := c.do(http.MethodGet, "/v1/match/"+start.MatchID+"/replay", "", nil, &replay); code != http.StatusOK {
		t.Fatalf("public replay: %d", code)
	}
	if replay.Status != "finished" || replay.ReplayHash == "" || len(replay.Events) == 0 {
		t.Fatalf("unexpected replay: status=%s hash=%q events=%d", replay.Status, replay.ReplayHash, len(replay.Events))
	}
	t.Logf("loop OK: solo dev played house bot to finish; public replay %s has %d events, hash %s",
		start.MatchID, len(replay.Events), replay.ReplayHash[:12])
}
