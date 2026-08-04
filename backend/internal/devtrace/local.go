package devtrace

import (
	"context"
	"encoding/json"
	"time"
)

// TRACES FROM THE ARENA'S OWN DATABASE.
//
// The Lens (see devtrace.go) is a separate service on a separate host with a separate
// secret, and /traces used to be a thin window onto it — so the page had exactly the
// availability of that service. Every way that could go wrong, it did: no read endpoint
// configured (the read variable had no fallback while the read key did), the query api
// unreachable from the arena's network, or reachable and refusing the arena's key. In each
// case a developer opened "Agent traces" and was told the trace store was unreachable.
//
// That is the wrong dependency for a first-class product surface. Everything the page needs
// to answer its actual question — "what did my agent do, and why was it slow" — is already
// in Postgres, written transactionally by the match engine:
//
//   - match_events is the append-only, gap-free log that IS the replay. It holds each
//     sealed card (the decision), each revealed round (what the card actually was and
//     whether it won), and every line of table talk including the rationale an agent
//     attached to a move.
//   - match_players maps seat → agent, which is what makes an event attributable.
//   - The event timestamps give a REAL per-decision latency: the gap between a round's
//     prize being revealed and the agent sealing its card is exactly how long the agent
//     took to think. That is a better measure than the sampled timings table, whose
//     match_id was never populated.
//
// So this is the primary source and the Lens is enrichment. The page works on a bare
// deployment with no telemetry stack at all, which is what it needs to be for beta.
//
// SCOPE IS UNCHANGED. Callers pass the agent ids the ownership gate already resolved, and
// only allowlisted event types are mapped. An opponent's decisions are never returned:
// events are filtered to the caller's own seat, and `card_sealed` carries no card value at
// all, so even the raw rows hold nothing about the other player's hidden move.

// LocalRepo reads trace-worthy facts out of the arena's own match tables.
type LocalRepo interface {
	// MatchActivity returns the raw rows for these agents' matches since `since`,
	// newest match first, bounded by limit. Each row is one match_event already
	// attributed to one of the caller's agents (or match-wide, e.g. a round reveal).
	MatchActivity(ctx context.Context, agentPublicIDs []string, since time.Time, limit int) ([]MatchRow, error)
	// AgentRegistrations returns when each of these agents was created, so a developer
	// with no matches yet sees something true rather than an empty page.
	AgentRegistrations(ctx context.Context, agentPublicIDs []string) ([]Registration, error)
}

// MatchRow is one match_event row, already joined to the match and the owning agent.
type MatchRow struct {
	MatchPublicID string
	Game          string
	AgentPublicID string
	// Seat is the agent's seat in this match — needed to pick its own card out of a
	// round reveal, which carries both players' cards.
	Seat      int
	Type      string
	Payload   json.RawMessage
	CreatedAt time.Time
}

// Registration is an agent's creation moment.
type Registration struct {
	AgentPublicID string
	Name          string
	CreatedAt     time.Time
}

// SetLocalRepo wires the Postgres-backed trace source. Without it the service falls back
// to Lens-only behaviour (which is how it used to work, and how it fails).
func (s *Service) SetLocalRepo(r LocalRepo) { s.local = r }

// localActivity assembles the flat "recent activity" feed from the arena's own tables.
//
// Rounds are folded rather than reported one row per event: a developer wants "round 7,
// played the 9, took 1.2s, won the 11-point prize" as ONE line, not four rows they have to
// join by eye. That folding — and the per-game payload mapping for all three games — lives
// in games.go, shared with the per-match detail endpoint so the two views can never
// describe the same match differently.
func (s *Service) localActivity(ctx context.Context, agentIDs []string, since time.Time, limit int) ([]Entry, error) {
	rows, err := s.local.MatchActivity(ctx, agentIDs, since, limit)
	if err != nil {
		return nil, err
	}

	// No per-match context here on purpose: this flat feed spans many matches, and
	// fetching a roster per match to name seats would be N+1 queries for a view whose
	// job is "what has my agent been doing lately". `Finished` stays false, so ONLY the
	// caller's own seat is admitted — the same scope this endpoint always had. The
	// per-match detail endpoint is where seats get names and public events get shown.
	out := mapRows(rows, nil)

	// "Your agent exists" is a fact worth showing when there is nothing else.
	//
	// A developer who has connected an agent but not played opens this page and sees an
	// empty timeline, which reads as "my agent is broken" — the exact misreading the empty
	// state was written to prevent. One registered line, dated, says the platform knows
	// about the agent and is waiting for it to play.
	if len(out) == 0 {
		if regs, rerr := s.local.AgentRegistrations(ctx, agentIDs); rerr == nil {
			for _, reg := range regs {
				if reg.CreatedAt.Before(since) {
					continue // outside the window the developer asked about
				}
				out = append(out, Entry{
					At:      reg.CreatedAt,
					Type:    "agent_registered",
					Status:  "ok",
					AgentID: reg.AgentPublicID,
					Detail:  map[string]any{"name": reg.Name, "summary": "Agent " + reg.Name + " registered"},
				})
			}
		}
	}

	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
