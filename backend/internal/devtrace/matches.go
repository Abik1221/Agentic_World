package devtrace

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
)

// THE DEVELOPER'S GAME HISTORY — one row per (match, their agent), paginated.
//
// The traces page used to be a single flat request: 300 events over 7 days, no total, no
// cursor. That has one failure mode and it is the one that matters, because a busy agent
// makes tens of events per match: a developer with a hundred matches got the newest three
// or four and no indication that anything had been cut. The page said "100+ games" from
// one counter and then showed a handful of timelines, which reads as data loss.
//
// So the list is now a LIST — bounded, counted, offset-addressable — and the timeline
// moved behind it, per match. Three consequences worth keeping:
//
//   - `Total` is the count of MATCHES matching the filter, not of events, and it is the
//     same number the page paginates over. A count that does not match what the list can
//     reach is how the old page lied.
//   - Sandbox and competitive are separate questions, not a mixed feed with a badge:
//     practice is where you expect failures and real money is where you cannot afford
//     them, so `Mode` is a filter with its own totals.
//   - Every row carries the FUEL — coins moved, tokens burned, verified USD, fallbacks —
//     because "what did this game cost me" is not answerable by looking at a timeline.
//     That data was already recorded per (match, agent) and was reachable only through a
//     one-match-at-a-time endpoint that nothing called.

// MatchMode filters the history. Empty means every mode.
type MatchMode string

// The three filters. The stored engine value is "competitive"; the product's word for it
// is "ranked", and ValidMatchMode accepts both so the UI's vocabulary and the database's
// do not have to be the same string.
const (
	ModeAny         MatchMode = ""
	ModeSandbox     MatchMode = "sandbox"
	ModeCompetitive MatchMode = "competitive"
)

// ValidMatchMode maps a query value to a filter, rejecting anything else rather than
// silently widening to "all" — a typo'd mode returning real-money matches under a
// sandbox heading would be the worst possible failure of this filter.
func ValidMatchMode(v string) (MatchMode, bool) {
	switch MatchMode(v) {
	case ModeAny, ModeSandbox, ModeCompetitive:
		return MatchMode(v), true
	// The client's tab labels, accepted as synonyms so the UI's vocabulary and the
	// engine's stored value do not have to be the same word.
	case "ranked", "real":
		return ModeCompetitive, true
	case "all":
		return ModeAny, true
	}
	return ModeAny, false
}

// MatchSummary is one of the caller's matches, from their agent's point of view.
type MatchSummary struct {
	MatchID string `json:"match_id"`
	Game    string `json:"game"`
	Mode    string `json:"mode"`   // sandbox | competitive
	Status  string `json:"status"` // waiting | active | finished | aborted

	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	Seat      int    `json:"seat"`
	// Players is the seat count for this match — 2 for Goofspiel, up to 12 for Mafia.
	// Shown because a result is meaningless without it: 3rd of 12 is not 3rd of 4.
	Players int `json:"players"`

	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`

	// Result is the engine's own verdict for THIS seat ("win"|"loss"|"draw"), or empty
	// when the match has not been settled. Never derived from a score comparison here:
	// team games (Mafia) and net-worth games (Monopoly) do not agree on what a higher
	// number means, and the engine already recorded the answer.
	Result string `json:"result"`
	Score  *int   `json:"score,omitempty"`

	// ── Money: coins. What the match did to the agent's balance.
	Stake      int64  `json:"stake"`
	CoinsDelta *int64 `json:"coins_delta,omitempty"`

	// ── Fuel: inference. What the agent spent to play it.
	Decisions int `json:"decisions"`
	Legal     int `json:"legal"`
	// Fallbacks are moves the ENGINE played because the agent was late, illegal or
	// unreachable. Surfaced on every row because it is the number that quietly ruins a
	// win rate and appears nowhere else in the product.
	Fallbacks      int     `json:"fallbacks"`
	AvgLatencyMS   int64   `json:"avg_latency_ms"`
	Tokens         int64   `json:"tokens"`
	SelfReported   float64 `json:"self_reported_cost_usd"`
	VerifiedCost   float64 `json:"verified_cost_usd"`
	VerifiedCalls  int     `json:"verified_calls"`
	BoundDecisions int     `json:"bound_decisions"`
}

// RosterSeat is one participant in a match, as the caller is allowed to see them.
type RosterSeat struct {
	Seat      int    `json:"seat"`
	AgentID   string `json:"agent_id"`
	AgentName string `json:"agent_name"`
	// Mine marks the caller's own agent. The client leans on this heavily: a twelve-seat
	// Mafia roster is unreadable until you can find yourself in it.
	Mine bool `json:"mine"`
	// House marks a platform-run opponent, so a developer can tell practice from a real
	// opponent without cross-referencing the leaderboard.
	House      bool   `json:"house"`
	FinalScore *int   `json:"final_score,omitempty"`
	CoinsDelta *int64 `json:"coins_delta,omitempty"`
}

// MatchDetail is everything the caller may see about one of their matches.
type MatchDetail struct {
	Match  MatchSummary `json:"match"`
	Roster []RosterSeat `json:"roster"`
	// Entries is the full timeline for this match, newest first, already rendered into
	// sentences by games.go. Other seats' public events are included only once the match
	// has finished — see the note in games.go.
	Entries []Entry `json:"entries"`
	// Decisions is the caller's own per-move record: what it chose, why, how long it
	// took and what the call cost. ALWAYS the caller's agent only — a rationale is
	// private reasoning about opponents, and it must never appear in another
	// developer's trace even after the match ends.
	Decisions []Decision `json:"decisions"`
}

// Decision is one recorded move by the caller's agent.
//
// This is what the trace was missing. The event log says what HAPPENED in the match;
// this says what the agent DID and why — the only view from which a developer can
// change anything.
type Decision struct {
	Seq       int    `json:"seq"`
	Round     int    `json:"round"`
	Action    string `json:"action,omitempty"`
	Outcome   string `json:"outcome,omitempty"` // ok|illegal|timeout|transport_error|…
	LatencyMS int64  `json:"latency_ms"`
	// Rationale is the agent's own explanation of the move, verbatim. Empty when the
	// agent returned none — most do not, and the UI says so rather than implying the
	// move was unreasoned.
	Rationale string `json:"rationale,omitempty"`

	Provider string `json:"provider,omitempty"`
	Model    string `json:"model,omitempty"`

	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	CachedTokens     int     `json:"cached_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`

	// SkillRegret is the share of the achievable value this decision gave up, in [0,1]:
	// 0 played the best action available from this exact state, 1 played the worst. Nil
	// when the decision has not been scored — an arena with no scorer yet, or a decision
	// recorded before input capture shipped.
	//
	// A POINTER, not a float, and that distinction is the whole contract: 0 means "you
	// found the best move" and nil means "we did not score this". Collapsing them would
	// tell a developer their unscored Monopoly match was played perfectly.
	SkillRegret *float64 `json:"skill_regret,omitempty"`
	// SkillBest is the action that was best, in the game's own vocabulary. Paired with
	// the action actually taken this is the single most actionable line the platform can
	// show: "you bid 6, the best bid was 12" beats any aggregate.
	SkillBest string `json:"skill_best,omitempty"`

	// Input is the turn view the agent was handed for this decision, passed through as
	// raw JSON so the arena's own shape reaches the client unflattened. Absent when
	// there was none to keep.
	//
	// This is the seat's HIDDEN information (its hand, its Mafia role) and is strictly
	// more sensitive than the rationale beside it — the read path is owner-scoped at the
	// query for exactly that reason.
	Input json.RawMessage `json:"input,omitempty"`
	// InputTruncated marks a view that existed but was dropped for size, so the client
	// can say "not kept" rather than rendering the same empty pane it shows for "the
	// agent was handed nothing".
	InputTruncated bool `json:"input_truncated,omitempty"`

	// StartedAt is when the engine asked for this move. nil for matches recorded before
	// the platform stamped it — the client then draws no timeline rather than inventing
	// offsets from cumulative latency, which would fabricate exactly the gaps a timeline
	// exists to reveal.
	StartedAt *time.Time `json:"started_at,omitempty"`
}

// MatchRepo is the history read port. Separate from LocalRepo because these are
// aggregate queries over matches, not a scan of the event log.
type MatchRepo interface {
	// Matches returns one row per (match, agent) for these agents, newest first, plus
	// the TOTAL number matching the filter (not the number returned) so a client can
	// paginate honestly.
	Matches(ctx context.Context, agentPublicIDs []string, mode MatchMode, limit, offset int) ([]MatchSummary, int, error)
	// MatchSummaryFor returns one match as seen by whichever of these agents played in
	// it, or ErrNoMatch when none of them did.
	MatchSummaryFor(ctx context.Context, agentPublicIDs []string, matchPublicID string) (MatchSummary, error)
	// Roster returns every seat in the match. Callers must already have established that
	// the caller played in it.
	Roster(ctx context.Context, matchPublicID string, ownedAgentIDs []string) ([]RosterSeat, error)
	// MatchEvents returns the allowlisted log rows for one match, attributed to the
	// caller's seat.
	MatchEvents(ctx context.Context, agentPublicIDs []string, matchPublicID string, limit int) ([]MatchRow, error)
	// MatchDecisions returns the per-move record for the caller's OWN agents in one
	// match, in order. Scoped to the passed agent ids at the query — a rationale is
	// private reasoning and is never returned for a seat the caller does not own.
	MatchDecisions(ctx context.Context, agentPublicIDs []string, matchPublicID string, limit int) ([]Decision, error)
}

// SetMatchRepo wires the history source. Without it the endpoints below report
// unconfigured rather than returning an empty history, which would read as "you have
// never played".
func (s *Service) SetMatchRepo(r MatchRepo) { s.matches = r }

// ErrNoMatch is returned when the caller has no seat in the requested match. It is a
// 404 and NOT a 403: telling someone that a match they cannot see exists is itself a
// disclosure, and match ids are guessable enough to be worth not confirming.
var ErrNoMatch = httpx.NewError(http.StatusNotFound, "not_found", "No match of yours with that id.")

// MaxMatchPageSize bounds one page. Generous enough that a developer scanning history
// rarely pages twice, small enough that the aggregate query stays cheap.
const MaxMatchPageSize = 50

// Matches returns a page of the caller's match history.
//
// agentPublicID narrows to one of their agents; empty means all of them. As everywhere
// in this package that resolves to an explicit list of owned ids and is never turned
// into an unfiltered query.
func (s *Service) Matches(
	ctx context.Context, userPublicID, agentPublicID string, mode MatchMode, limit, offset int,
) ([]MatchSummary, int, error) {
	if s.matches == nil {
		return nil, 0, httpx.NewError(http.StatusServiceUnavailable, "traces_unconfigured",
			"Match history is not enabled in this environment.")
	}
	actors, err := s.actors(ctx, userPublicID, agentPublicID)
	if err != nil {
		return nil, 0, err
	}
	if len(actors) == 0 {
		// No agents, or an id the caller does not own. Either way: an empty page with a
		// zero total, which is the truthful answer and not an error.
		return []MatchSummary{}, 0, nil
	}
	if limit <= 0 || limit > MaxMatchPageSize {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	out, total, err := s.matches.Matches(ctx, actors, mode, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	if out == nil {
		out = []MatchSummary{}
	}
	return out, total, nil
}

// MatchDetail returns one match: the summary, the roster, and the timeline.
func (s *Service) MatchDetail(ctx context.Context, userPublicID, matchPublicID string) (MatchDetail, error) {
	if s.matches == nil {
		return MatchDetail{}, httpx.NewError(http.StatusServiceUnavailable, "traces_unconfigured",
			"Match history is not enabled in this environment.")
	}
	owned, err := s.repo.OwnedAgentIDs(ctx, userPublicID)
	if err != nil {
		return MatchDetail{}, err
	}
	if len(owned) == 0 {
		return MatchDetail{}, ErrNoMatch
	}

	// GATE 1 — ownership. The summary query is restricted to the caller's own agents, so
	// a match they had no seat in cannot resolve at all. Everything below runs only
	// after this has succeeded.
	sum, err := s.matches.MatchSummaryFor(ctx, owned, matchPublicID)
	if err != nil {
		return MatchDetail{}, err
	}

	roster, err := s.matches.Roster(ctx, matchPublicID, owned)
	if err != nil {
		return MatchDetail{}, err
	}

	rows, err := s.matches.MatchEvents(ctx, owned, matchPublicID, 4000)
	if err != nil {
		return MatchDetail{}, err
	}

	names := make(map[int]string, len(roster))
	for _, seat := range roster {
		names[seat.Seat] = seat.AgentName
	}
	finished := sum.Status == "finished" || sum.Status == "aborted"
	entries := mapRows(rows, func(string) mapOptions {
		return mapOptions{
			SeatName: func(n int) string { return names[n] },
			// Other seats' public events only once the match is over. A live match must
			// not become a live read-out of what everyone else is doing.
			Finished: finished,
		}
	})

	// The caller's own per-move record. Best-effort: a match played before decisions
	// were persisted has none, and the trace is still worth rendering without them —
	// failing the whole page over a missing supplement would be worse than the gap.
	decisions, err := s.matches.MatchDecisions(ctx, owned, matchPublicID, 1000)
	if err != nil {
		return MatchDetail{}, err
	}

	return MatchDetail{Match: sum, Roster: roster, Entries: entries, Decisions: decisions}, nil
}

// actors resolves the agent ids a request may read, enforcing ownership. Shared by the
// history endpoints so the gate cannot drift between them.
func (s *Service) actors(ctx context.Context, userPublicID, agentPublicID string) ([]string, error) {
	owned, err := s.repo.OwnedAgentIDs(ctx, userPublicID)
	if err != nil {
		return nil, err
	}
	if agentPublicID == "" {
		return owned, nil
	}
	for _, id := range owned {
		if id == agentPublicID {
			return []string{agentPublicID}, nil
		}
	}
	// An agent id the caller does not own is a filter that matches nothing, not an
	// authorisation error — consistent with Activity, and it does not confirm whether
	// the id exists.
	return nil, nil
}
