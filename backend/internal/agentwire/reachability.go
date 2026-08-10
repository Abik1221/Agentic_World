package agentwire

import (
	"context"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

// Reachability is the verdict on whether an agent was actually there when a decision
// timed out.
//
// Why this exists: absence forfeits real money. A seat the platform had to play for is
// judged absent at settlement, its stake goes to the opponent, and it is exempted from
// the integrity void. That is a defensible rule ONLY if the platform genuinely tried to
// reach the agent before concluding it was gone — otherwise "your model was 200ms slower
// than our deadline" and "your process was dead for the whole match" take a developer's
// coins in exactly the same way, and the platform has no evidence to show for it.
//
// The probe is /health: unauthenticated, no game state, and — the point — NO INFERENCE.
// It costs the developer nothing, which is why it is safe to do here when retrying the
// turn itself is not. A retried turn is duplicate inference the developer pays for; a
// health check is a TCP round trip.
type Reachability string

const (
	// ReachUnknown — no probe was performed (not configured, or a socket-driven seat
	// where the gateway already knows the connection state). Never treat as evidence.
	ReachUnknown Reachability = ""
	// ReachGone — the endpoint did not answer /health either. The agent is genuinely
	// unreachable: crashed, undeployed, or firewalled. This is real absence.
	ReachGone Reachability = "gone"
	// ReachAlive — /health answered. The process is UP and simply did not produce a move
	// in time: an overloaded model, a hung provider call, a bug in the turn handler.
	//
	// Worth keeping distinct even though both currently forfeit the round. "You were
	// down" and "you were up but too slow" are different bugs, the developer can only
	// fix the one they are told about, and if the forfeit rule is ever disputed this is
	// the difference between the two cases.
	ReachAlive Reachability = "alive"
)

// Prober is the slice of agentclient the check needs. An interface so the push loops
// stay unit-testable without a live endpoint.
type Prober interface {
	Health(ctx context.Context, t agentclient.Target) (agentclient.HealthResult, error)
}

// ConfirmReachability probes an endpoint after its turn failed to land.
//
// Deliberately bounded and best-effort: it runs on a fresh short-lived context so a
// hanging endpoint cannot extend the turn it already lost, and any error is an answer
// (ReachGone) rather than a failure to propagate. Classification must never be able to
// break a match.
//
// Returns ReachUnknown when there is nothing to probe, so callers can tell "we did not
// look" from "we looked and it was down" — recording the first as the second would be
// manufacturing evidence.
func ConfirmReachability(ctx context.Context, p Prober, t agentclient.Target, timeout time.Duration, log *slog.Logger) Reachability {
	if p == nil || t.EndpointURL == "" {
		return ReachUnknown
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	// context.WithoutCancel: the turn's context is usually already past its deadline by
	// the time we get here — that is why we are here — so inheriting it would cancel the
	// probe before it was sent and report every slow agent as gone.
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()

	res, err := p.Health(probeCtx, t)
	if err != nil || res.Status < 200 || res.Status > 299 {
		if log != nil {
			log.Info("agent did not answer /health after a missed turn — recording it as unreachable",
				"endpoint", t.EndpointURL, "status", res.Status, "error", errText(err))
		}
		return ReachGone
	}
	if log != nil {
		log.Info("agent answered /health after a missed turn — it is UP but did not decide in time",
			"endpoint", t.EndpointURL, "health_latency_ms", res.LatencyMs)
	}
	return ReachAlive
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
