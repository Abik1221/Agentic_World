package benchmark

import (
	"context"
	"errors"
	"testing"
)

func TestResultFromLabel(t *testing.T) {
	cases := map[string]Result{
		"you": ResultWin, "win": ResultWin, "won": ResultWin,
		"opponent": ResultLoss, "loss": ResultLoss, "lost": ResultLoss,
		"tie": ResultDraw, "draw": ResultDraw,
		"": "", "weird": "",
	}
	for label, want := range cases {
		if got := ResultFromLabel(label); got != want {
			t.Errorf("ResultFromLabel(%q)=%q want %q", label, got, want)
		}
	}
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name         string
		err          error
		notConnected bool
		want         Outcome
	}{
		{"nil is ok", nil, false, OutcomeOK},
		{"not connected", errors.New("whatever"), true, OutcomeDisconnected},
		{"deadline is timeout", context.DeadlineExceeded, false, OutcomeTimeout},
		{"wrapped deadline is timeout", errors.Join(errors.New("x"), context.DeadlineExceeded), false, OutcomeTimeout},
		{"agent error frame", errors.New("agentgw: agent error: boom"), false, OutcomeError},
		{"other is transport", errors.New("broken pipe"), false, OutcomeTransportError},
		{"notConnected wins over deadline", context.DeadlineExceeded, true, OutcomeDisconnected},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyError(tc.err, tc.notConnected); got != tc.want {
				t.Errorf("ClassifyError(%v, %v) = %s, want %s", tc.err, tc.notConnected, got, tc.want)
			}
		})
	}
}
