package remoteplay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
)

// playServer stands in for a developer's hosted agent. decide maps a view to a
// card; the returned handler speaks the push protocol.
func playServer(t *testing.T, decide func(GoofspielView) int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v GoofspielView
		if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(GoofspielMove{Round: v.Round, Card: decide(v)})
	}))
}

func remoteSeat(url string) RemoteDecider {
	return RemoteDecider{
		Client:  agentclient.New(agentclient.Config{AllowPrivate: true, Retries: 0}),
		Target:  agentclient.Target{EndpointURL: url},
		MatchID: "m_test",
	}
}

func TestPlayGoofspiel_RemoteVsLocal(t *testing.T) {
	srv := playServer(t, func(v GoofspielView) int { return pickNearest(v.PrizePool, v.LegalActions) })
	defer srv.Close()

	res, err := PlayGoofspiel(context.Background(), remoteSeat(srv.URL), NearestPool{}, []byte("seed-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Finished {
		t.Fatal("match did not finish")
	}
	if res.Rounds != 13 || res.Moves != 26 {
		t.Fatalf("expected 13 rounds / 26 moves, got %d / %d", res.Rounds, res.Moves)
	}
	if res.FallbackMoves != 0 {
		t.Fatalf("expected no fallbacks with a well-behaved endpoint, got %d", res.FallbackMoves)
	}
	if res.ReplayHash == "" {
		t.Fatal("no replay hash")
	}
}

func TestPlayGoofspiel_Deterministic(t *testing.T) {
	srv := playServer(t, func(v GoofspielView) int { return pickNearest(v.PrizePool, v.LegalActions) })
	defer srv.Close()

	a, err := PlayGoofspiel(context.Background(), remoteSeat(srv.URL), NearestPool{}, []byte("seed-xyz"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := PlayGoofspiel(context.Background(), remoteSeat(srv.URL), NearestPool{}, []byte("seed-xyz"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ReplayHash != b.ReplayHash {
		t.Fatalf("same seed+deciders must reproduce the same replay hash: %s != %s", a.ReplayHash, b.ReplayHash)
	}
}

func TestPlayGoofspiel_IllegalMoveFallsBack(t *testing.T) {
	// A hostile endpoint that always returns an illegal card (999). The match must
	// still complete via the deterministic fallback.
	srv := playServer(t, func(GoofspielView) int { return 999 })
	defer srv.Close()

	res, err := PlayGoofspiel(context.Background(), remoteSeat(srv.URL), NearestPool{}, []byte("seed-2"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Finished {
		t.Fatal("match must finish even when the endpoint misbehaves")
	}
	if res.FallbackMoves != 13 {
		t.Fatalf("expected 13 fallback moves (one per round for the remote seat), got %d", res.FallbackMoves)
	}
}

func TestPlayGoofspiel_EndpointDownFallsBack(t *testing.T) {
	// Endpoint returns 500 every time -> Play errors -> deterministic fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	res, err := PlayGoofspiel(context.Background(), remoteSeat(srv.URL), NearestPool{}, []byte("seed-3"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Finished || res.FallbackMoves != 13 {
		t.Fatalf("expected completed match with 13 fallbacks, got finished=%v fallbacks=%d", res.Finished, res.FallbackMoves)
	}
}

func TestNearestPoolAndLowest(t *testing.T) {
	if got := pickNearest(6, []int{1, 5, 8}); got != 5 {
		t.Fatalf("nearest to 6 among {1,5,8} = %d, want 5", got)
	}
	if got := lowest([]int{7, 3, 9}); got != 3 {
		t.Fatalf("lowest = %d, want 3", got)
	}
}
