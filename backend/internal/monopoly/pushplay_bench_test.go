package monopoly

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/benchmark"
)

// fakeTransport satisfies agentwire.Transport; turn controls each Turn call.
type fakeTransport struct{ turn func(out any) error }

func (fakeTransport) Initialize(context.Context, agentclient.InitializeRequest) error { return nil }
func (f fakeTransport) Turn(_ context.Context, _, out any) error                      { return f.turn(out) }
func (fakeTransport) Event(context.Context, string, int, string, []byte) error        { return nil }
func (fakeTransport) GameEnd(context.Context, string, []byte) error                   { return nil }
func (fakeTransport) Socket() bool                                                    { return false }

func writeMonoMove(action string) func(out any) error {
	return func(out any) error {
		if m, ok := out.(*MonopolyPushMove); ok {
			m.Action = action
		}
		return nil
	}
}

func TestMonopolyDecide_Classifies(t *testing.T) {
	p := &pushPlayer{log: slog.Default()}
	v := AgentView{YourSeat: 0, Phase: "main", Legal: []string{"roll", "end_turn"}}

	cases := []struct {
		name string
		turn func(out any) error
		want benchmark.Outcome
	}{
		{"legal", writeMonoMove("roll"), benchmark.OutcomeOK},
		{"illegal", writeMonoMove("build_hotel"), benchmark.OutcomeIllegal},
		{"timeout", func(any) error { return context.DeadlineExceeded }, benchmark.OutcomeTimeout},
		{"transport", func(any) error { return errors.New("connection refused") }, benchmark.OutcomeTransportError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			act, out, latency := p.decide(context.Background(), fakeTransport{turn: tc.turn}, "m1", v)
			if out != tc.want {
				t.Errorf("outcome=%s want %s", out, tc.want)
			}
			if latency < 0 {
				t.Errorf("latency %d < 0", latency)
			}
			// A fallback must still be a legal action so the match advances.
			if out.Fallback() && !containsStr(v.Legal, act.Kind) {
				t.Errorf("fallback action %q not legal", act.Kind)
			}
			if out == benchmark.OutcomeOK && act.Kind != "roll" {
				t.Errorf("ok action=%q want roll", act.Kind)
			}
		})
	}
}
