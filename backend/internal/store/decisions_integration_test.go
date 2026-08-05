package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/devtrace"
)

// The per-decision record, end to end against real Postgres: written from a seat's
// decision log, read back owner-scoped.
//
// Before this table existed, every one of these fields was computed during the match,
// emitted to Lens, and dropped. A developer with no Lens could see that their agent
// made 240 decisions at 96% legal and nothing about any individual one — which is the
// only granularity at which an agent can actually be improved.

func TestMatchDecisionsRoundTripLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewPIndexRepo(pool)
	trace := NewDevTraceRepo(pool)
	_, run := isolate()

	mine, _ := mkUserAgent(t, pool, "dec-mine-"+run)
	theirs, _ := mkUserAgent(t, pool, "dec-theirs-"+run)
	match := "m_dec_" + run

	if err := repo.RecordMatchDecisions(ctx, match, mine, []MatchDecision{
		{
			Seq: 0, Round: 1, Action: "9", Outcome: "ok", LatencyMS: 820,
			Rationale: "the 11 is worth overpaying for",
			Provider:  "anthropic", Model: "claude-sonnet-4",
			PromptTokens: 620, CompletionTokens: 44, TotalTokens: 664, EstimatedCost: 0.0025,
		},
		{
			Seq: 1, Round: 2, Action: "1", Outcome: "illegal", LatencyMS: 4100,
			Rationale: "dumping my lowest on a cheap prize",
			Provider:  "anthropic", Model: "claude-sonnet-4",
			PromptTokens: 640, CompletionTokens: 51, TotalTokens: 691, EstimatedCost: 0.0027,
		},
		// A move made without an LLM call: no usage, no rationale. It must still be
		// recorded — a turn the agent took cheaply is a fact about the agent.
		{Seq: 2, Round: 3, Action: "5", Outcome: "ok", LatencyMS: 3},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Another developer's agent in the same match, with its own reasoning.
	if err := repo.RecordMatchDecisions(ctx, match, theirs, []MatchDecision{
		{Seq: 0, Round: 1, Action: "12", Outcome: "ok", Rationale: "SECRET opponent reasoning"},
	}); err != nil {
		t.Fatalf("record other: %v", err)
	}

	got, err := trace.MatchDecisions(ctx, []string{mine}, match, 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d decisions, want 3: %+v", len(got), got)
	}

	// Order is the log's order, not insertion luck.
	for i, d := range got {
		if d.Seq != i {
			t.Errorf("row %d has seq %d — decisions must come back in order", i, d.Seq)
		}
	}

	first := got[0]
	if first.Round != 1 || first.Action != "9" || first.Outcome != "ok" || first.LatencyMS != 820 {
		t.Errorf("first decision = %+v", first)
	}
	if first.Rationale != "the 11 is worth overpaying for" {
		t.Errorf("rationale = %q — the reasoning is the point of the table", first.Rationale)
	}
	if first.Model != "claude-sonnet-4" || first.TotalTokens != 664 || first.EstimatedCost == 0 {
		t.Errorf("per-move economics lost: %+v", first)
	}
	// An illegal move must be visible AS illegal — this is the row a developer opens the
	// trace to find.
	if got[1].Outcome != "illegal" {
		t.Errorf("second outcome = %q, want illegal", got[1].Outcome)
	}
	// The LLM-free turn is present with zeroed economics rather than absent.
	if got[2].Action != "5" || got[2].TotalTokens != 0 || got[2].Rationale != "" {
		t.Errorf("third decision = %+v, want a recorded turn with no usage", got[2])
	}

	// ── the privacy boundary ────────────────────────────────────────────────────
	// A rationale is private reasoning about opponents. Reading the same match as the
	// caller who owns `mine` must never return the other developer's row.
	for _, d := range got {
		if d.Rationale == "SECRET opponent reasoning" {
			t.Fatal("another developer's private reasoning leaked into this trace")
		}
	}
	// And the other developer sees only their own.
	other, err := trace.MatchDecisions(ctx, []string{theirs}, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].Rationale != "SECRET opponent reasoning" {
		t.Errorf("owner should see their own row: %+v", other)
	}

	// ── redelivery ──────────────────────────────────────────────────────────────
	// The outbox delivers at least once. A replay must refresh, not duplicate — and a
	// replay that lost its rationale must not erase the one already stored.
	if err := repo.RecordMatchDecisions(ctx, match, mine, []MatchDecision{
		{Seq: 0, Round: 1, Action: "9", Outcome: "ok", LatencyMS: 820}, // no rationale
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	again, err := trace.MatchDecisions(ctx, []string{mine}, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 3 {
		t.Fatalf("replay duplicated rows: %d", len(again))
	}
	if again[0].Rationale != "the 11 is worth overpaying for" {
		t.Errorf("a replay without a rationale erased the stored one: %q", again[0].Rationale)
	}

	// An unknown agent is a no-op, not an error: bots have no agent row.
	if err := repo.RecordMatchDecisions(ctx, match, "ag_not_real", []MatchDecision{{Seq: 0}}); err != nil {
		t.Errorf("unknown agent should be a no-op, got %v", err)
	}
	// Empty inputs are no-ops too.
	if err := repo.RecordMatchDecisions(ctx, match, mine, nil); err != nil {
		t.Errorf("empty log should be a no-op, got %v", err)
	}
}

// MatchDetail must carry the decisions, since that is what the trace page renders.
func TestMatchDetailCarriesDecisions(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	_, run := isolate()

	agent, _ := mkUserAgent(t, pool, "decd-"+run)
	match := "m_decd_" + run
	if err := NewPIndexRepo(pool).RecordMatchDecisions(ctx, match, agent, []MatchDecision{
		{Seq: 0, Round: 4, Action: "vote", Outcome: "ok", Rationale: "seat 3 went quiet"},
	}); err != nil {
		t.Fatal(err)
	}
	var got []devtrace.Decision
	got, err := NewDevTraceRepo(pool).MatchDecisions(ctx, []string{agent}, match, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Rationale != "seat 3 went quiet" || got[0].Round != 4 {
		t.Fatalf("decision did not survive to the read port: %+v", got)
	}
}

// The INPUT half, end to end against real Postgres.
//
// Three things can go silently wrong here and only a real database catches them: pgx
// sends []byte as bytea unless the placeholder is cast (a type error at execute time,
// not prepare time); an empty capture stored without care becomes the jsonb literal
// `null`, which reads back indistinguishably from a view that really was absent; and a
// redelivered event must not erase a view already captured.
func TestDecisionInputRoundTripLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewPIndexRepo(pool)
	trace := NewDevTraceRepo(pool)
	_, run := isolate()

	mine, _ := mkUserAgent(t, pool, "in-mine-"+run)
	theirs, _ := mkUserAgent(t, pool, "in-theirs-"+run)
	match := "m_in_" + run

	view := []byte(`{"round":1,"prize":11,"hand":[1,5,9,13],"you":{"seat":0}}`)
	if err := repo.RecordMatchDecisions(ctx, match, mine, []MatchDecision{
		// A move WITH the view kept.
		{Seq: 0, Round: 1, Action: "9", Outcome: "ok", Rationale: "worth overpaying",
			InputJSON: view},
		// A move whose view was dropped for size.
		{Seq: 1, Round: 2, Action: "1", Outcome: "ok", InputTruncated: true},
		// A move that never had a view at all.
		{Seq: 2, Round: 3, Action: "5", Outcome: "ok"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Another developer's seat, with its own hidden information.
	if err := repo.RecordMatchDecisions(ctx, match, theirs, []MatchDecision{
		{Seq: 0, Round: 1, Action: "12", Outcome: "ok",
			InputJSON: []byte(`{"hand":[2,3,4],"secret":"OPPONENT HAND"}`)},
	}); err != nil {
		t.Fatalf("record other: %v", err)
	}

	got, err := trace.MatchDecisions(ctx, []string{mine}, match, 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d decisions, want 3", len(got))
	}

	// ── the view survives as the arena's own shape ───────────────────────────────
	var back struct {
		Round int   `json:"round"`
		Prize int   `json:"prize"`
		Hand  []int `json:"hand"`
	}
	if err := json.Unmarshal(got[0].Input, &back); err != nil {
		t.Fatalf("stored input is not valid json: %v (%s)", err, got[0].Input)
	}
	if back.Prize != 11 || len(back.Hand) != 4 {
		t.Errorf("view did not survive the round trip: %+v", back)
	}
	if got[0].InputTruncated {
		t.Error("a kept view must not be flagged truncated")
	}

	// ── dropped is distinguishable from absent ───────────────────────────────────
	if len(got[1].Input) != 0 || !got[1].InputTruncated {
		t.Errorf("dropped view: input=%q truncated=%v, want empty + true",
			got[1].Input, got[1].InputTruncated)
	}
	if len(got[2].Input) != 0 || got[2].InputTruncated {
		t.Errorf("absent view: input=%q truncated=%v, want empty + false — a decision "+
			"that never had a view must not claim one was lost", got[2].Input, got[2].InputTruncated)
	}
	// And at the SQL level the absent one is a true NULL, not the jsonb literal `null`:
	// the two are different facts and only one of them is true here.
	var isNull bool
	if err := pool.QueryRow(ctx,
		`SELECT d.input_json IS NULL FROM agent_match_decisions d
		   JOIN agents a ON a.id = d.agent_id
		  WHERE d.match_id = $1 AND a.public_id = $2 AND d.seq = 2`, match, mine).Scan(&isNull); err != nil {
		t.Fatal(err)
	}
	if !isNull {
		t.Error("an absent view stored the jsonb literal `null` instead of SQL NULL")
	}

	// ── the privacy boundary, on the most sensitive column in the schema ─────────
	for _, d := range got {
		if len(d.Input) > 0 && strings.Contains(string(d.Input), "OPPONENT HAND") {
			t.Fatal("another seat's hidden information leaked into this developer's trace")
		}
	}

	// ── redelivery must not erase a captured view ────────────────────────────────
	if err := repo.RecordMatchDecisions(ctx, match, mine, []MatchDecision{
		{Seq: 0, Round: 1, Action: "9", Outcome: "ok"}, // replay, no view
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	again, err := trace.MatchDecisions(ctx, []string{mine}, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(again[0].Input) == 0 {
		t.Error("a replay without a view erased the view already captured")
	}
	if again[0].InputTruncated {
		t.Error("a replay must not mark an already-captured view as truncated")
	}
}

// The timeline anchor, end to end. A real timestamp must survive as a real timestamp,
// and an absent one must stay absent rather than becoming year 1 — which would place
// every pre-stamp decision at the start of a two-millennium timeline.
func TestDecisionStartedAtRoundTripLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	_, run := isolate()
	agent, _ := mkUserAgent(t, pool, "ts-"+run)
	match := "m_ts_" + run

	t0 := time.Now().UTC().Truncate(time.Millisecond)
	if err := NewPIndexRepo(pool).RecordMatchDecisions(ctx, match, agent, []MatchDecision{
		{Seq: 0, Round: 1, Action: "9", Outcome: "ok", LatencyMS: 300, StartedAt: t0},
		{Seq: 1, Round: 2, Action: "4", Outcome: "ok", LatencyMS: 250,
			StartedAt: t0.Add(2 * time.Second)},
		// No timestamp: a decision recorded before the platform stamped them.
		{Seq: 2, Round: 3, Action: "1", Outcome: "ok", LatencyMS: 100},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	got, err := NewDevTraceRepo(pool).MatchDecisions(ctx, []string{agent}, match, 100)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows", len(got))
	}
	if got[0].StartedAt == nil || got[1].StartedAt == nil {
		t.Fatalf("timestamps lost: %+v / %+v", got[0].StartedAt, got[1].StartedAt)
	}
	if !got[0].StartedAt.Equal(t0) {
		t.Errorf("first started_at = %s, want %s", got[0].StartedAt, t0)
	}
	// The GAP is the whole reason for the column: 2s between asks, only 300ms of it
	// spent working.
	gap := got[1].StartedAt.Sub(got[0].StartedAt.Add(300 * time.Millisecond))
	if gap < 1500*time.Millisecond {
		t.Errorf("inter-turn gap = %s, want ~1.7s of visible idle time", gap)
	}
	// An unstamped decision stays unstamped — never back-filled to the zero time.
	if got[2].StartedAt != nil {
		t.Errorf("an unstamped decision came back as %s; it must stay NULL so the client "+
			"draws no timeline rather than a fabricated one", got[2].StartedAt)
	}

	// A replay without the timestamp must not erase the stored one.
	if err := NewPIndexRepo(pool).RecordMatchDecisions(ctx, match, agent, []MatchDecision{
		{Seq: 0, Round: 1, Action: "9", Outcome: "ok", LatencyMS: 300},
	}); err != nil {
		t.Fatal(err)
	}
	again, err := NewDevTraceRepo(pool).MatchDecisions(ctx, []string{agent}, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	if again[0].StartedAt == nil || !again[0].StartedAt.Equal(t0) {
		t.Errorf("a replay without a timestamp erased the stored one: %+v", again[0].StartedAt)
	}
}
