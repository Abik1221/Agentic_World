package query

import (
	"math"
	"sort"
	"time"

	"github.com/gofiber/fiber/v2"
)

// leaderboardCandidateCap bounds how many (agent, game) rows we pull before the
// Wilson re-rank + truncation, so a small-sample fluke can't crowd out real
// agents by SQL LIMIT alone.
const leaderboardCandidateCap = 1000

// BenchmarkStat is one agent's reliability + latency benchmark for a game,
// aggregated over the requested window. Rates are computed from the summed
// counters (division-safe) rather than in SQL.
type BenchmarkStat struct {
	AgentID         string  `json:"agent_id"`
	AgentVersion    string  `json:"agent_version,omitempty"`
	Provider        string  `json:"provider,omitempty"`
	Model           string  `json:"model,omitempty"`
	Game            string  `json:"game"`
	Matches         int64   `json:"matches"`
	Decisions       int64   `json:"decisions"`
	Legal           int64   `json:"legal"`
	Illegal         int64   `json:"illegal"`
	Timeouts        int64   `json:"timeouts"`
	TransportErrors int64   `json:"transport_errors"`
	Disconnects     int64   `json:"disconnects"`
	Errors          int64   `json:"errors"`
	Fallbacks       int64   `json:"fallbacks"`
	Wins            int64   `json:"wins"`
	Losses          int64   `json:"losses"`
	Draws           int64   `json:"draws"`
	LegalRate       float64 `json:"legal_rate"`
	FallbackRate    float64 `json:"fallback_rate"`
	TimeoutRate     float64 `json:"timeout_rate"`
	WinRate         float64 `json:"win_rate"`
	// WinRateLB is the Wilson score-interval lower bound of the win rate at 95%
	// confidence — a sample-size-aware conservative estimate the leaderboard ranks
	// by, so a 3-match 100% doesn't outrank a 500-match 80%.
	WinRateLB    float64 `json:"win_rate_lb"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`

	// Token/cost economics (from the agent's SDK-reported model_call events,
	// aggregated by actor_id). Zero when the agent's SDK doesn't report usage.
	ModelCalls     int64   `json:"model_calls"`
	TotalTokens    int64   `json:"total_tokens"`
	EstimatedCost  float64 `json:"estimated_cost"`
	TokensPerMatch float64 `json:"tokens_per_match"`
	CostPerMatch   float64 `json:"cost_per_match"`
	CostPerWin     float64 `json:"cost_per_win"` // 0 when no wins
	// AvgModelLatencyMS is the provider's model-call latency (from SDK model_call
	// events), distinct from AvgLatencyMS (the engine's full ask→answer round-trip
	// = transport + agent compute + model). Together they decompose where time goes.
	AvgModelLatencyMS float64 `json:"avg_model_latency_ms"`

	latencySumMS int64 // scanned, then folded into AvgLatencyMS; not serialized
}

type tokenAgg struct {
	tokens int64
	cost   float64
	calls  int64
	latSum int64
}

// tokenUsageByAgent aggregates SDK-reported model-call token usage + cost per
// (agent, game) over the window, keyed "agent|game". This is the economics half
// of the report — what a model provider cares about — joined to the reliability
// half at query time. Missing (no SDK usage) ⇒ absent from the map (zeros).
func (h Handler) tokenUsageByAgent(c *fiber.Ctx, orgID string, since time.Time, game string) (map[string]tokenAgg, error) {
	where := "organization_id = ? AND event_time >= ? AND event_type = 'model_call_completed' AND actor_id != ''"
	args := []any{orgID, since}
	if game != "" {
		where += " AND session_id = ?"
		args = append(args, game)
	}
	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT actor_id, session_id,
			sum(total_tokens), sum(if(reconciled_cost > 0, reconciled_cost, estimated_cost)), count(), sum(latency_ms)
		FROM events_raw
		WHERE `+where+`
		GROUP BY actor_id, session_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]tokenAgg{}
	for rows.Next() {
		var agent, g string
		var t tokenAgg
		if err := rows.Scan(&agent, &g, &t.tokens, &t.cost, &t.calls, &t.latSum); err != nil {
			return nil, err
		}
		m[agent+"|"+g] = t
	}
	return m, rows.Err()
}

// attachTokenUsage folds the token/cost aggregate into each stat + derives
// per-match / per-win economics.
func attachTokenUsage(stats []BenchmarkStat, tok map[string]tokenAgg) {
	for i := range stats {
		s := &stats[i]
		t, ok := tok[s.AgentID+"|"+s.Game]
		if !ok {
			continue
		}
		s.ModelCalls, s.TotalTokens, s.EstimatedCost = t.calls, t.tokens, t.cost
		if s.Matches > 0 {
			s.TokensPerMatch = float64(t.tokens) / float64(s.Matches)
			s.CostPerMatch = t.cost / float64(s.Matches)
		}
		if s.Wins > 0 {
			s.CostPerWin = t.cost / float64(s.Wins)
		}
		if t.calls > 0 {
			s.AvgModelLatencyMS = float64(t.latSum) / float64(t.calls)
		}
	}
}

// AgentLeaderboard ranks agents by reliability (legal-move rate desc, then avg
// latency asc) over the last N days. Filterable by ?game= and ?mode=. One row per
// (agent, game).
//
//	GET /v1/benchmarks/agents?game=&mode=&days=30&limit=100
func (h Handler) AgentLeaderboard(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	days := parseInt(c.Query("days"), 30)
	limit := parseInt(c.Query("limit"), 100)
	since := dayBucketDaysAgo(days)

	where := "organization_id = ? AND bucket_start >= ?"
	args := []any{orgID, since}
	if g := c.Query("game"); g != "" {
		where += " AND game = ?"
		args = append(args, g)
	}
	if m := c.Query("mode"); m != "" {
		where += " AND mode = ?"
		args = append(args, m)
	}
	args = append(args, leaderboardCandidateCap)

	stats, err := h.queryBenchmarks(c, `
		SELECT agent_id, game,
			sum(matches), sum(decisions), sum(legal), sum(illegal), sum(timeouts),
			sum(transport_errors), sum(disconnects), sum(errors), sum(fallbacks), sum(latency_sum_ms),
			sum(wins), sum(losses), sum(draws), any(provider), any(model)
		FROM agent_benchmarks
		WHERE `+where+`
		GROUP BY agent_id, game
		HAVING sum(decisions) > 0
		LIMIT ?`, args...)
	if err != nil {
		return err
	}
	// Authoritative rank is the Wilson lower bound (computed in Go), then legal
	// rate, then latency — sample-size-aware, so flukes don't top the board.
	sort.SliceStable(stats, func(i, j int) bool {
		a, b := stats[i], stats[j]
		if a.WinRateLB != b.WinRateLB {
			return a.WinRateLB > b.WinRateLB
		}
		if a.LegalRate != b.LegalRate {
			return a.LegalRate > b.LegalRate
		}
		return a.AvgLatencyMS < b.AvgLatencyMS
	})
	if limit > 0 && len(stats) > limit {
		stats = stats[:limit]
	}
	if tok, terr := h.tokenUsageByAgent(c, orgID, since, c.Query("game")); terr == nil {
		attachTokenUsage(stats, tok)
	}
	return c.JSON(fiber.Map{"agents": stats})
}

// AgentBenchmark returns one agent's per-game benchmark breakdown over the window.
//
//	GET /v1/benchmarks/agents/:agent_id?days=30
func (h Handler) AgentBenchmark(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	agentID := c.Params("agent_id")
	days := parseInt(c.Query("days"), 30)
	since := dayBucketDaysAgo(days)

	stats, err := h.queryBenchmarks(c, `
		SELECT agent_id, game,
			sum(matches), sum(decisions), sum(legal), sum(illegal), sum(timeouts),
			sum(transport_errors), sum(disconnects), sum(errors), sum(fallbacks), sum(latency_sum_ms),
			sum(wins), sum(losses), sum(draws), any(provider), any(model)
		FROM agent_benchmarks
		WHERE organization_id = ? AND agent_id = ? AND bucket_start >= ?
		GROUP BY agent_id, game
		HAVING sum(decisions) > 0
		ORDER BY game`, orgID, agentID, since)
	if err != nil {
		return err
	}
	if tok, terr := h.tokenUsageByAgent(c, orgID, since, ""); terr == nil {
		attachTokenUsage(stats, tok)
	}
	return c.JSON(fiber.Map{"agent_id": agentID, "games": stats})
}

// AgentVersions compares an agent's benchmark across its manifest versions — the
// version-diff view (did v2 beat v1?). One row per (game, agent_version),
// ordered newest-played first so the latest iteration is on top.
//
//	GET /v1/benchmarks/agents/:agent_id/versions?days=90
func (h Handler) AgentVersions(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	agentID := c.Params("agent_id")
	days := parseInt(c.Query("days"), 90)
	since := dayBucketDaysAgo(days)

	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT agent_id, agent_version, game,
			sum(matches), sum(decisions), sum(legal), sum(illegal), sum(timeouts),
			sum(transport_errors), sum(disconnects), sum(errors), sum(fallbacks), sum(latency_sum_ms),
			sum(wins), sum(losses), sum(draws), any(provider), any(model)
		FROM agent_benchmarks
		WHERE organization_id = ? AND agent_id = ? AND bucket_start >= ? AND agent_version != ''
		GROUP BY agent_id, agent_version, game
		HAVING sum(decisions) > 0
		ORDER BY game, agent_version`, orgID, agentID, since)
	if err != nil {
		return err
	}
	defer rows.Close()
	stats := []BenchmarkStat{}
	for rows.Next() {
		var s BenchmarkStat
		if err := rows.Scan(
			&s.AgentID, &s.AgentVersion, &s.Game, &s.Matches, &s.Decisions, &s.Legal, &s.Illegal, &s.Timeouts,
			&s.TransportErrors, &s.Disconnects, &s.Errors, &s.Fallbacks, &s.latencySumMS,
			&s.Wins, &s.Losses, &s.Draws, &s.Provider, &s.Model,
		); err != nil {
			return err
		}
		s.compute()
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"agent_id": agentID, "versions": stats})
}

// ProviderBenchmark is one provider×model×game aggregate (provider intel).
type ProviderBenchmark struct {
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Game         string  `json:"game"`
	Agents       int64   `json:"agents"`
	Matches      int64   `json:"matches"`
	Decisions    int64   `json:"decisions"`
	WinRate      float64 `json:"win_rate"`
	LegalRate    float64 `json:"legal_rate"`
	FallbackRate float64 `json:"fallback_rate"`
	TimeoutRate  float64 `json:"timeout_rate"`
	AvgLatencyMS float64 `json:"avg_latency_ms"`
}

// ProviderBenchmarks compares model providers/models across all agents — the
// provider-intel view (which provider is fastest / most reliable per game). One
// row per (provider, model, game), latency-ranked.
//
//	GET /v1/benchmarks/providers?game=&days=30
func (h Handler) ProviderBenchmarks(c *fiber.Ctx) error {
	orgID, err := requireOrgID(c)
	if err != nil {
		return err
	}
	days := parseInt(c.Query("days"), 30)
	since := dayBucketDaysAgo(days)
	where := "organization_id = ? AND bucket_start >= ? AND provider != ''"
	args := []any{orgID, since}
	if g := c.Query("game"); g != "" {
		where += " AND game = ?"
		args = append(args, g)
	}

	rows, err := h.Store.DB.QueryContext(c.UserContext(), `
		SELECT provider, model, game,
			uniqExact(agent_id), sum(matches), sum(decisions),
			sum(wins), sum(legal), sum(fallbacks), sum(timeouts), sum(latency_sum_ms)
		FROM agent_benchmarks
		WHERE `+where+`
		GROUP BY provider, model, game
		HAVING sum(decisions) > 0
		ORDER BY sum(latency_sum_ms) / sum(decisions) ASC`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	out := []ProviderBenchmark{}
	for rows.Next() {
		var p ProviderBenchmark
		var wins, legal, fallbacks, timeouts, latSum int64
		if err := rows.Scan(&p.Provider, &p.Model, &p.Game, &p.Agents, &p.Matches, &p.Decisions,
			&wins, &legal, &fallbacks, &timeouts, &latSum); err != nil {
			return err
		}
		p.WinRate = ratio(wins, p.Matches)
		p.LegalRate = ratio(legal, p.Decisions)
		p.FallbackRate = ratio(fallbacks, p.Decisions)
		p.TimeoutRate = ratio(timeouts, p.Decisions)
		if p.Decisions > 0 {
			p.AvgLatencyMS = float64(latSum) / float64(p.Decisions)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return c.JSON(fiber.Map{"providers": out})
}

// queryBenchmarks runs a benchmark aggregation query and derives the rates.
func (h Handler) queryBenchmarks(c *fiber.Ctx, sql string, args ...any) ([]BenchmarkStat, error) {
	rows, err := h.Store.DB.QueryContext(c.UserContext(), sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BenchmarkStat{}
	for rows.Next() {
		var s BenchmarkStat
		if err := rows.Scan(
			&s.AgentID, &s.Game, &s.Matches, &s.Decisions, &s.Legal, &s.Illegal, &s.Timeouts,
			&s.TransportErrors, &s.Disconnects, &s.Errors, &s.Fallbacks, &s.latencySumMS,
			&s.Wins, &s.Losses, &s.Draws, &s.Provider, &s.Model,
		); err != nil {
			return nil, err
		}
		s.compute()
		out = append(out, s)
	}
	return out, rows.Err()
}

func (s *BenchmarkStat) compute() {
	if s.Decisions > 0 {
		s.LegalRate = ratio(s.Legal, s.Decisions)
		s.FallbackRate = ratio(s.Fallbacks, s.Decisions)
		s.TimeoutRate = ratio(s.Timeouts, s.Decisions)
		s.AvgLatencyMS = float64(s.latencySumMS) / float64(s.Decisions)
	}
	// Win-rate over all matches played (unknown-outcome matches count as non-wins).
	s.WinRate = ratio(s.Wins, s.Matches)
	s.WinRateLB = wilsonLowerBound(s.Wins, s.Matches, 1.96) // 95% confidence
}

// wilsonLowerBound is the lower bound of the Wilson score interval for a
// proportion (wins/n) at confidence z. It shrinks toward 0 as the sample gets
// small, so few-match agents are ranked conservatively. n<=0 → 0.
func wilsonLowerBound(wins, n int64, z float64) float64 {
	if n <= 0 {
		return 0
	}
	nf := float64(n)
	phat := float64(wins) / nf
	z2 := z * z
	denom := 1 + z2/nf
	center := phat + z2/(2*nf)
	margin := z * math.Sqrt(phat*(1-phat)/nf+z2/(4*nf*nf))
	lb := (center - margin) / denom
	if lb < 0 {
		return 0
	}
	return lb
}

func ratio(n, d int64) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

func dayBucketDaysAgo(days int) time.Time {
	if days <= 0 {
		days = 30
	}
	t := time.Now().UTC().AddDate(0, 0, -days)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
