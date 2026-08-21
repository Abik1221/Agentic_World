package harnessseed

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEmbeddedExportIsReconciled runs the real shipped file through the real check.
//
// It documents the CURRENT state of data/lab-2026-08.json rather than asserting it is clean,
// and pins exactly which rules fire so a regeneration that fixes one and breaks another cannot
// pass unnoticed. That is precisely what it caught when the export was regenerated to carry the
// paid trials: two counts moved and the test refused to go green on either.
//
// One rule has since been retired from the expected set. bound_call_must_be_2xx fired on 67
// calls flagged bound beside a 429, 502, 400 or 401 — a response that returned no completion
// cannot have bound a move, so the export now derives the flag as (bound AND status 2xx) and
// the rule reports zero. Corrected in the export rather than in the lab rows: the table is the
// record of what happened, errors included, and rewriting history to make a publication clean
// is the wrong direction.
//
// Two remain, and both are honest about the same underlying fact rather than a defect in the
// data path:
//
//	bound_call_must_have_tokens        113 calls bound with no token count. These are the
//	                                   UsageUnreadable case — a provider shape the normaliser
//	                                   could not read, which is loudly logged and costed at
//	                                   zero. The binding itself is real; only the usage is
//	                                   missing, so dropping the call would lose a decision
//	                                   that genuinely happened.
//	decision_model_must_match_gateway  Lab personas declared a display model ("claude-opus-4")
//	                                   while the gateway called the stand-in. Harmless where
//	                                   it matters: every published surface resolves attribution
//	                                   gateway-verified FIRST and falls back to self-reported
//	                                   only when no verified call exists, so these rows publish
//	                                   under the model that actually answered.
func TestEmbeddedExportIsReconciled(t *testing.T) {
	var r results
	if err := json.Unmarshal(resultsJSON, &r); err != nil {
		t.Fatalf("parse embedded export: %v", err)
	}
	rep := reconcile(&r)
	t.Logf("report: %s", rep.Summary())

	if !rep.Fatal {
		t.Fatal("embedded export reconciled clean — if it was regenerated, invert this test " +
			"to assert cleanliness rather than deleting it")
	}
	fired := map[string]int{}
	for _, f := range rep.Findings {
		fired[f.Rule] = f.Count
	}
	for _, rule := range []string{
		"bound_call_must_have_tokens",
		"decision_model_must_match_gateway",
	} {
		if fired[rule] == 0 {
			t.Errorf("expected rule %q to fire on the shipped export, it did not", rule)
		}
	}
	// Fixed, and it must STAY fixed: a call the provider refused cannot carry a bound move,
	// and letting this creep back would credit models with decisions never made.
	if got := fired["bound_call_must_be_2xx"]; got != 0 {
		t.Errorf("non-2xx bound calls = %d, want 0 — the export is flagging refused calls as "+
			"bound again, which inflates the one figure this benchmark is trusted on", got)
	}
	if got := fired["bound_call_must_have_tokens"]; got != 113 {
		t.Errorf("zero-token bound calls = %d, want 113", got)
	}
}

// TestReconcileAcceptsAConsistentExport proves the check is not simply always-fatal.
func TestReconcileAcceptsAConsistentExport(t *testing.T) {
	var r results
	mk := func(model string) {
		r.ModelCalls = append(r.ModelCalls, struct {
			MatchID           string  `json:"match_id"`
			Agent             string  `json:"agent"`
			Round             *int    `json:"round"`
			Bound             bool    `json:"bound"`
			Provider          string  `json:"provider"`
			Model             string  `json:"model"`
			UpstreamHost      string  `json:"upstream_host"`
			PromptTokens      int     `json:"prompt_tokens"`
			CompletionTokens  int     `json:"completion_tokens"`
			CachedReadTokens  int     `json:"cached_read_tokens"`
			CachedWriteTokens int     `json:"cached_write_tokens"`
			ReasoningTokens   int     `json:"reasoning_tokens"`
			LatencyMS         int64   `json:"latency_ms"`
			Status            int     `json:"status"`
			Streamed          bool    `json:"streamed"`
			CreatedAt         *string `json:"created_at"`
		}{MatchID: "m1", Agent: "ag1", Bound: true, Model: model, Status: 200,
			PromptTokens: 10, CompletionTokens: 5})
	}
	mk("groq/llama-3.1-8b-instant")
	scaffold := "sc_abc"
	declared := "llama-3.1-8b-instant"
	r.Decisions = append(r.Decisions, struct {
		MatchID          string   `json:"match_id"`
		Agent            string   `json:"agent"`
		Seq              int      `json:"seq"`
		Round            *int     `json:"round"`
		Action           *string  `json:"action"`
		Outcome          *string  `json:"outcome"`
		LatencyMS        *int64   `json:"latency_ms"`
		Provider         *string  `json:"provider"`
		Model            *string  `json:"model"`
		PromptTokens     *int     `json:"prompt_tokens"`
		CompletionTokens *int     `json:"completion_tokens"`
		ReasoningTokens  *int     `json:"reasoning_tokens"`
		CachedTokens     *int     `json:"cached_tokens"`
		TotalTokens      *int     `json:"total_tokens"`
		EstimatedCost    *float64 `json:"estimated_cost"`
		Scaffold         *string  `json:"scaffold"`
		ScaffoldUnstable *bool    `json:"scaffold_unstable"`
		ScaffoldIssue    *string  `json:"scaffold_issue"`
		CreatedAt        *string  `json:"created_at"`
	}{MatchID: "m1", Agent: "ag1", Seq: 1, Model: &declared, Scaffold: &scaffold})

	rep := reconcile(&r)
	if rep.Fatal {
		t.Fatalf("a consistent export was rejected: %s", rep.Summary())
	}
}

// TestVendorPrefixIsNotAMismatch. The same weights are routed under different names —
// groq serves "openai/gpt-oss-120b". Flagging that as fraud would make the check useless.
func TestVendorPrefixIsNotAMismatch(t *testing.T) {
	if normModel("openai/gpt-oss-120b") != normModel("gpt-oss-120b") {
		t.Fatal("vendor prefix should not change the comparison key")
	}
	if normModel("google/gemma-4-26b-a4b-it:free") != normModel("gemma-4-26b-a4b-it") {
		t.Fatal(":free suffix should not change the comparison key")
	}
	if normModel("claude-opus-4") == normModel("llama-3.1-8b-instant") {
		t.Fatal("genuinely different models must not compare equal")
	}
}

func TestSeverityOrderingPutsFatalFirst(t *testing.T) {
	var r results
	r.Decisions = append(r.Decisions, struct {
		MatchID          string   `json:"match_id"`
		Agent            string   `json:"agent"`
		Seq              int      `json:"seq"`
		Round            *int     `json:"round"`
		Action           *string  `json:"action"`
		Outcome          *string  `json:"outcome"`
		LatencyMS        *int64   `json:"latency_ms"`
		Provider         *string  `json:"provider"`
		Model            *string  `json:"model"`
		PromptTokens     *int     `json:"prompt_tokens"`
		CompletionTokens *int     `json:"completion_tokens"`
		ReasoningTokens  *int     `json:"reasoning_tokens"`
		CachedTokens     *int     `json:"cached_tokens"`
		TotalTokens      *int     `json:"total_tokens"`
		EstimatedCost    *float64 `json:"estimated_cost"`
		Scaffold         *string  `json:"scaffold"`
		ScaffoldUnstable *bool    `json:"scaffold_unstable"`
		ScaffoldIssue    *string  `json:"scaffold_issue"`
		CreatedAt        *string  `json:"created_at"`
	}{MatchID: "m1", Agent: "ag1"}) // no scaffold -> Warn, no evidence -> Warn
	r.BoundDecisions = append(r.BoundDecisions, struct {
		MatchID        string  `json:"match_id"`
		Agent          string  `json:"agent"`
		Round          int     `json:"round"`
		ExtractedMove  *string `json:"extracted_move"`
		CompletionHash *string `json:"completion_hash"`
		BindReceipt    *string `json:"bind_receipt"`
		CreatedAt      *string `json:"created_at"`
	}{MatchID: "m1", Agent: "ag1", Round: 1}) // orphan -> Fatal

	rep := reconcile(&r)
	if !rep.Fatal || len(rep.Findings) < 2 {
		t.Fatalf("want a fatal plus warnings, got %s", rep.Summary())
	}
	if rep.Findings[0].Severity != Fatal {
		t.Fatalf("fatal finding must sort first, got %s", rep.Findings[0].Rule)
	}
	if !strings.HasPrefix(rep.Summary(), "[FATAL]") {
		t.Fatalf("summary should lead with the fatal finding: %s", rep.Summary())
	}
}
