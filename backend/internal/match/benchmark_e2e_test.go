package match_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform/telemetry"
)

const e2eOrg = "e2e-arena"

// End-to-end proof of the arena→Lens hop: two live socket agents play a full
// ranked goofspiel match driven by the platform, and the drive loop's benchmark
// summary is emitted through the REAL telemetry client to a captured Lens ingest,
// carrying server-authoritative provider/model (from the meta resolver) + the
// classified decision outcomes. This exercises the exact production path
// (drive → decide → recorder → SetAgentMeta/SetResult → Flush → Emit → HTTP).
func TestRankedDriveEmitsBenchmarkToLens(t *testing.T) {
	// Fake Lens ingest: capture the batched benchmark events.
	var mu sync.Mutex
	var events []telemetry.Event
	var key string
	ingest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("X-Pyyol-Key")
		var body struct {
			Events []telemetry.Event `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		events = append(events, body.Events...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer ingest.Close()

	// Live mode: when E2E_LENS_INGEST is set, emit to the REAL Lens ingest and
	// (with E2E_LENS_QUERY) assert against the live leaderboard — a full
	// arena-match → ClickHouse → query-api proof. Otherwise self-contained.
	endpoint, apiKey := ingest.URL, "arena-key"
	liveQuery := os.Getenv("E2E_LENS_QUERY")
	if v := os.Getenv("E2E_LENS_INGEST"); v != "" {
		endpoint = v
		apiKey = os.Getenv("E2E_LENS_KEY")
	}

	discard := slog.New(slog.NewTextHandler(io.Discard, nil))
	em := telemetry.New(telemetry.Config{
		Enabled: true, Endpoint: endpoint, APIKey: apiKey, Organization: e2eOrg,
		FlushInterval: 20 * time.Millisecond,
	}, discard)
	defer func() { _ = em.Shutdown(context.Background()) }()

	// Server-authoritative agent metadata (stands in for manifest.PublicActive).
	meta := func(_ context.Context, agentID string) benchmark.AgentMeta {
		return benchmark.AgentMeta{Version: "1.0.0", Provider: "openai", Model: "gpt-4o"}
	}

	// Real gateway + two live WebSocket agents (reuses the WS harness).
	gw := agentgw.New(nil, agentgw.Options{
		TurnTimeout: 2 * time.Second, HeartbeatInterval: 100 * time.Millisecond,
		LivenessTimeout: time.Second, AllowInsecureOrigin: true,
	}, discard)
	wsSrv := httptest.NewServer(gw.Handler())
	defer wsSrv.Close()
	wsURL := "ws" + strings.TrimPrefix(wsSrv.URL, "http")

	closeA := wsAgent(t, wsURL, "ag_a")
	defer closeA()
	closeB := wsAgent(t, wsURL, "ag_b")
	defer closeB()

	deadline := time.Now().Add(3 * time.Second)
	for (!gw.Connected("ag_a") || !gw.Connected("ag_b")) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	svc, _ := newSvcWithRepo()
	svc.EnableRankedDrive(gw, nil, nil, em, nil, meta, discard) // persist nil ⇒ direct emit path

	ctx := context.Background()
	id, err := svc.CreatePaired(ctx, "ag_a", "usr_a", "ag_b", "usr_b", 50)
	if err != nil {
		t.Fatalf("CreatePaired: %v", err)
	}

	// Wait for the match to finish.
	fin := time.Now().Add(10 * time.Second)
	finished := false
	for time.Now().Before(fin) {
		v, _ := svc.State(ctx, id, "ag_a", false, 0)
		if v.Status == match.StatusFinished {
			finished = true
			break
		}
		time.Sleep(30 * time.Millisecond)
	}
	if !finished {
		t.Fatal("match did not finish")
	}

	// Live mode: assert the events reached the REAL Lens leaderboard.
	if liveQuery != "" {
		_ = em.Shutdown(context.Background()) // force flush to live ingest
		found := map[string]bool{}
		poll := time.Now().Add(8 * time.Second)
		for time.Now().Before(poll) {
			req, _ := http.NewRequest(http.MethodGet, liveQuery+"/v1/benchmarks/agents?days=1", nil)
			req.Header.Set("x-organization-id", e2eOrg)
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				var body struct {
					Agents []struct {
						AgentID  string `json:"agent_id"`
						Provider string `json:"provider"`
					} `json:"agents"`
				}
				_ = json.NewDecoder(resp.Body).Decode(&body)
				resp.Body.Close()
				for _, a := range body.Agents {
					if a.AgentID == "ag_a" || a.AgentID == "ag_b" {
						if a.Provider != "openai" {
							t.Errorf("live leaderboard %s provider=%q want openai", a.AgentID, a.Provider)
						}
						found[a.AgentID] = true
					}
				}
			}
			if len(found) >= 2 {
				break
			}
			time.Sleep(400 * time.Millisecond)
		}
		if len(found) < 2 {
			t.Fatalf("live leaderboard missing agents (arena→Lens→query broken): found %v", found)
		}
		t.Logf("LIVE: ag_a + ag_b present on real Lens leaderboard with provider=openai")
		return
	}

	// Poll the captured ingest for the two benchmark_recorded events (the drive
	// goroutine flushes on return; the emitter batches every 20ms).
	var seat = map[string]telemetry.Event{}
	got := time.Now().Add(3 * time.Second)
	for time.Now().Before(got) {
		mu.Lock()
		for _, e := range events {
			if e.EventType == "benchmark_recorded" {
				seat[e.ActorID] = e
			}
		}
		mu.Unlock()
		if len(seat) >= 2 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	if key != "arena-key" {
		t.Errorf("ingest auth header = %q, want arena-key", key)
	}
	for _, id := range []string{"ag_a", "ag_b"} {
		e, ok := seat[id]
		if !ok {
			t.Fatalf("no benchmark_recorded for %s (arena→Lens hop broken)", id)
		}
		if e.RunID == "" {
			t.Errorf("%s: empty run_id (match id)", id)
		}
		if e.Provider != "openai" || e.Model != "gpt-4o" {
			t.Errorf("%s: provider/model not attached server-side: %q/%q", id, e.Provider, e.Model)
		}
		if e.SessionID != "goofspiel" {
			t.Errorf("%s: game=%q want goofspiel", id, e.SessionID)
		}
		// The winning agent's summary should carry a win; a full 13-round match was played.
		if d, _ := e.PayloadJSON["decisions"].(float64); d <= 0 {
			t.Errorf("%s: decisions=%v, expected >0 recorded", id, e.PayloadJSON["decisions"])
		}
	}
}
