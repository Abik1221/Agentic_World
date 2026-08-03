//go:build livetrace

// A REAL round trip: this arena package, over HTTP, against a running Pyyol Lens
// query-api backed by a real ClickHouse. No stub server.
//
// It exists because every layer of this path unit-tests green against a fake and the
// whole thing was still broken end to end — the trace store rejected the arena's event
// types at ingest, and the arena read the query plane with the ingest key. Neither is
// visible to a test that stands in for the remote.
//
// Run (with the local stack up):
//
//	LENS_QUERY=http://127.0.0.1:18082 LENS_QUERY_KEY=e2e-query-key LENS_ORG=pyyol \
//	  go test -tags=livetrace -count=1 -run TestLive ./internal/devtrace/
package devtrace

import (
	"context"
	"os"
	"testing"
	"time"
)

type liveRepo struct{ owned []string }

func (r liveRepo) OwnedAgentIDs(context.Context, string) ([]string, error) { return r.owned, nil }

func liveSvc(t *testing.T, owned ...string) *Service {
	t.Helper()
	ep := os.Getenv("LENS_QUERY")
	if ep == "" {
		t.Skip("LENS_QUERY unset — needs a running Lens query-api")
	}
	return New(liveRepo{owned: owned}, ep, os.Getenv("LENS_QUERY_KEY"), os.Getenv("LENS_ORG"), nil)
}

// The events the harness ingested are readable through this package, with ownership
// and visibility both enforced, and the fields a developer debugs against intact.
func TestLiveActivityRoundTrip(t *testing.T) {
	svc := liveSvc(t, "agt_e2e_owned")
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	got, err := svc.Activity(context.Background(), "usr_live", "", since, 300)
	if err != nil {
		t.Fatalf("live read failed: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no entries — the trace store accepted the arena's events but this read found none")
	}

	var decisions, sawRationale, sawTimeout, sawLifecycle int
	for _, e := range got {
		if e.AgentID != "agt_e2e_owned" {
			t.Fatalf("read back an agent the caller does not own: %q", e.AgentID)
		}
		switch e.Type {
		case "agent_decision":
			decisions++
			if e.Detail["rationale"] == "held the high card" {
				sawRationale++
			}
			if e.Status == "error" && e.LatencyMS == 8000 {
				sawTimeout++
			}
		case "agent_connected", "agent_endpoint_failed":
			sawLifecycle++
		case "model_call_completed", "log_record":
			t.Fatalf("operator-only event type reached the developer view: %q", e.Type)
		}
	}
	if decisions < 2 {
		t.Fatalf("decisions = %d, want at least 2", decisions)
	}
	if sawRationale == 0 {
		t.Fatal("the agent's own rationale did not survive the round trip — the payload is the point of this view")
	}
	if sawTimeout == 0 {
		t.Fatal("a timed-out decision did not come back as an error with its latency — that is the row a developer is here for")
	}
	if sawLifecycle < 2 {
		t.Fatalf("lifecycle events = %d, want the connect and the endpoint failure", sawLifecycle)
	}
	t.Logf("live: %d entries, %d decisions, %d lifecycle", len(got), decisions, sawLifecycle)
}

// Narrowing to one owned agent is a filter that reaches the remote, and an unowned id
// is refused before any query is issued.
func TestLiveOwnershipGate(t *testing.T) {
	svc := liveSvc(t, "agt_e2e_owned")
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	if _, err := svc.Activity(context.Background(), "usr_live", "agt_e2e_owned", since, 300); err != nil {
		t.Fatalf("reading my own agent by id failed: %v", err)
	}
	// agt_someone_else HAS rows in the store; ownership is what keeps them out.
	if _, err := svc.Activity(context.Background(), "usr_live", "agt_someone_else", since, 300); err == nil {
		t.Fatal("read another developer's agent — the ownership gate did not hold against a live store")
	}
}

// The read key is the QUERY key. Handed the wrong one, a live Lens answers 401 and the
// arena must call that misconfigured, not unreachable: the store is plainly up.
func TestLiveWrongKeyIsMisconfiguredNotUnreachable(t *testing.T) {
	ep := os.Getenv("LENS_QUERY")
	if ep == "" {
		t.Skip("LENS_QUERY unset")
	}
	svc := New(liveRepo{owned: []string{"agt_e2e_owned"}}, ep, "the-ingest-key-not-the-query-key", os.Getenv("LENS_ORG"), nil)
	_, err := svc.Activity(context.Background(), "usr_live", "", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC), 10)
	if err == nil {
		t.Fatal("a live store accepted a wrong key")
	}
	if got := code(err); got != "traces_misconfigured" {
		t.Fatalf("code = %q, want traces_misconfigured (%v)", got, err)
	}
}
