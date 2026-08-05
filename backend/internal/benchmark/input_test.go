package benchmark

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// Input capture — the view the agent was handed.
//
// The caps are the whole risk here: a Monopoly view carries the board and there can be
// 256 per seat, so getting them wrong turns an observability feature into an outbox
// outage. And a dropped view must be DISTINGUISHABLE from no view, or a developer reads
// an empty pane as an engine bug.

func TestInputIsCapturedAndSerialized(t *testing.T) {
	r := NewRecorder("goofspiel", "m1")
	type view struct {
		Round int   `json:"round"`
		Prize int   `json:"prize"`
		Hand  []int `json:"hand"`
	}
	r.Record(Decision{
		Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK, Round: 1, Action: "9",
		View: view{Round: 1, Prize: 11, Hand: []int{1, 5, 9, 13}},
	})

	log := r.Summary().Seats[0].DecisionLog
	if len(log) != 1 {
		t.Fatalf("want one logged move, got %d", len(log))
	}
	if log[0].InputTruncated {
		t.Error("a small view must not be reported as truncated")
	}
	// Round-trips as the arena's own shape — the client renders it, so it must not be
	// flattened or re-keyed on the way through.
	var back view
	if err := json.Unmarshal(log[0].Input, &back); err != nil {
		t.Fatalf("captured input is not valid json: %v", err)
	}
	if back.Prize != 11 || len(back.Hand) != 4 {
		t.Errorf("view did not survive: %+v", back)
	}
}

func TestNoViewIsNotTruncated(t *testing.T) {
	r := NewRecorder("goofspiel", "m1")
	r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK})
	d := r.Summary().Seats[0].DecisionLog[0]
	if d.Input != nil {
		t.Errorf("no view should store nothing, got %s", d.Input)
	}
	// The distinction that keeps the UI honest: "handed nothing" is not "not kept".
	if d.InputTruncated {
		t.Error("a decision with no view must NOT be marked truncated — the UI would " +
			"tell the developer we lost something that never existed")
	}
}

func TestOversizeViewIsDroppedAndMarked(t *testing.T) {
	r := NewRecorder("monopoly", "m1")
	huge := map[string]string{"board": strings.Repeat("x", maxInputBytes+1)}
	r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK, View: huge})

	d := r.Summary().Seats[0].DecisionLog[0]
	if d.Input != nil {
		t.Errorf("a view over the per-decision cap must not be stored (%d bytes)", len(d.Input))
	}
	if !d.InputTruncated {
		t.Fatal("a dropped view MUST be marked, or the developer cannot tell it was dropped")
	}
	// The move itself is still fully recorded — losing the input must never lose the
	// decision.
	if d.Outcome != string(OutcomeOK) {
		t.Errorf("the decision was damaged by dropping its input: %+v", d)
	}
}

func TestSeatInputBudgetStopsALongMatch(t *testing.T) {
	r := NewRecorder("monopoly", "m1")
	// Views comfortably under the per-decision cap, but enough of them to exceed the
	// seat's total budget. Without the second cap, a long match multiplies a
	// merely-large view into an unbounded payload.
	view := map[string]string{"state": strings.Repeat("y", 8<<10)}
	const moves = 200
	for i := 0; i < moves; i++ {
		r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK, Round: i, View: view})
	}

	seat := r.Summary().Seats[0]
	var kept, dropped, bytes int
	for _, d := range seat.DecisionLog {
		if d.Input != nil {
			kept++
			bytes += len(d.Input)
		}
		if d.InputTruncated {
			dropped++
		}
	}
	if kept == 0 {
		t.Fatal("the budget must keep the EARLY views, not refuse everything")
	}
	if dropped == 0 {
		t.Fatal("once the budget is spent, later views must be marked dropped")
	}
	if bytes > maxSeatInputBytes {
		t.Fatalf("seat kept %d bytes of input, over the %d budget", bytes, maxSeatInputBytes)
	}
	// Every move is still logged (up to the decision-log cap) even once inputs stop.
	if len(seat.DecisionLog) != moves {
		t.Errorf("logged %d moves, want all %d — the input budget must not drop decisions",
			len(seat.DecisionLog), moves)
	}
	if seat.Decisions != moves {
		t.Errorf("aggregate decisions = %d, want %d", seat.Decisions, moves)
	}
}

func TestUnserializableViewCannotBreakTheMatch(t *testing.T) {
	r := NewRecorder("goofspiel", "m1")
	// A channel cannot be marshalled. Instrumentation must degrade, never fail.
	r.Record(Decision{
		Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK, Action: "9",
		View: map[string]any{"ch": make(chan int)},
	})
	d := r.Summary().Seats[0].DecisionLog[0]
	if d.Input != nil {
		t.Error("an unmarshallable view must store nothing")
	}
	if !d.InputTruncated {
		t.Error("...and must be reported as not kept")
	}
	if d.Action != "9" {
		t.Errorf("the decision itself was lost: %+v", d)
	}
}

// The seat's byte counter is a budget, not a fact about the match — it must never reach
// the wire and be mistaken for one.
func TestInputBudgetCounterIsNotSerialized(t *testing.T) {
	r := NewRecorder("goofspiel", "m1")
	r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK, View: map[string]int{"a": 1}})
	raw, err := json.Marshal(r.Summary())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "inputBytes") || strings.Contains(string(raw), "input_bytes") {
		t.Fatalf("the internal budget counter leaked onto the wire: %s", raw)
	}
}

// The decision TIMESTAMP — the anchor a waterfall needs.
//
// It is derived (record time − latency), so the derivation is the thing to pin: get the
// sign wrong and every bar sits after the answer instead of before it, which reads as an
// agent that responded before it was asked.
func TestDecisionIsStampedAtTheAsk(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	// A clock that advances 1s per read, so consecutive decisions land apart.
	var n int
	r := NewRecorder("goofspiel", "m1", WithClock(func() time.Time {
		n++
		return base.Add(time.Duration(n) * time.Second)
	}))

	// Answered at base+1s having taken 400ms ⇒ asked at base+600ms.
	r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK, Round: 1, LatencyMS: 400})
	// Answered at base+2s having taken 1500ms ⇒ asked at base+500ms.
	r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeTimeout, Round: 2, LatencyMS: 1500})

	log := r.Summary().Seats[0].DecisionLog
	if len(log) != 2 {
		t.Fatalf("want 2 logged moves, got %d", len(log))
	}
	if want := base.Add(600 * time.Millisecond); !log[0].At.Equal(want) {
		t.Errorf("first ask = %s, want %s (answer − latency)", log[0].At, want)
	}
	if want := base.Add(500 * time.Millisecond); !log[1].At.Equal(want) {
		t.Errorf("second ask = %s, want %s", log[1].At, want)
	}
	// The ask must precede the answer, always. A positive offset here would draw every
	// bar as if the agent replied before being asked.
	for i, d := range log {
		answer := d.At.Add(time.Duration(d.LatencyMS) * time.Millisecond)
		if !d.At.Before(answer) && d.LatencyMS > 0 {
			t.Errorf("move %d: ask %s is not before answer %s", i, d.At, answer)
		}
	}
}

// A zero-latency decision still gets a real timestamp — it is a moment, not a gap.
func TestZeroLatencyDecisionStillStamped(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewRecorder("mafia", "m1", WithClock(func() time.Time { return at }))
	r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK})
	if got := r.Summary().Seats[0].DecisionLog[0].At; !got.Equal(at) {
		t.Fatalf("At = %s, want %s", got, at)
	}
}

// Timestamps must be MONOTONIC in log order for a waterfall to render sanely, which for
// a single seat follows from the engine asking one turn at a time.
func TestDecisionTimestampsAdvanceWithTheMatch(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	var n int
	r := NewRecorder("goofspiel", "m1", WithClock(func() time.Time {
		n++
		// Each turn: 2s of waiting, then a 300ms answer.
		return base.Add(time.Duration(n) * 2300 * time.Millisecond)
	}))
	for i := 0; i < 5; i++ {
		r.Record(Decision{Seat: 0, AgentID: "ag_1", Outcome: OutcomeOK, Round: i, LatencyMS: 300})
	}
	log := r.Summary().Seats[0].DecisionLog
	for i := 1; i < len(log); i++ {
		if !log[i].At.After(log[i-1].At) {
			t.Fatalf("move %d (%s) does not follow move %d (%s)", i, log[i].At, i-1, log[i-1].At)
		}
	}
	// And the gap between turns is visible — which is the entire reason for this column.
	gap := log[1].At.Sub(log[0].At.Add(300 * time.Millisecond))
	if gap < 1900*time.Millisecond {
		t.Errorf("inter-turn gap = %s, want ~2s; the waterfall's whole value is showing "+
			"time the agent was NOT working", gap)
	}
}
