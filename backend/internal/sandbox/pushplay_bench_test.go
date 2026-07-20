package sandbox

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/benchmark"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/remoteplay"
)

type fakeTransport struct{ turn func(out any) error }

func (fakeTransport) Initialize(context.Context, agentclient.InitializeRequest) error { return nil }
func (f fakeTransport) Turn(_ context.Context, _, out any) error                      { return f.turn(out) }
func (fakeTransport) Event(context.Context, string, int, string, []byte) error        { return nil }
func (fakeTransport) GameEnd(context.Context, string, []byte) error                   { return nil }
func (fakeTransport) Socket() bool                                                    { return false }

func writeCard(card int) func(out any) error {
	return func(out any) error {
		if m, ok := out.(*remoteplay.GoofspielMove); ok {
			m.Card = card
		}
		return nil
	}
}

func TestSandboxDecide_Classifies(t *testing.T) {
	p := &pushPlayer{log: slog.Default()}
	v := match.AgentView{Round: 1}
	legal := []int{2, 4, 6}

	cases := []struct {
		name     string
		turn     func(out any) error
		wantCard int
		want     benchmark.Outcome
	}{
		{"legal", writeCard(6), 6, benchmark.OutcomeOK},
		{"illegal falls back to lowest", writeCard(99), 2, benchmark.OutcomeIllegal},
		{"timeout", func(any) error { return context.DeadlineExceeded }, 2, benchmark.OutcomeTimeout},
		{"transport", func(any) error { return errors.New("eof") }, 2, benchmark.OutcomeTransportError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card, out, latency, _, _ := p.decide(context.Background(), fakeTransport{turn: tc.turn}, "m1", v, legal)
			if card != tc.wantCard {
				t.Errorf("card=%d want %d", card, tc.wantCard)
			}
			if out != tc.want {
				t.Errorf("outcome=%s want %s", out, tc.want)
			}
			if latency < 0 {
				t.Errorf("latency %d < 0", latency)
			}
		})
	}
}
