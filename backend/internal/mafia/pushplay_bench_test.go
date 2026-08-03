package mafia

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

func writeMafiaMove(action string) func(out any) error {
	return func(out any) error {
		if m, ok := out.(*MafiaPushMove); ok {
			m.Action = action
		}
		return nil
	}
}

func TestMafiaDecideRemote_Classifies(t *testing.T) {
	p := &pushPlayer{log: slog.Default()}
	v := AgentView{YourSeat: 3, Phase: "day", Legal: []string{"vote", "speak"}}

	cases := []struct {
		name string
		turn func(out any) error
		want benchmark.Outcome
	}{
		{"legal", writeMafiaMove("vote"), benchmark.OutcomeOK},
		{"illegal", writeMafiaMove("kill"), benchmark.OutcomeIllegal},
		{"timeout", func(any) error { return context.DeadlineExceeded }, benchmark.OutcomeTimeout},
		{"transport", func(any) error { return errors.New("eof") }, benchmark.OutcomeTransportError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			act, out, latency, _, _ := p.decideRemote(context.Background(), fakeTransport{turn: tc.turn}, "m1", "ag_1", v)
			if out != tc.want {
				t.Errorf("outcome=%s want %s", out, tc.want)
			}
			if latency < 0 {
				t.Errorf("latency %d < 0", latency)
			}
			// On OK the chosen action is the agent's; on fallback it's botDecide's
			// deterministic legal move — either way it must be non-empty.
			if act.Kind == "" {
				t.Errorf("empty action kind for outcome %s", out)
			}
		})
	}
}
