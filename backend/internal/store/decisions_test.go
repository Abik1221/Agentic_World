package store

import (
	"testing"

	"github.com/agent-arena/arena/internal/benchmark"
)

// The projection from a real SeatSummary to the persisted per-move record.
//
// This link used to live in package main, where nothing could test it — so the one
// step that decides whether a developer's decision log is correct or silently wrong was
// the only step with no coverage.

func TestDecisionsFromSeat(t *testing.T) {
	seat := benchmark.SeatSummary{
		AgentID: "ag_1", Provider: "anthropic", Model: "claude-sonnet-4",
		DecisionLog: []benchmark.DecisionDetail{
			{
				Round: 1, Action: "9", Outcome: "ok", LatencyMS: 820,
				Rationale: "the 11 is worth overpaying for",
				Usage: &benchmark.TokenUsage{
					PromptTokens: 600, CompletionTokens: 40, TotalTokens: 640,
					Provider: "anthropic", Model: "claude-sonnet-4",
				},
			},
			// A turn taken WITHOUT an LLM call — cached, or rule-based.
			{Round: 2, Action: "1", Outcome: "ok", LatencyMS: 2},
			// A move on a DIFFERENT model: an agent may use something cheap for routine
			// turns. The per-move model must survive, or the whole reason for storing it
			// per decision is gone.
			{
				Round: 3, Action: "12", Outcome: "illegal", LatencyMS: 5400,
				Usage: &benchmark.TokenUsage{
					PromptTokens: 100, CompletionTokens: 10,
					Provider: "openai", Model: "gpt-4o-mini",
				},
			},
		},
	}

	got := DecisionsFromSeat(seat, "anthropic", "claude-sonnet-4")
	if len(got) != 3 {
		t.Fatalf("got %d rows, want one per logged move", len(got))
	}

	// seq is the LOG's order, not the round: Mafia takes several actions per day and
	// some arenas report round 0 throughout, so round cannot carry the ordering.
	for i, d := range got {
		if d.Seq != i {
			t.Errorf("row %d has seq %d", i, d.Seq)
		}
	}

	first := got[0]
	if first.Round != 1 || first.Action != "9" || first.Outcome != "ok" || first.LatencyMS != 820 {
		t.Errorf("first = %+v", first)
	}
	if first.Rationale != "the 11 is worth overpaying for" {
		t.Errorf("rationale lost: %q", first.Rationale)
	}
	if first.TotalTokens != 640 || first.EstimatedCost <= 0 {
		t.Errorf("economics lost: tokens=%d cost=%f", first.TotalTokens, first.EstimatedCost)
	}

	// A move with no LLM call keeps the SEAT's model rather than blanking it — a blank
	// would render as though the agent switched models for that turn.
	second := got[1]
	if second.Model != "claude-sonnet-4" || second.Provider != "anthropic" {
		t.Errorf("no-usage move lost its seat attribution: %+v", second)
	}
	if second.TotalTokens != 0 || second.EstimatedCost != 0 {
		t.Errorf("a move with no LLM call must cost nothing: %+v", second)
	}

	// A move on another model reports THAT model, not the seat's.
	third := got[2]
	if third.Model != "gpt-4o-mini" || third.Provider != "openai" {
		t.Errorf("per-move model did not win over the seat's: %+v", third)
	}
	if third.Outcome != "illegal" {
		t.Errorf("outcome = %q, want illegal — this is the row a developer opens the log to find", third.Outcome)
	}
	// TotalTokens was absent from the usage, so it is derived from the parts rather
	// than reported as zero.
	if third.TotalTokens != 110 {
		t.Errorf("total tokens = %d, want 100+10 derived from the parts", third.TotalTokens)
	}
}

func TestDecisionsFromSeatEmptyLog(t *testing.T) {
	if got := DecisionsFromSeat(benchmark.SeatSummary{AgentID: "ag_1"}, "openai", "gpt-4o"); got != nil {
		t.Fatalf("a seat with no logged moves must produce no rows, got %+v", got)
	}
}

// The input must survive the projection untouched.
//
// This is the seam between the producer (which serialized and capped the view) and the
// store (which only carries it). If this layer re-marshalled or dropped it, the caps
// would be enforced in one place and the data lost in another.
func TestDecisionsFromSeatCarriesInput(t *testing.T) {
	view := []byte(`{"round":3,"prize":7,"hand":[2,4,6]}`)
	seat := benchmark.SeatSummary{
		AgentID: "ag_1", Provider: "openai", Model: "gpt-4o",
		DecisionLog: []benchmark.DecisionDetail{
			{Round: 3, Action: "6", Outcome: "ok", Input: view},
			// A view that existed and was dropped for size.
			{Round: 4, Action: "2", Outcome: "ok", InputTruncated: true},
			// A move that never had one.
			{Round: 5, Action: "4", Outcome: "ok"},
		},
	}
	got := DecisionsFromSeat(seat, "openai", "gpt-4o")
	if len(got) != 3 {
		t.Fatalf("got %d rows", len(got))
	}

	// Byte-identical: the store carries the producer's bytes, it does not re-encode them.
	if string(got[0].InputJSON) != string(view) {
		t.Errorf("input was altered in transit:\n got %s\nwant %s", got[0].InputJSON, view)
	}
	if got[0].InputTruncated {
		t.Error("a kept view must not be flagged truncated")
	}

	// The two absent cases stay distinguishable all the way through.
	if len(got[1].InputJSON) != 0 || !got[1].InputTruncated {
		t.Errorf("dropped view: json=%q truncated=%v, want empty + true", got[1].InputJSON, got[1].InputTruncated)
	}
	if len(got[2].InputJSON) != 0 || got[2].InputTruncated {
		t.Errorf("absent view: json=%q truncated=%v, want empty + false", got[2].InputJSON, got[2].InputTruncated)
	}
}
