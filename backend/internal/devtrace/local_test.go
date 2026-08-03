package devtrace

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// THE ARENA'S OWN LOG IS THE SOURCE OF TRUTH FOR /traces.
//
// The bug these tests exist for was reported three times: a developer opens "Agent traces"
// and reads "Trace store unreachable" (or "traces are not enabled here") on a platform that
// had recorded every one of their moves. /traces was a window onto a separate telemetry
// service, so it had that service's availability and nothing else.
//
// Postgres is now primary and the Lens is enrichment. Every way the Lens can fail is
// exercised below, and in each case the page must still be filled from the match log.

var traceEpoch = time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

// localStub is a match log: a handful of raw match_events rows already attributed to a seat,
// exactly as store.DevTraceRepo returns them.
type localStub struct {
	rows []MatchRow
	regs []Registration
	// calls records that the local source was actually consulted.
	calls int
	err   error
}

func (l *localStub) MatchActivity(context.Context, []string, time.Time, int) ([]MatchRow, error) {
	l.calls++
	return l.rows, l.err
}

func (l *localStub) AgentRegistrations(context.Context, []string) ([]Registration, error) {
	return l.regs, nil
}

func raw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// oneGoofspielRound is a realistic slice of the log: a prize revealed, the agent sealing its
// card 1.2s later, the round revealed (which is where the card value finally appears), and a
// line of reasoning.
func oneGoofspielRound() []MatchRow {
	return []MatchRow{
		{MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
			Type: "prize_revealed", CreatedAt: traceEpoch,
			Payload: raw(map[string]any{"round": 3, "prize": 11})},
		{MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
			Type: "card_sealed", CreatedAt: traceEpoch.Add(1200 * time.Millisecond),
			Payload: raw(map[string]any{"round": 3, "seat": 0})},
		{MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
			Type: "round_revealed", CreatedAt: traceEpoch.Add(1300 * time.Millisecond),
			Payload: raw(map[string]any{"round": 3, "prize": 11, "cards": []int{9, 4}, "winner": 0})},
		{MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
			Type: "agent_says", CreatedAt: traceEpoch.Add(1100 * time.Millisecond),
			Payload: raw(map[string]any{"round": 3, "seat": 0, "text": "the 11 is worth overpaying for", "kind": "rationale"})},
	}
}

func localSvc(t *testing.T, stub *localStub, lensURL string) *Service {
	t.Helper()
	svc := New(fakeRepo{owned: []string{"ag_mine"}}, lensURL, "k", "org", nil)
	svc.SetLocalRepo(stub)
	return svc
}

// With no Lens configured at all — the DEFAULT for any deployment that has not stood up a
// telemetry stack — the page is served entirely from the match log.
func TestTracesWorkWithNoLensAtAll(t *testing.T) {
	stub := &localStub{rows: oneGoofspielRound()}
	svc := localSvc(t, stub, "")

	got, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	if err != nil {
		t.Fatalf("no Lens must not be an error when the match log is available: %v", err)
	}
	if stub.calls == 0 {
		t.Fatal("the local match log was never consulted")
	}
	if len(got) == 0 {
		t.Fatal("no entries — this is the blank page developers were shown")
	}
}

// The decision carries the card the agent PLAYED and the time it took to choose it.
//
// Both are derived, and that is the point: `card_sealed` deliberately carries no card value
// (an opponent must not learn a move before both seats commit), so the value comes from the
// round reveal; and latency is the gap between the prize appearing and the card being
// sealed, which is literally how long the agent thought.
func TestDecisionCarriesTheCardAndTheThinkingTime(t *testing.T) {
	svc := localSvc(t, &localStub{rows: oneGoofspielRound()}, "")
	got, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}

	var decision *Entry
	for i := range got {
		if got[i].Type == "agent_decision" {
			decision = &got[i]
		}
	}
	if decision == nil {
		t.Fatalf("no decision entry in %+v", got)
	}
	if decision.LatencyMS != 1200 {
		t.Fatalf("latency = %dms, want 1200 (prize revealed → card sealed)", decision.LatencyMS)
	}
	if action, _ := decision.Detail["action"].(string); action != "played 9" {
		t.Fatalf("action = %q, want \"played 9\" — seat 0's card from the reveal", action)
	}
	if won, _ := decision.Detail["won"].(bool); !won {
		t.Fatal("seat 0 won round 3 in the fixture but the entry says otherwise")
	}
	if decision.MatchID != "m_1" || decision.Game != "goofspiel" {
		t.Fatalf("decision lost its match context: %+v", decision)
	}
}

// A rationale is the agent explaining itself, and it is the single most useful thing on this
// page — "why did my agent do that" is the question people arrive with.
func TestRationaleIsCarriedThrough(t *testing.T) {
	svc := localSvc(t, &localStub{rows: oneGoofspielRound()}, "")
	got, _ := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)

	for _, e := range got {
		if e.Type == "agent_said" {
			if r, _ := e.Detail["rationale"].(string); r == "the 11 is worth overpaying for" {
				return
			}
			t.Fatalf("agent_said entry lost its rationale: %+v", e.Detail)
		}
	}
	t.Fatal("no agent_said entry was produced")
}

// Table talk and reasoning stay DISTINCT. One was said to the opponent, the other explains a
// move; the client renders them differently and collapsing them would misattribute both.
func TestTableTalkIsNotFiledAsRationale(t *testing.T) {
	rows := []MatchRow{{
		MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
		Type: "agent_says", CreatedAt: traceEpoch,
		Payload: raw(map[string]any{"round": 2, "seat": 0, "text": "you always overbid", "kind": "say"}),
	}}
	svc := localSvc(t, &localStub{rows: rows}, "")
	got, _ := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)

	if len(got) != 1 {
		t.Fatalf("want one entry, got %+v", got)
	}
	if _, isRationale := got[0].Detail["rationale"]; isRationale {
		t.Fatal("table talk was filed as the agent's private reasoning")
	}
	if text, _ := got[0].Detail["text"].(string); text != "you always overbid" {
		t.Fatalf("text = %q", text)
	}
}

// AN OPPONENT'S MOVE IS NEVER ATTRIBUTED TO YOU. The reveal carries both cards; the seat
// decides which one is yours, and getting that backwards would show a developer someone
// else's play as their own.
func TestOpponentsCardIsNeverReportedAsYours(t *testing.T) {
	rows := []MatchRow{{
		// Same reveal, but the caller is seat 1 this time.
		MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 1,
		Type: "round_revealed", CreatedAt: traceEpoch,
		Payload: raw(map[string]any{"round": 3, "prize": 11, "cards": []int{9, 4}, "winner": 0}),
	}}
	svc := localSvc(t, &localStub{rows: rows}, "")
	got, _ := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)

	if len(got) != 1 {
		t.Fatalf("want one entry, got %+v", got)
	}
	if action, _ := got[0].Detail["action"].(string); action != "played 4" {
		t.Fatalf("action = %q, want \"played 4\" — seat 1's own card, not seat 0's", action)
	}
	if won, _ := got[0].Detail["won"].(bool); won {
		t.Fatal("seat 1 lost round 3 but the entry claims a win")
	}
}

// Someone else's table talk must not surface either. A payload seat that is not the caller's
// seat is dropped, which is the second gate on top of the SQL join.
func TestAnotherSeatsTalkIsDropped(t *testing.T) {
	rows := []MatchRow{{
		MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
		Type: "agent_says", CreatedAt: traceEpoch,
		Payload: raw(map[string]any{"round": 2, "seat": 1, "text": "opponent's line", "kind": "say"}),
	}}
	svc := localSvc(t, &localStub{rows: rows}, "")
	got, _ := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	if len(got) != 0 {
		t.Fatalf("leaked another seat's talk: %+v", got)
	}
}

// EVERY LENS FAILURE MODE STILL SHOWS THE MATCH LOG.
//
// This is the regression that matters most: each of these used to return an error and an
// empty page, and each is a real state a deployment has been in.
func TestEveryLensFailureStillServesTheMatchLog(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"refuses our key (401)", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }},
		{"forbidden (403)", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusForbidden) }},
		{"server error (500)", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{"unreadable body", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()

			svc := localSvc(t, &localStub{rows: oneGoofspielRound()}, srv.URL)
			got, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
			if err != nil {
				t.Fatalf("a broken Lens must not empty the page: %v", err)
			}
			if len(got) == 0 {
				t.Fatal("no entries — the developer would see 'trace store unreachable' over their own match log")
			}
		})
	}
}

// An unreachable host (DNS/refused/timeout) is the case the user reported verbatim.
func TestUnreachableLensStillServesTheMatchLog(t *testing.T) {
	// A closed server: connecting fails at the transport, not with an HTTP status.
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	svc := localSvc(t, &localStub{rows: oneGoofspielRound()}, url)
	got, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	if err != nil {
		t.Fatalf("want the match log, got error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("no entries — this is exactly the 'Trace store unreachable' page")
	}
}

// A failed LOCAL read must not take out a page a working Lens could fill. The two sources
// degrade independently.
func TestLocalFailureFallsBackToTheLens(t *testing.T) {
	stub := &lensStub{events: []map[string]any{{
		"event_time": traceEpoch, "event_type": "agent_decision", "agent_id": "ag_mine", "status": "ok",
	}}}
	srv := stub.server(t)
	defer srv.Close()

	svc := localSvc(t, &localStub{err: context.DeadlineExceeded}, srv.URL)
	got, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	if err != nil {
		t.Fatalf("a local read failure must not fail the request: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want the Lens entry, got %+v", got)
	}
}

// With BOTH sources up, the page shows both — the Lens contributes connection lifecycle the
// match log knows nothing about, and the match log contributes the moves.
func TestBothSourcesAreMerged(t *testing.T) {
	stub := &lensStub{events: []map[string]any{{
		"event_time": traceEpoch.Add(-time.Minute), "event_type": "agent_connected",
		"agent_id": "ag_mine", "status": "ok",
	}}}
	srv := stub.server(t)
	defer srv.Close()

	svc := localSvc(t, &localStub{rows: oneGoofspielRound()}, srv.URL)
	got, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]bool{}
	for _, e := range got {
		types[e.Type] = true
	}
	if !types["agent_connected"] {
		t.Fatal("lost the Lens's connection event")
	}
	if !types["agent_decision"] {
		t.Fatal("lost the match log's decisions")
	}
	// Newest first, so a timeline reads correctly however the two were interleaved.
	for i := 1; i < len(got); i++ {
		if got[i].At.After(got[i-1].At) {
			t.Fatalf("merged entries are not newest-first at %d: %v then %v", i, got[i-1].At, got[i].At)
		}
	}
}

// A developer whose agent exists but has never played sees that, rather than a blank page
// that reads as "my agent is broken".
func TestNoMatchesYetShowsTheRegistration(t *testing.T) {
	stub := &localStub{regs: []Registration{
		{AgentPublicID: "ag_mine", Name: "Nightshade", CreatedAt: traceEpoch},
	}}
	svc := localSvc(t, stub, "")
	got, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != "agent_registered" {
		t.Fatalf("want a single registration entry, got %+v", got)
	}
}

// Without a local repo the old contract still holds, so a deployment that deliberately runs
// Lens-only keeps its explicit "not enabled here" answer instead of a silent empty list.
func TestNoLocalRepoKeepsTheExplicitUnavailableError(t *testing.T) {
	svc := New(fakeRepo{owned: []string{"ag_mine"}}, "", "", "", nil)
	if _, err := svc.Activity(context.Background(), "usr_1", "", traceEpoch, 10); code(err) != "traces_unconfigured" {
		t.Fatalf("code = %q, want traces_unconfigured", code(err))
	}
}

// A nonsense clock (rows out of order, or a duplicated reveal) must not produce a fictional
// latency on a chart a developer tunes against.
func TestImplausibleLatencyIsReportedAsUnknown(t *testing.T) {
	rows := []MatchRow{
		{MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
			Type: "prize_revealed", CreatedAt: traceEpoch,
			Payload: raw(map[string]any{"round": 1})},
		{MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
			// Sealed BEFORE the prize appeared: impossible, so no number is better than one.
			Type: "card_sealed", CreatedAt: traceEpoch.Add(-time.Minute),
			Payload: raw(map[string]any{"round": 1, "seat": 0})},
		{MatchPublicID: "m_1", Game: "goofspiel", AgentPublicID: "ag_mine", Seat: 0,
			Type: "round_revealed", CreatedAt: traceEpoch.Add(time.Second),
			Payload: raw(map[string]any{"round": 1, "prize": 5, "cards": []int{2, 3}, "winner": 1})},
	}
	svc := localSvc(t, &localStub{rows: rows}, "")
	got, _ := svc.Activity(context.Background(), "usr_1", "", traceEpoch.Add(-time.Hour), 50)
	for _, e := range got {
		if e.Type == "agent_decision" && e.LatencyMS != 0 {
			t.Fatalf("reported %dms for an impossible ordering", e.LatencyMS)
		}
	}
}
