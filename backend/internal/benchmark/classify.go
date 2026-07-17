package benchmark

import (
	"context"
	"errors"
	"strings"
)

// AgentMeta is the manifest-declared metadata attached to a benchmark fact
// server-side: version (version-diff) + model provider/model (provider intel).
type AgentMeta struct {
	Version  string
	Provider string
	Model    string
}

// AgentMetaResolver resolves an agent's manifest metadata (zero-value if unknown).
// Injected into the drive loops so the benchmark fact carries trusted, server-
// side agent metadata, without the benchmark package importing the manifest svc.
type AgentMetaResolver func(ctx context.Context, agentID string) AgentMeta

// ResultFromLabel maps a game's winner label (from that seat's perspective) to a
// Result: win = {you, win, won}; loss = {opponent, loss, lost}; draw = {tie,
// draw}. Anything else → "" (unknown, not counted). Shared by the goofspiel loops.
func ResultFromLabel(label string) Result {
	switch label {
	case "you", "win", "won":
		return ResultWin
	case "opponent", "loss", "lost":
		return ResultLoss
	case "tie", "draw":
		return ResultDraw
	default:
		return ""
	}
}

// ClassifyError maps a decision transport error to an Outcome, shared by every
// drive loop so the taxonomy is consistent across games. notConnected lets the
// caller signal a disconnect (this package does not import agentgw); callers pass
// errors.Is(err, agentgw.ErrNotConnected) where they have it, else false.
func ClassifyError(err error, notConnected bool) Outcome {
	switch {
	case err == nil:
		return OutcomeOK
	case notConnected:
		return OutcomeDisconnected
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimeout
	case strings.Contains(err.Error(), "agent error"):
		return OutcomeError
	default:
		return OutcomeTransportError
	}
}
