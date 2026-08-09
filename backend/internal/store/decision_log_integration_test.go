package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDecisionLogRoundTripIntegration writes decisions through RecordMatchDecisions and reads
// every column back from a REAL Postgres.
//
// WHY THIS EXISTS, given there is already a static guard on the same statement.
//
// The decision log is written by a hand-built multi-row INSERT: a column list in a string, a
// positional row slice, and a `::jsonb` cast pinned to one placeholder index. The static
// guard (pindex_repo_columns_test.go) checks those three agree by reading the source, which
// catches a miscount — but it cannot prove the statement EXECUTES. That gap matters here
// specifically: pgx sends an uncast []byte as bytea, and a bytea landing in a jsonb column
// fails at execute time rather than prepare time. A cast on the wrong placeholder is
// therefore invisible to every test that does not touch a database, and would surface first
// in production.
//
// Adding cached_write_tokens (0080) and then scaffold (0081) each shifted that index. This
// test is what makes the next such shift fail here instead of there.
//
// Skipped unless PYYOL_TEST_DATABASE_URL points at a migrated DB.
func TestDecisionLogRoundTripIntegration(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// t.Cleanup, not defer. A deferred Close runs when the test function RETURNS, which is
	// before every t.Cleanup — so a cleanup that deletes rows through this pool was running
	// against a closed pool and silently doing nothing (the deletes ignore their errors).
	// Registered FIRST so LIFO ordering runs it LAST, after the data cleanups.
	t.Cleanup(pool.Close)

	// A dedicated agent + match id so the test neither reads nor disturbs real lab data.
	const agentPublic = "ag_decisionlog_itest"
	const matchID = "m_decisionlog_itest"
	var agentID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO agents (public_id, name, slug, owner_user_id, status)
		 SELECT $1, 'decision-log-itest', 'decision-log-itest',
		        (SELECT id FROM users ORDER BY id LIMIT 1), 'active'
		 ON CONFLICT (public_id) DO UPDATE SET name = EXCLUDED.name
		 RETURNING id`, agentPublic).Scan(&agentID); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM agent_match_decisions WHERE match_id = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM agents WHERE public_id = $1`, agentPublic)
	})
	_, _ = pool.Exec(ctx, `DELETE FROM agent_match_decisions WHERE match_id = $1`, matchID)

	repo := NewPIndexRepo(pool)
	view := json.RawMessage(`{"round":3,"prize":7,"hand":[1,4,9]}`)
	started := time.Now().UTC().Truncate(time.Second)

	want := MatchDecision{
		Seq: 1, Round: 3, Action: "bid:9", Outcome: "won", LatencyMS: 812,
		Rationale: "high prize, spend the nine",
		Provider:  "anthropic", Model: "claude-opus-4",
		// The exact token shape from a live gateway call, on the normalized convention:
		// prompt is total billable input, with reads and writes as subsets of it.
		PromptTokens: 2520, CompletionTokens: 90, ReasoningTokens: 0,
		CachedTokens: 1500, CachedWriteTokens: 600, TotalTokens: 2610,
		EstimatedCost: 0.02655,
		Scaffold:      "sc_2fd2f65436751e0b", ScaffoldUnstable: false, ScaffoldIssue: "",
		InputJSON: view, InputTruncated: false, StartedAt: started,
	}
	if err := repo.RecordMatchDecisions(ctx, matchID, agentPublic, []MatchDecision{want}); err != nil {
		t.Fatalf("RecordMatchDecisions: %v", err)
	}

	var got MatchDecision
	var readBack []byte
	if err := pool.QueryRow(ctx,
		`SELECT seq, round, action, outcome, latency_ms, rationale, provider, model,
		        prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens,
		        cached_write_tokens, total_tokens, estimated_cost, scaffold, scaffold_unstable,
		        scaffold_issue, input_json, input_truncated
		   FROM agent_match_decisions WHERE match_id = $1 AND agent_id = $2 AND seq = 1`,
		matchID, agentID).Scan(
		&got.Seq, &got.Round, &got.Action, &got.Outcome, &got.LatencyMS, &got.Rationale,
		&got.Provider, &got.Model, &got.PromptTokens, &got.CompletionTokens,
		&got.ReasoningTokens, &got.CachedTokens, &got.CachedWriteTokens, &got.TotalTokens,
		&got.EstimatedCost, &got.Scaffold, &got.ScaffoldUnstable, &got.ScaffoldIssue,
		&readBack, &got.InputTruncated); err != nil {
		t.Fatalf("read back: %v", err)
	}

	if got.CachedWriteTokens != want.CachedWriteTokens {
		t.Errorf("cached_write_tokens = %d, want %d", got.CachedWriteTokens, want.CachedWriteTokens)
	}
	if got.Scaffold != want.Scaffold {
		t.Errorf("scaffold = %q, want %q", got.Scaffold, want.Scaffold)
	}
	if got.CachedTokens != want.CachedTokens || got.PromptTokens != want.PromptTokens {
		t.Errorf("tokens: prompt=%d cached=%d, want %d/%d",
			got.PromptTokens, got.CachedTokens, want.PromptTokens, want.CachedTokens)
	}
	// The invariant the cache normalization exists to hold, checked on what actually landed
	// in the table rather than only on the value in memory.
	if got.CachedTokens+got.CachedWriteTokens > got.PromptTokens {
		t.Errorf("stored row violates the subset invariant: %d+%d > %d",
			got.CachedTokens, got.CachedWriteTokens, got.PromptTokens)
	}
	// The whole point of the ::jsonb cast: the view must come back as readable JSON, not as
	// the escaped bytea text a missing cast would have produced.
	var decoded map[string]any
	if err := json.Unmarshal(readBack, &decoded); err != nil {
		t.Fatalf("input_json did not round-trip as JSON (the ::jsonb cast is on the wrong "+
			"placeholder): %v — raw: %q", err, string(readBack))
	}
	if decoded["round"] != float64(3) {
		t.Errorf("input_json round = %v, want 3 (raw: %s)", decoded["round"], readBack)
	}

	// Redelivery: the outbox delivers at least once, so the same event arriving twice must
	// refresh the row rather than duplicate or error on it.
	if err := repo.RecordMatchDecisions(ctx, matchID, agentPublic, []MatchDecision{want}); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_match_decisions WHERE match_id = $1`, matchID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Errorf("after redelivery there are %d rows, want 1 — the upsert is not idempotent", n)
	}

	// A replay that lost its usage block must NOT blank a fingerprint already recorded:
	// silently dropping one would remove the agent from every paired comparison, and nothing
	// about that failure looks like a bug.
	stripped := want
	stripped.Scaffold = ""
	stripped.Rationale = ""
	if err := repo.RecordMatchDecisions(ctx, matchID, agentPublic, []MatchDecision{stripped}); err != nil {
		t.Fatalf("replay without usage: %v", err)
	}
	var keptScaffold, keptRationale string
	if err := pool.QueryRow(ctx,
		`SELECT scaffold, rationale FROM agent_match_decisions
		  WHERE match_id = $1 AND agent_id = $2 AND seq = 1`,
		matchID, agentID).Scan(&keptScaffold, &keptRationale); err != nil {
		t.Fatalf("read after replay: %v", err)
	}
	if keptScaffold != want.Scaffold {
		t.Errorf("a replay without a scaffold erased the stored one (%q -> %q)",
			want.Scaffold, keptScaffold)
	}
	if keptRationale != want.Rationale {
		t.Errorf("a replay without a rationale erased the stored one (%q -> %q)",
			want.Rationale, keptRationale)
	}

	// An INELIGIBLE decision: no fingerprint, but a code saying why. The whole point is that a
	// developer whose agent silently drops out of paired comparison can see the reason and fix
	// it, rather than filing a ticket asking why their agent is missing from a board.
	ineligible := want
	ineligible.Seq = 2
	ineligible.Scaffold = ""
	ineligible.ScaffoldIssue = "no_system_prompt"
	if err := repo.RecordMatchDecisions(ctx, matchID, agentPublic, []MatchDecision{ineligible}); err != nil {
		t.Fatalf("record ineligible decision: %v", err)
	}
	var gotScaffold, gotIssue string
	if err := pool.QueryRow(ctx,
		`SELECT scaffold, scaffold_issue FROM agent_match_decisions
		  WHERE match_id = $1 AND agent_id = $2 AND seq = 2`,
		matchID, agentID).Scan(&gotScaffold, &gotIssue); err != nil {
		t.Fatalf("read ineligible: %v", err)
	}
	if gotScaffold != "" {
		t.Errorf("scaffold = %q, want empty — an unfingerprintable decision must not carry one", gotScaffold)
	}
	if gotIssue != "no_system_prompt" {
		t.Errorf("scaffold_issue = %q, want %q — without it the developer cannot tell why the "+
			"agent is excluded from the model board", gotIssue, "no_system_prompt")
	}
}
