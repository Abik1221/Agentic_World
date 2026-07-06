package agentgw

import (
	"context"

	"github.com/agent-arena/arena/internal/remoteplay"
)

// GoofspielDecider drives one seat of a Goofspiel match from a socket-connected
// agent. It satisfies remoteplay.Decider, so remoteplay.PlayGoofspiel treats a
// local WSS agent identically to the historical HTTP push agent: the engine stays
// authoritative and an absent/slow/illegal agent falls back to a deterministic
// move inside PlayGoofspiel — the socket transport changes nothing about that
// guarantee.
type GoofspielDecider struct {
	GW      *Gateway
	AgentID string
	MatchID string
}

// Decide implements remoteplay.Decider by pushing the view over the socket and
// reading back the move.
func (d GoofspielDecider) Decide(ctx context.Context, view remoteplay.GoofspielView) (int, error) {
	view.MatchID = d.MatchID
	var move remoteplay.GoofspielMove
	if err := d.GW.Turn(ctx, d.AgentID, view, &move); err != nil {
		return 0, err
	}
	return move.Card, nil
}
