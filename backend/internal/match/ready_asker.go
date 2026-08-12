package match

import (
	"context"
	"log/slog"
	"time"

	"github.com/agent-arena/arena/internal/agentclient"
)

// InitializeAsker asks a seat "are you there?" by sending the lifecycle call the protocol
// already has, and reads the acknowledgement the SDKs already send.
//
// # Why /initialize rather than a new call
//
// The push protocol has always documented /initialize as "match start — synchronous ack
// expected", InitializeResponse has always carried a Ready flag, and BOTH SDKs already answer
// it: the Python server returns {"ready": true, "display_name": …} and the JS one returns the
// same. The backend simply discarded the response — agentwire's transports do
// `_, err := Initialize(...)`.
//
// So the field was defined, populated by every agent in existence, and read by nobody. Using
// it here means the ready check works against agents that have already shipped, without asking
// a single developer to change a line. Inventing a second acknowledgement call would have made
// every existing agent fail a check its SDK was already answering correctly.
//
// # What counts as ready
//
// A 2xx with Ready=true. Anything else — transport failure, a non-2xx, or an explicit
// Ready=false — is "not yet", and the seat is asked again before it is dropped. That
// deliberately does NOT distinguish "unreachable" from "declined": both mean the seat is not
// playing right now, and a developer whose process is still booting deserves the same second
// chance as one whose agent said no.
type InitializeAsker struct {
	Resolver InitResolver
	Client   InitClient
	Sockets  SocketAsker // optional: agents connected over the live socket
	Log      *slog.Logger
}

// InitResolver resolves a verified hosted endpoint. Satisfied by manifest.Service, the same
// way the ranked driver resolves one.
type InitResolver interface {
	PlayTarget(ctx context.Context, agentPublicID string) (agentclient.Target, bool, error)
}

// InitClient sends the lifecycle call and RETURNS the response, which is the part the existing
// transports drop.
type InitClient interface {
	Initialize(ctx context.Context, t agentclient.Target, req agentclient.InitializeRequest) (agentclient.InitializeResponse, error)
}

// SocketAsker reaches an agent on the live WebSocket. Optional: without it, a socket-connected
// agent is asked over its hosted endpoint if it has one, and otherwise simply does not answer
// — which the state machine already handles by asking again and then dropping.
type SocketAsker interface {
	// req is `any` to match agentgw.Gateway, which frames whatever it is given. Typed
	// narrowly here would mean re-stating the gateway's signature and drifting from it.
	Initialize(ctx context.Context, agentPublicID string, req any) error
}

// AskReady implements ReadyAsker.
//
// Returns an error only to say "this seat did not acknowledge". The sweeper treats that as a
// non-event — it logs at debug and moves on — because an undeliverable ask and an unanswered
// one are the same fact, and one unreachable seat must never abort a sweep that is holding
// other people's tables.
func (a InitializeAsker) AskReady(ctx context.Context, agentPublicID, matchPublicID string, deadline time.Time) error {
	req := agentclient.InitializeRequest{
		Protocol: agentclient.ProtocolVersion,
		MatchID:  matchPublicID,
		Game:     "goofspiel",
		// The window, so an agent can decide whether it can be ready in time rather than
		// guessing. Milliseconds to match every other deadline on this protocol.
		DeadlineMs: time.Until(deadline).Milliseconds(),
		Players:    2,
	}

	// The socket first when the agent is connected: it is already open, so it is both faster
	// and the path that a locally-run `pyyol run` agent is actually on.
	if a.Sockets != nil {
		if err := a.Sockets.Initialize(ctx, agentPublicID, req); err == nil {
			// The socket gateway does not surface the ack payload, so a delivered frame is
			// taken as the acknowledgement. Defensible: the frame only succeeds when the
			// agent's runtime processed it, which is the same thing the HTTP ack proves.
			return nil
		}
	}

	if a.Resolver == nil || a.Client == nil {
		return errNotAcknowledged
	}
	target, ok, err := a.Resolver.PlayTarget(ctx, agentPublicID)
	if err != nil || !ok {
		return errNotAcknowledged
	}
	resp, err := a.Client.Initialize(ctx, target, req)
	if err != nil {
		return errNotAcknowledged
	}
	if !resp.Ready {
		// An explicit "not yet". Logged because it is the one case that is a real decision by
		// the agent rather than an accident of the network, and a developer looking at why
		// their seat was dropped should be able to see that their own code said no.
		if a.Log != nil {
			a.Log.Debug("ready check: the agent answered but is not ready",
				"agent", agentPublicID, "match", matchPublicID)
		}
		return errNotAcknowledged
	}
	return nil
}

type readyErr string

func (e readyErr) Error() string { return string(e) }

const errNotAcknowledged = readyErr("ready check: the seat did not acknowledge")
