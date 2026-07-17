package benchmark

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestOutcome_Fallback(t *testing.T) {
	if OutcomeOK.Fallback() {
		t.Error("OK must not be a fallback")
	}
	for _, o := range []Outcome{OutcomeIllegal, OutcomeTimeout, OutcomeTransportError, OutcomeDisconnected, OutcomeError} {
		if !o.Fallback() {
			t.Errorf("%s must be a fallback", o)
		}
	}
}

func TestRecorder_AggregatesPerSeat(t *testing.T) {
	r := NewRecorder("goofspiel", "m1")
	// Seat 0: 3 legal + 1 illegal, latencies 10/20/30/40.
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, LatencyMS: 10})
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, LatencyMS: 20})
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeOK, LatencyMS: 30})
	r.Record(Decision{Seat: 0, AgentID: "ag0", Outcome: OutcomeIllegal, LatencyMS: 40})
	// Seat 1: 1 legal + timeout + disconnect + transport_error.
	r.Record(Decision{Seat: 1, AgentID: "ag1", Outcome: OutcomeOK, LatencyMS: 5})
	r.Record(Decision{Seat: 1, AgentID: "ag1", Outcome: OutcomeTimeout, LatencyMS: 10000})
	r.Record(Decision{Seat: 1, AgentID: "ag1", Outcome: OutcomeDisconnected, LatencyMS: 0})
	r.Record(Decision{Seat: 1, AgentID: "ag1", Outcome: OutcomeTransportError, LatencyMS: 1})

	sum := r.Summary()
	if sum.Game != "goofspiel" || sum.MatchID != "m1" {
		t.Fatalf("summary header wrong: %+v", sum)
	}
	if len(sum.Seats) != 2 {
		t.Fatalf("want 2 seats, got %d", len(sum.Seats))
	}

	s0 := sum.Seats[0]
	if s0.Seat != 0 || s0.AgentID != "ag0" {
		t.Errorf("seat0 identity wrong: %+v", s0)
	}
	if s0.Decisions != 4 || s0.Legal != 3 || s0.Illegal != 1 || s0.Fallbacks != 1 {
		t.Errorf("seat0 counts wrong: %+v", s0)
	}
	if !approx(s0.LegalRate(), 0.75) || !approx(s0.FallbackRate(), 0.25) {
		t.Errorf("seat0 rates: legal=%v fallback=%v", s0.LegalRate(), s0.FallbackRate())
	}
	if !approx(s0.AvgLatencyMS(), 25) || s0.LatencyMinMS != 10 || s0.LatencyMaxMS != 40 {
		t.Errorf("seat0 latency: avg=%v min=%d max=%d", s0.AvgLatencyMS(), s0.LatencyMinMS, s0.LatencyMaxMS)
	}

	s1 := sum.Seats[1]
	if s1.Decisions != 4 || s1.Legal != 1 || s1.Timeouts != 1 || s1.Disconnects != 1 || s1.TransportErrors != 1 {
		t.Errorf("seat1 counts wrong: %+v", s1)
	}
	if s1.Fallbacks != 3 {
		t.Errorf("seat1 fallbacks = %d, want 3", s1.Fallbacks)
	}
	if !approx(s1.LegalRate(), 0.25) {
		t.Errorf("seat1 legal rate = %v, want 0.25", s1.LegalRate())
	}
	if s1.LatencyMinMS != 0 || s1.LatencyMaxMS != 10000 {
		t.Errorf("seat1 latency min/max: %d/%d", s1.LatencyMinMS, s1.LatencyMaxMS)
	}
}

func TestRecorder_StableSeatOrderByFirstSeen(t *testing.T) {
	r := NewRecorder("mafia", "m2")
	r.Record(Decision{Seat: 3, AgentID: "c"})
	r.Record(Decision{Seat: 1, AgentID: "a"})
	r.Record(Decision{Seat: 3, AgentID: "c"})
	r.Record(Decision{Seat: 2, AgentID: "b"})
	got := r.Summary().Seats
	want := []int{3, 1, 2}
	for i, w := range want {
		if got[i].Seat != w {
			t.Errorf("seat order[%d]=%d want %d", i, got[i].Seat, w)
		}
	}
}

func TestRecorder_EmptyAndZeroRates(t *testing.T) {
	r := NewRecorder("goofspiel", "m3")
	if !r.Empty() {
		t.Error("new recorder must be empty")
	}
	var s SeatSummary
	if s.LegalRate() != 0 || s.FallbackRate() != 0 || s.AvgLatencyMS() != 0 {
		t.Error("empty seat rates must be zero, not NaN")
	}
	r.Record(Decision{Seat: 0, AgentID: "x", Outcome: OutcomeOK, LatencyMS: 7})
	if r.Empty() {
		t.Error("recorder with a decision must not be empty")
	}
}

func TestRecorder_NilSafe(t *testing.T) {
	var r *Recorder
	r.Record(Decision{Seat: 0, Outcome: OutcomeOK}) // must not panic
	r.SetResult(0, "a", ResultWin)                  // must not panic
}

func TestRecorder_SetResult(t *testing.T) {
	r := NewRecorder("goofspiel", "m1")
	r.Record(Decision{Seat: 0, AgentID: "a", Outcome: OutcomeOK, LatencyMS: 1})
	r.SetResult(0, "a", ResultWin)
	// A seat that never decided still gets counted (created) so its loss is scored.
	r.SetResult(1, "b", ResultLoss)

	seats := r.Summary().Seats
	if len(seats) != 2 {
		t.Fatalf("want 2 seats, got %d", len(seats))
	}
	if seats[0].Result != ResultWin || seats[0].AgentID != "a" {
		t.Errorf("seat0 = %+v", seats[0])
	}
	if seats[1].Result != ResultLoss || seats[1].AgentID != "b" || seats[1].Decisions != 0 {
		t.Errorf("seat1 = %+v", seats[1])
	}
}
