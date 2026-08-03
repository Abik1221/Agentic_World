package devtrace

import (
	"context"
	"encoding/json"
	"sort"
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

// localActivity assembles trace entries from the arena's own tables.
//
// Rounds are folded rather than reported one row per event: a developer wants "round 7,
// played the 9, took 1.2s, won the 11-point prize" as ONE line, not four rows they have to
// join by eye. That folding is the whole reason this returns a shaped timeline instead of a
// table dump.
func (s *Service) localActivity(ctx context.Context, agentIDs []string, since time.Time, limit int) ([]Entry, error) {
	rows, err := s.local.MatchActivity(ctx, agentIDs, since, limit)
	if err != nil {
		return nil, err
	}

	// Per (match, agent, round): when the prize was revealed (the round's start) and when
	// this agent sealed its card. The difference is its thinking time.
	type roundKey struct {
		match string
		agent string
		round int
	}
	prizeAt := map[roundKey]time.Time{}
	sealedAt := map[roundKey]time.Time{}
	out := make([]Entry, 0, len(rows))

	// First pass: timings. Done separately because a round's prize is revealed before the
	// card is sealed but a reveal arrives for the whole table, so the pairing cannot be
	// made row-by-row in one sweep.
	for _, r := range rows {
		switch r.Type {
		case "prize_revealed":
			var p struct{ Round int }
			if json.Unmarshal(r.Payload, &p) == nil {
				k := roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}
				// Keep the FIRST reveal for a round. A retry or a duplicate row must not
				// move the start of the clock and make a decision look instant.
				if _, seen := prizeAt[k]; !seen {
					prizeAt[k] = r.CreatedAt
				}
			}
		case "card_sealed":
			var p struct {
				Round int
				Seat  int
			}
			if json.Unmarshal(r.Payload, &p) == nil && p.Seat == r.Seat {
				sealedAt[roundKey{r.MatchPublicID, r.AgentPublicID, p.Round}] = r.CreatedAt
			}
		}
	}

	latency := func(k roundKey) int64 {
		start, ok := prizeAt[k]
		if !ok {
			return 0
		}
		end, ok := sealedAt[k]
		if !ok {
			return 0
		}
		ms := end.Sub(start).Milliseconds()
		// A negative or absurd gap means the two rows are not the pair we think they are;
		// reporting it would put a fictional number on a latency chart.
		if ms < 0 || ms > int64(6*time.Hour/time.Millisecond) {
			return 0
		}
		return ms
	}

	for _, r := range rows {
		base := Entry{
			At:      r.CreatedAt,
			Status:  "ok",
			Game:    r.Game,
			MatchID: r.MatchPublicID,
			AgentID: r.AgentPublicID,
		}
		switch r.Type {
		case "round_revealed":
			// The decision WITH its value. `card_sealed` deliberately carries no card (a
			// spectator must not learn a move early), so the reveal is where a developer
			// finally sees what their agent actually played.
			var p struct {
				Round  int    `json:"round"`
				Prize  int    `json:"prize"`
				Cards  [2]int `json:"cards"`
				Winner int    `json:"winner"`
			}
			if json.Unmarshal(r.Payload, &p) != nil || r.Seat < 0 || r.Seat > 1 {
				continue
			}
			e := base
			e.Type = "agent_decision"
			e.Operation = "bid"
			e.LatencyMS = latency(roundKey{r.MatchPublicID, r.AgentPublicID, p.Round})
			e.Detail = map[string]any{
				"round":  p.Round,
				"action": "played " + itoa(p.Cards[r.Seat]),
				"prize":  p.Prize,
				"won":    p.Winner == r.Seat,
			}
			out = append(out, e)

		case "agent_says":
			var p struct {
				Round int    `json:"round"`
				Seat  int    `json:"seat"`
				Text  string `json:"text"`
				Kind  string `json:"kind"`
			}
			if json.Unmarshal(r.Payload, &p) != nil || p.Seat != r.Seat {
				continue
			}
			e := base
			e.Type = "agent_said"
			e.Detail = map[string]any{"round": p.Round}
			// A rationale is the agent explaining its own move; table talk is what it said
			// to the opponent. The client renders them differently, so they stay distinct.
			if p.Kind == "rationale" {
				e.Detail["rationale"] = p.Text
			} else {
				e.Detail["text"] = p.Text
			}
			out = append(out, e)

		case "match_finished":
			var p struct {
				Scores [2]int `json:"scores"`
				Winner int    `json:"winner"`
			}
			if json.Unmarshal(r.Payload, &p) != nil {
				continue
			}
			e := base
			e.Type = "match_finished"
			e.Detail = map[string]any{"won": p.Winner == r.Seat}
			if r.Seat >= 0 && r.Seat <= 1 {
				e.Detail["score"] = p.Scores[r.Seat]
			}
			out = append(out, e)

		case "match_created":
			e := base
			e.Type = "match_started"
			out = append(out, e)
		}
	}

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
					Detail:  map[string]any{"name": reg.Name},
				})
			}
		}
	}

	// Newest first, matching what the Lens returns and what the client expects before it
	// regroups into per-match timelines.
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// itoa avoids pulling strconv in for one call and keeps the detail map values as strings
// where the client expects strings.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
