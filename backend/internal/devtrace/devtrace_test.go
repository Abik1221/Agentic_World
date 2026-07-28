package devtrace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type fakeRepo struct{ owned []string }

func (f fakeRepo) OwnedAgentIDs(context.Context, string) ([]string, error) { return f.owned, nil }

// lensStub stands in for the Lens read api and records what it was asked for.
type lensStub struct {
	lastQuery url.Values
	events    []map[string]any
}

func (l *lensStub) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.lastQuery = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]any{"events": l.events})
	}))
}

// The remote is asked only for agents the caller owns, and only for allowlisted
// event types — never with an empty or absent filter.
func TestQueryIsScopedToOwnedAgentsAndAllowlist(t *testing.T) {
	stub := &lensStub{}
	srv := stub.server(t)
	defer srv.Close()

	svc := New(fakeRepo{owned: []string{"ag_mine", "ag_also_mine"}}, srv.URL, "k", "org")
	if _, err := svc.Activity(context.Background(), "usr_1", "", time.Now().Add(-time.Hour), 50); err != nil {
		t.Fatal(err)
	}

	actors := stub.lastQuery.Get("actor_ids")
	if actors != "ag_mine,ag_also_mine" {
		t.Fatalf("actor filter = %q, want only the caller's agents", actors)
	}
	types := stub.lastQuery.Get("event_types")
	if types == "" {
		t.Fatal("event_types filter absent — the query would not be allowlist-scoped")
	}
	for _, forbidden := range []string{"model_call_completed", "log_record", "payout"} {
		if strings.Contains(types, forbidden) {
			t.Fatalf("requested a non-dev-visible type %q", forbidden)
		}
	}
}

// Asking for someone else's agent is a 404, and no query is issued at all.
func TestForeignAgentIsRejectedBeforeQuerying(t *testing.T) {
	stub := &lensStub{}
	srv := stub.server(t)
	defer srv.Close()

	svc := New(fakeRepo{owned: []string{"ag_mine"}}, srv.URL, "k", "org")
	_, err := svc.Activity(context.Background(), "usr_1", "ag_someone_else", time.Now().Add(-time.Hour), 50)
	if err == nil {
		t.Fatal("reading another developer's agent must fail")
	}
	if stub.lastQuery != nil {
		t.Fatal("a query was issued for an agent the caller does not own")
	}
}

// The second gate. Even if the remote misbehaves and returns rows the caller does not
// own, or event types outside the allowlist, none of it reaches the developer. This is
// the property that makes a single bug insufficient to cause a leak.
func TestRemoteRowsAreRefilteredOnTheWayOut(t *testing.T) {
	stub := &lensStub{events: []map[string]any{
		{"agent_id": "ag_mine", "event_type": "agent_decision", "status": "ok"},
		{"agent_id": "ag_rival", "event_type": "agent_decision", "status": "ok"},    // not owned
		{"agent_id": "ag_mine", "event_type": "model_call_completed", "status": ""}, // not visible
		{"agent_id": "ag_mine", "event_type": "stake_escrowed", "status": "ok"},     // not visible
	}}
	srv := stub.server(t)
	defer srv.Close()

	svc := New(fakeRepo{owned: []string{"ag_mine"}}, srv.URL, "k", "org")
	got, err := svc.Activity(context.Background(), "usr_1", "", time.Now().Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1 — a non-owned or non-visible row survived: %+v", len(got), got)
	}
	if got[0].AgentID != "ag_mine" || got[0].Type != "agent_decision" {
		t.Fatalf("unexpected surviving entry: %+v", got[0])
	}
}

// A user with no agents gets an empty result, and crucially no request at all — an
// empty filter list is the shape most likely to be read as "no filter" downstream.
func TestNoAgentsIssuesNoQuery(t *testing.T) {
	stub := &lensStub{}
	srv := stub.server(t)
	defer srv.Close()

	svc := New(fakeRepo{owned: nil}, srv.URL, "k", "org")
	got, err := svc.Activity(context.Background(), "usr_new", "", time.Now().Add(-time.Hour), 50)
	if err != nil || len(got) != 0 {
		t.Fatalf("got %v, %v; want empty, nil", got, err)
	}
	if stub.lastQuery != nil {
		t.Fatal("queried the trace store with an empty actor filter")
	}
}

// An unconfigured Lens degrades to an error on this route only; it must not be
// mistaken for "no activity", which would read as a bug in the developer's agent.
func TestUnconfiguredLensReportsUnavailable(t *testing.T) {
	svc := New(fakeRepo{owned: []string{"ag_mine"}}, "", "", "")
	if svc.Enabled() {
		t.Fatal("service claims enabled with no endpoint")
	}
	if _, err := svc.Activity(context.Background(), "usr_1", "", time.Now(), 10); err == nil {
		t.Fatal("want an explicit unavailable error, not a silent empty list")
	}
}
