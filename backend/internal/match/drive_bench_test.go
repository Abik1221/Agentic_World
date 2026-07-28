package match

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/agentgw"
	"github.com/agent-arena/arena/internal/benchmark"
)

// fakeMover satisfies the driver's mover interface. turnFn decides what each
// Turn call does (write a move / return an error), so we can drive every
// classification branch of decide() without a real socket.
type fakeMover struct {
	turnFn func(out any) error
}

func (f fakeMover) Connected(string) bool { return true }
func (f fakeMover) Turn(_ context.Context, _ string, _, out any) error {
	return f.turnFn(out)
}
func (fakeMover) GameEnd(context.Context, string, string, string, json.RawMessage) error { return nil }

func newDriver(fn func(out any) error) *driver {
	return &driver{gw: fakeMover{turnFn: fn}, log: slog.Default()}
}

func viewWithHand(hand []int) AgentView {
	return AgentView{
		Round: 1, CurrentPrize: 5, PrizePool: 5,
		You:          sideView{Hand: hand, Score: 0},
		Opponent:     oppView{Score: 0},
		LegalActions: legalView{PlayCardFrom: hand},
	}
}

func writeMove(card int) func(out any) error {
	return func(out any) error {
		// out is *goofspielTurnMove
		if m, ok := out.(*goofspielTurnMove); ok {
			m.Card = card
		}
		return nil
	}
}

func TestDecide_ClassifiesOutcomes(t *testing.T) {
	hand := []int{2, 4, 6}

	cases := []struct {
		name     string
		fn       func(out any) error
		wantCard int
		wantOut  benchmark.Outcome
	}{
		{"legal", writeMove(6), 6, benchmark.OutcomeOK},
		{"illegal falls back to lowest", writeMove(99), 2, benchmark.OutcomeIllegal},
		{"disconnected", func(any) error { return agentgw.ErrNotConnected }, 2, benchmark.OutcomeDisconnected},
		{"timeout", func(any) error { return context.DeadlineExceeded }, 2, benchmark.OutcomeTimeout},
		{"agent error", func(any) error { return errors.New("agentgw: agent error: boom") }, 2, benchmark.OutcomeError},
		{"transport", func(any) error { return errors.New("write: broken pipe") }, 2, benchmark.OutcomeTransportError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDriver(tc.fn)
			card, out, latency, _, _ := d.decide(context.Background(), socketSeat{gw: d.gw, id: "ag1", matchID: "m1"}, 0, "m1", viewWithHand(hand))
			if card != tc.wantCard {
				t.Errorf("card=%d want %d", card, tc.wantCard)
			}
			if out != tc.wantOut {
				t.Errorf("outcome=%s want %s", out, tc.wantOut)
			}
			if latency < 0 {
				t.Errorf("latency must be >= 0, got %d", latency)
			}
		})
	}
}

func TestFlushBenchmark_DurablePathPersistsPayload(t *testing.T) {
	var gotType string
	var gotPayload []byte
	d := &driver{
		log: slog.Default(),
		persist: func(_ context.Context, eventType string, payload []byte) error {
			gotType = eventType
			gotPayload = payload
			return nil
		},
	}
	rec := benchmark.NewRecorder("goofspiel", "m5")
	rec.Record(benchmark.Decision{Seat: 0, AgentID: "ag0", Outcome: benchmark.OutcomeOK, LatencyMS: 12})
	rec.Record(benchmark.Decision{Seat: 1, AgentID: "ag1", Outcome: benchmark.OutcomeTimeout, LatencyMS: 9000})

	d.flushBenchmark(rec)

	if gotType != "match.benchmark" {
		t.Fatalf("event type = %q, want match.benchmark", gotType)
	}
	var dto struct {
		Game    string `json:"game"`
		MatchID string `json:"match_id"`
		Mode    string `json:"mode"`
		Seats   []struct {
			AgentID   string `json:"agent_id"`
			Fallbacks int64  `json:"fallbacks"`
		} `json:"seats"`
	}
	if err := json.Unmarshal(gotPayload, &dto); err != nil {
		t.Fatalf("payload not valid json: %v", err)
	}
	if dto.MatchID != "m5" || dto.Game != "goofspiel" || dto.Mode != "ranked" {
		t.Errorf("payload header wrong: %+v", dto)
	}
	if len(dto.Seats) != 2 || dto.Seats[1].Fallbacks != 1 {
		t.Errorf("payload seats wrong: %+v", dto.Seats)
	}
}

func TestFlushBenchmark_EmptyRecorderNoOp(t *testing.T) {
	called := false
	d := &driver{log: slog.Default(), persist: func(context.Context, string, []byte) error { called = true; return nil }}
	d.flushBenchmark(benchmark.NewRecorder("goofspiel", "m6"))
	if called {
		t.Error("empty recorder must not persist a benchmark fact")
	}
}

// A recorder fed the classified decisions produces the fallback/legal split a
// benchmark would report — the end-to-end shape of the drive integration.
func TestDecide_FeedsRecorder(t *testing.T) {
	hand := []int{1, 3, 5}
	rec := benchmark.NewRecorder("goofspiel", "m1")

	legal := newDriver(writeMove(5))
	timeout := newDriver(func(any) error { return context.DeadlineExceeded })

	for i := 0; i < 3; i++ {
		_, out, ms, _, _ := legal.decide(context.Background(), socketSeat{gw: legal.gw, id: "ag0", matchID: "m1"}, 0, "m1", viewWithHand(hand))
		rec.Record(benchmark.Decision{Seat: 0, AgentID: "ag0", Outcome: out, LatencyMS: ms})
	}
	_, out, ms, _, _ := timeout.decide(context.Background(), socketSeat{gw: timeout.gw, id: "ag0", matchID: "m1"}, 0, "m1", viewWithHand(hand))
	rec.Record(benchmark.Decision{Seat: 0, AgentID: "ag0", Outcome: out, LatencyMS: ms})

	s := rec.Summary().Seats[0]
	if s.Legal != 3 || s.Timeouts != 1 || s.Fallbacks != 1 || s.Decisions != 4 {
		t.Fatalf("unexpected summary: %+v", s)
	}
	if s.FallbackRate() != 0.25 {
		t.Errorf("fallback_rate=%v want 0.25", s.FallbackRate())
	}
}
