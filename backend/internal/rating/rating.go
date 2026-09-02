package rating

import (
	"context"
	"github.com/agent-arena/arena/internal/arenanorm"
	"github.com/agent-arena/arena/internal/integrity"
	"math"
	"sort"
	"strconv"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/prometheus/client_golang/prometheus"
)

// seasonEpoch anchors season numbering; seasons are fixed-length windows from here.
var seasonEpoch = time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

// Config tunes the rating system.
type Config struct {
	SeasonLength time.Duration // length of one season (default 30 days)
}

// Arena game keys. The rating subject is (agent, game, season): each arena keeps an
// independent rating. New arenas simply use a new key — no schema change.
const (
	GameGoofspiel = "goofspiel"
	GameMafia     = "mafia"
)

// Rating algorithms. 1v1 arenas use Glicko-2; N-player arenas use TrueSkill.
const (
	AlgoGlicko2   = "glicko2"
	AlgoTrueSkill = "trueskill"
)

// PlayerResult is one seat's finished-match outcome. Placement is the finishing rank
// (1 = best); equal placements mean a tie between those agents (e.g. a winning
// faction in Mafia all share placement 1).
type PlayerResult struct {
	AgentPublicID string
	Seat          int
	Placement     int
	CoinsDelta    int64
}

// MatchResult is the finalized outcome the match worker hands to Rate. Game selects
// the arena; the player count selects the algorithm (2 → Glicko-2, >2 → TrueSkill).
type MatchResult struct {
	MatchPublicID string
	Game          string
	Players       []PlayerResult
	// Integrity is the verdict the engine already built for settlement. Rate uses it to
	// decide which seats may move a rating, via integrity.FilterRatable.
	//
	// The ZERO VALUE IS INERT — an engine that does not supply one rates everything, which
	// is the behaviour that existed before this field. That default is deliberate: reading
	// an absent verdict as "everyone failed" would void honest play in bulk, the same
	// mistake the completion-binding rules exist to prevent.
	//
	// It is passed in rather than recomputed here because the engines evaluate once for
	// FilterPayable, and a second evaluation of the same table could disagree with the
	// first — leaving a seat paid but unrated, or the reverse.
	Integrity integrity.Verdict
}

// LeaderboardPage is a paginated leaderboard slice.
type LeaderboardPage struct {
	Season     int         `json:"season"`
	Game       string      `json:"game"`
	Entries    []LeaderRow `json:"entries"`
	NextCursor string      `json:"next_cursor,omitempty"`
}

// Service applies rating changes and serves the leaderboard.
type Service struct {
	repo  Repo
	clock platform.Clock
	cfg   Config
	m     *metrics
	// Boards that scan the whole benchmark table, cached briefly. See cache.go for why an
	// index is not the answer here.
	modelCache *resultCache[BenchmarkPage]
	devCache   *resultCache[DeveloperBoard]
}

// New builds the rating service.
func New(repo Repo, clock platform.Clock, cfg Config, reg *prometheus.Registry) *Service {
	if cfg.SeasonLength <= 0 {
		cfg.SeasonLength = 30 * 24 * time.Hour
	}
	return &Service{
		repo: repo, clock: clock, cfg: cfg, m: newMetrics(reg),
		// 30s matches the Cache-Control these endpoints already advertise, so the origin now
		// keeps the same promise it was making to clients.
		modelCache: newResultCache[BenchmarkPage](30 * time.Second),
		devCache:   newResultCache[DeveloperBoard](30 * time.Second),
	}
}

// CurrentSeason is the season number for now (date-derived; a new window starts a
// fresh ELO baseline while prior seasons' rows remain as the historical snapshot).
func (s *Service) CurrentSeason() int {
	d := s.clock.Now().Sub(seasonEpoch)
	if d < 0 {
		return 0
	}
	return int(d / s.cfg.SeasonLength)
}

// SeasonInfo describes a season's fixed window.
type SeasonInfo struct {
	Season    int       `json:"season"`
	StartsAt  time.Time `json:"starts_at"`
	EndsAt    time.Time `json:"ends_at"`
	Now       time.Time `json:"now"`
	Remaining string    `json:"remaining"` // human duration until the season ends
}

// SeasonBounds returns the [start, end) window for a season number.
func (s *Service) SeasonBounds(season int) (start, end time.Time) {
	start = seasonEpoch.Add(time.Duration(season) * s.cfg.SeasonLength)
	end = start.Add(s.cfg.SeasonLength)
	return start, end
}

// SeasonChampion is the winner of the most recently finalised season — the
// rank-1 agent of that season's final standings — with full stats + avatar.
// Champion is nil when no season has been finalised yet (or it had no matches).
type SeasonChampion struct {
	Season   int        `json:"season"`
	Champion *LeaderRow `json:"champion"`
}

// SeasonChampion returns the winner of the last finalised season. Standings are
// preserved per-season, so the champion is that season's rank 1.
func (s *Service) SeasonChampion(ctx context.Context) (SeasonChampion, error) {
	last, err := s.repo.LastRolledSeason(ctx)
	if err != nil {
		return SeasonChampion{}, err
	}
	res := SeasonChampion{Season: last}
	if last < 0 {
		return res, nil // no season finalised yet
	}
	rows, err := s.repo.Leaderboard(ctx, GameGoofspiel, last, 0, 1)
	if err != nil {
		return SeasonChampion{}, err
	}
	if len(rows) > 0 {
		rows[0].Rank = 1
		res.Champion = &rows[0]
	}
	return res, nil
}

// CurrentSeasonInfo describes the ongoing season and how long is left in it.
func (s *Service) CurrentSeasonInfo() SeasonInfo {
	season := s.CurrentSeason()
	start, end := s.SeasonBounds(season)
	now := s.clock.Now()
	return SeasonInfo{
		Season: season, StartsAt: start, EndsAt: end, Now: now,
		Remaining: end.Sub(now).Round(time.Second).String(),
	}
}

// RollCompleted finalises every season that has ended but not yet been rolled:
// it records the roll (idempotently) and emits season.rolled with the champion
// (the top of that season's leaderboard, or empty if the season had no matches).
// Safe to call repeatedly and on every instance — the DB roll row is the guard.
func (s *Service) RollCompleted(ctx context.Context) error {
	last, err := s.repo.LastRolledSeason(ctx)
	if err != nil {
		return err
	}
	cur := s.CurrentSeason()
	for season := last + 1; season < cur; season++ {
		champion, err := s.championOf(ctx, season)
		if err != nil {
			return err
		}
		if _, err := s.repo.RollSeason(ctx, season, champion); err != nil {
			return err
		}
	}
	return nil
}

// ForceRollCurrentSeason finalises the CURRENT season immediately — recording its
// champion and marking it rolled — regardless of the calendar. This is a DEV/TEST
// affordance so the season-champion surface can be exercised without waiting for a
// real season boundary; it is exposed only behind the dev gate. Idempotent per
// season (RollSeason is a no-op if already rolled).
func (s *Service) ForceRollCurrentSeason(ctx context.Context) (season int, champion string, err error) {
	cur := s.CurrentSeason()
	champion, err = s.championOf(ctx, cur)
	if err != nil {
		return 0, "", err
	}
	if _, err = s.repo.RollSeason(ctx, cur, champion); err != nil {
		return 0, "", err
	}
	return cur, champion, nil
}

// championOf returns the top-ranked agent of a season, or "" if none played. It
// queries the repo directly with the exact season (Service.Leaderboard treats
// season 0 as "current", which would misresolve season 0 here).
func (s *Service) championOf(ctx context.Context, season int) (string, error) {
	rows, err := s.repo.Leaderboard(ctx, GameGoofspiel, season, 0, 1)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", nil
	}
	return rows[0].AgentPublicID, nil
}

// Elo returns an agent's current-season rating in the given arena, or the 1500
// baseline if it has no rating row yet — unrated agents matchmake from the baseline.
// Used by matchmaking to pair within a skill band.
func (s *Service) Elo(ctx context.Context, agentPublicID, game string) (int, error) {
	if game == "" {
		game = GameGoofspiel
	}
	return s.repo.AgentElo(ctx, agentPublicID, game, s.CurrentSeason())
}

// Rate applies a finished match's rating change to the match's arena in the current
// season. Idempotent per match. 2-player matches use Glicko-2; N-player (>2) matches
// use TrueSkill. Implements (via an adapter) match.Rater.
func (s *Service) Rate(ctx context.Context, res MatchResult) error {
	if len(res.Players) < 2 {
		return nil // nothing to rate (need at least two participants)
	}
	game := res.Game
	if game == "" {
		game = GameGoofspiel
	}
	// The algorithm is fixed PER ARENA, not per match: Goofspiel (always 1v1) uses
	// Glicko-2; every other arena uses TrueSkill. Choosing by arena (not by the
	// per-match player count) prevents two algorithms from writing incompatible
	// state (elo/rd/vol vs mu/sigma) to the same rating row and clobbering it.
	algo := AlgoTrueSkill
	compute := TrueSkillApply
	if game == GameGoofspiel && len(res.Players) == 2 {
		algo = AlgoGlicko2
		compute = glicko2Apply
	}
	// Integrity voids ratings, not only money.
	//
	// Before this, a staked match whose seat proved not one LLM-backed decision was
	// refunded (match/service.go:1488-1497) and then rated anyway (:1551-1558). Mafia and
	// Monopoly withheld payouts and called the rater regardless. A scripted agent was
	// therefore refunded every time and climbed the ladder for free.
	//
	// One gate, one filter. An earlier draft also re-checked len(res.Players) < 2 after
	// filtering, which can never fire — FilterRatable already guarantees two survivors
	// when it returns ratable — and a dead check that reads like a guarantee is worse
	// than no check.
	ids := make([]string, len(res.Players))
	for i, p := range res.Players {
		ids[i] = p.AgentPublicID
	}
	keep, excluded, ratable := integrity.FilterRatable(ids, res.Integrity)
	if len(excluded) > 0 {
		s.m.ratingsVoided.Inc()
	}
	if !ratable {
		// Fewer than two seats survived, so there is no comparison left to make. For a 1v1
		// that is the whole match, matching Goofspiel's money rule where a bad seat voids
		// it: rating the honest seat would mean rating it against an opponent we have
		// reason to think was not an LLM at all.
		return nil
	}
	if len(excluded) > 0 {
		kept := make(map[string]bool, len(keep))
		for _, k := range keep {
			kept[k] = true
		}
		filtered := make([]PlayerResult, 0, len(keep))
		for _, p := range res.Players {
			if kept[p.AgentPublicID] {
				filtered = append(filtered, p)
			}
		}
		res.Players = filtered
	}

	players := make([]ApplyPlayer, len(res.Players))
	for i, p := range res.Players {
		players[i] = ApplyPlayer(p)
	}
	applied, err := s.repo.ApplyMatch(ctx, ApplyInput{
		MatchPublicID: res.MatchPublicID,
		Game:          game,
		Season:        s.CurrentSeason(),
		Algo:          algo,
		Players:       players,
		Compute:       compute,
	})
	if err != nil {
		return err
	}
	if applied {
		s.m.eloUpdates.Inc()
	}
	return nil
}

// glicko2Apply is the 2-player Compute closure: it derives seat 0's score from the
// two placements and runs the existing, unchanged Glicko-2 update, leaving the
// TrueSkill (mu/sigma) fields untouched.
func glicko2Apply(cur []RatingState, placements []int) []RatingState {
	scoreA := 0.5
	switch {
	case placements[0] < placements[1]:
		scoreA = 1
	case placements[0] > placements[1]:
		scoreA = 0
	}
	a := PlayerRating{Elo: cur[0].Elo, RD: cur[0].RD, Vol: cur[0].Vol}
	b := PlayerRating{Elo: cur[1].Elo, RD: cur[1].RD, Vol: cur[1].Vol}
	na, nb := Glicko2(a, b, scoreA)
	return []RatingState{
		{Elo: na.Elo, RD: na.RD, Vol: na.Vol, Mu: cur[0].Mu, Sigma: cur[0].Sigma},
		{Elo: nb.Elo, RD: nb.RD, Vol: nb.Vol, Mu: cur[1].Mu, Sigma: cur[1].Sigma},
	}
}

// Leaderboard returns one page of season standings (defaults: current season,
// limit 50, capped at 100). offset-based cursor.
func (s *Service) Leaderboard(ctx context.Context, game string, season, offset, limit int) (LeaderboardPage, error) {
	if game == "" {
		game = GameGoofspiel
	}
	if season <= 0 {
		season = s.CurrentSeason()
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.repo.Leaderboard(ctx, game, season, offset, limit)
	if err != nil {
		return LeaderboardPage{}, err
	}
	for i := range rows {
		rows[i].Rank = offset + i + 1
	}
	page := LeaderboardPage{Season: season, Game: game, Entries: rows}
	if len(rows) == limit {
		page.NextCursor = strconv.Itoa(offset + limit)
	}
	return page, nil
}

// SnapshotRanks records today's per-(game,season) rank for every agent so the
// leaderboard can show a rank trend. Idempotent per day; driven by the
// rank-snapshotter background loop. Returns rows written.
func (s *Service) SnapshotRanks(ctx context.Context) (int, error) {
	return s.repo.SnapshotRanks(ctx, s.clock.Now())
}

// ArenaAll is the `game` value meaning "every arena, aggregated". It is the DEFAULT
// for the model board: the board's claim is "which model wins on Pyyol", and silently
// answering it from one of three arenas — which is what an empty game used to do — is
// a different claim than the heading makes.
const ArenaAll = "all"

// Arenas is every rated arena, in board display order.
var Arenas = []string{GameGoofspiel, GameMafia}

// IsArena reports whether game names a rated arena (or the all-arena aggregate).
func IsArena(game string) bool {
	if game == "" || game == ArenaAll {
		return true
	}
	for _, g := range Arenas {
		if g == game {
			return true
		}
	}
	return false
}

// BenchmarkPage is the "which model wins" board for the current season.
type BenchmarkPage struct {
	Season int    `json:"season"`
	Game   string `json:"game"` // "all" for the cross-arena aggregate
	// SeasonStart/End bound the window every figure below was measured over, so a
	// reader can tell whether a small sample means "new model" or "quiet season".
	SeasonStart time.Time   `json:"season_start"`
	SeasonEnd   time.Time   `json:"season_end"`
	Arenas      []string    `json:"arenas"` // arenas selectable on this board
	MinGames    int         `json:"min_games"`
	Models      []ModelStat `json:"models"`
	// Groups is the same season pooled by provider, vendor, openness, hosting and
	// family — the "are open-weight models competitive yet" view. Derived from Models,
	// so it always reconciles against the rows above it and a never-before-seen model
	// joins its groups on its first finished match.
	Groups []GroupStat `json:"groups"`
}

// ModelBenchmark ranks models by their real season performance, across every arena
// (game == "" or ArenaAll) or within one.
//
// minGames is the floor for APPEARING at all and defaults to 1 — a model must have
// actually finished a rated game. It is deliberately not the same thing as having
// enough games to RANK on, which is what ModelStat.Preliminary and the win-rate
// confidence interval report: dropping thin rows would make the board look complete
// when it is not, so they are shown and marked instead.
// HarnessBenchmark is ModelBenchmark over the platform's own benchmark matches.
//
// Delegates to the same body so every derived figure — win rate, tokens per match, cost per
// win, the attribution tiering — is computed identically. A second copy of that derivation
// would be the same for one release and subtly different for every release after.
func (s *Service) HarnessBenchmark(ctx context.Context, game string, minGames int) (BenchmarkPage, error) {
	return s.benchmarkPage(ctx, game, minGames, true)
}

// HarnessMatches lists published platform-harness matches that can be replayed.
// Straight passthrough: the restriction that matters (harness-kind agents only, and
// only matches that actually have a log) belongs in the query, next to the data.
func (s *Service) HarnessMatches(ctx context.Context, game string, limit int) ([]HarnessMatch, error) {
	if game == ArenaAll {
		game = ""
	}
	return s.repo.HarnessMatches(ctx, game, limit)
}

// ModelBenchmark serves the public model board, cached for a few seconds.
//
// The cache sits here rather than in the handler so every caller benefits — the public
// endpoint, the admin proxy and DeveloperBoard, which reuses this for its baselines and
// would otherwise trigger the same full scan a second time on one request.
func (s *Service) ModelBenchmark(ctx context.Context, game string, minGames int) (BenchmarkPage, error) {
	if s.modelCache == nil { // a Service built without New (tests) stays uncached
		return s.modelBenchmarkUncached(ctx, game, minGames)
	}
	return s.modelCache.get(cacheKey(game, minGames), func(c context.Context) (BenchmarkPage, error) {
		return s.modelBenchmarkUncached(c, game, minGames)
	})
}

func cacheKey(game string, minGames int) string {
	return game + "|" + strconv.Itoa(minGames)
}

func (s *Service) modelBenchmarkUncached(ctx context.Context, game string, minGames int) (BenchmarkPage, error) {
	return s.benchmarkPage(ctx, game, minGames, false)
}

func (s *Service) benchmarkPage(ctx context.Context, game string, minGames int, harness bool) (BenchmarkPage, error) {
	if game == ArenaAll {
		game = "" // the repo reads "" as "do not filter by arena"
	}
	if minGames <= 0 {
		minGames = 1
	}
	season := s.CurrentSeason()
	start, end := s.SeasonBounds(season)
	read := s.repo.ModelBenchmark
	if harness {
		read = s.repo.HarnessModelBenchmark
	}
	models, err := read(ctx, season, game, start, end)
	if err != nil {
		return BenchmarkPage{}, err
	}

	out := make([]ModelStat, 0, len(models))
	for i := range models {
		m := &models[i]
		deriveModelStat(m)
		if m.Games < minGames {
			continue
		}
		out = append(out, *m)
	}
	orderModels(out, game)

	reported := game
	if reported == "" {
		reported = ArenaAll
	}
	return BenchmarkPage{
		Season: season, Game: reported,
		SeasonStart: start, SeasonEnd: end,
		Arenas: Arenas, MinGames: minGames, Models: out,
		// Built from the rows that survived filtering, so the groups always reconcile
		// against the board.
		Groups: BuildGroups(out),
	}, nil
}

// ModelDetail is everything one model's page shows: its full season record, the
// per-arena split, and who is actually running it.
type ModelDetail struct {
	Season      int       `json:"season"`
	SeasonStart time.Time `json:"season_start"`
	SeasonEnd   time.Time `json:"season_end"`
	Game        string    `json:"game"`
	Arenas      []string  `json:"arenas"`

	Model ModelStat `json:"model"`
	// Runners are the agents playing it, most active first. Capped by the store.
	Runners []ModelRunner `json:"runners"`
	// Rank is this model's position on the board it was reached from (1-based), so the
	// page can say "3rd of 11" without the reader having to go back and count.
	Rank  int `json:"rank"`
	Total int `json:"total"`
}

// ModelDetail assembles one model's page.
//
// It reuses ModelBenchmark rather than running its own aggregate, so the detail page
// and the row the reader clicked to reach it cannot disagree — the alternative is a
// second query that has to be kept in step with the first by hand, which is exactly
// the kind of drift that makes a benchmark untrustworthy. minGames is deliberately 1
// here: a model reachable by URL must render even when it is too thin for the board.
//
// found=false when no match in the window resolved to this model.
func (s *Service) ModelDetail(ctx context.Context, game, provider, model string) (ModelDetail, bool, error) {
	if model == "" {
		return ModelDetail{}, false, nil
	}
	page, err := s.ModelBenchmark(ctx, game, 1)
	if err != nil {
		return ModelDetail{}, false, err
	}
	idx := -1
	for i := range page.Models {
		if page.Models[i].Model == model && page.Models[i].Provider == provider {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ModelDetail{}, false, nil
	}

	arena := game
	if arena == ArenaAll {
		arena = ""
	}
	season := s.CurrentSeason()
	start, end := s.SeasonBounds(season)
	runners, err := s.repo.ModelRunners(ctx, season, arena, provider, model, start, end)
	if err != nil {
		return ModelDetail{}, false, err
	}
	for i := range runners {
		r := &runners[i]
		if decisive := r.Wins + r.Losses; decisive > 0 {
			r.WinRate = float64(r.Wins) / float64(decisive)
			r.WinRateCI = wilsonHalfWidth95(r.Wins, decisive)
		}
	}

	return ModelDetail{
		Season: season, SeasonStart: start, SeasonEnd: end,
		Game: page.Game, Arenas: Arenas,
		Model: page.Models[idx], Runners: runners,
		Rank: idx + 1, Total: len(page.Models),
	}, true, nil
}

// DeveloperBoard is the AGENTIC benchmark: developers ranked by how much they get out
// of whatever model they run, rather than by which model they can afford.
type DeveloperBoard struct {
	Season      int             `json:"season"`
	Game        string          `json:"game"`
	SeasonStart time.Time       `json:"season_start"`
	SeasonEnd   time.Time       `json:"season_end"`
	Arenas      []string        `json:"arenas"`
	MinGames    int             `json:"min_games"`
	Developers  []DeveloperEdge `json:"developers"`
}

// DeveloperBoard computes the agentic leaderboard for a season.
//
// It reuses ModelBenchmark for the baselines, so a developer's edge is measured against
// the exact per-model win rate the public model board publishes — the two boards are
// two views of one dataset, and a reader can check any edge by hand from them.
func (s *Service) DeveloperBoard(ctx context.Context, game string, minGames int) (DeveloperBoard, error) {
	if s.devCache == nil {
		return s.developerBoardUncached(ctx, game, minGames)
	}
	return s.devCache.get(cacheKey(game, minGames), func(c context.Context) (DeveloperBoard, error) {
		return s.developerBoardUncached(c, game, minGames)
	})
}

func (s *Service) developerBoardUncached(ctx context.Context, game string, minGames int) (DeveloperBoard, error) {
	if game == ArenaAll {
		game = ""
	}
	if minGames <= 0 {
		minGames = 1
	}
	season := s.CurrentSeason()
	start, end := s.SeasonBounds(season)

	// minGames 1 for the baselines on purpose: a thin model still supplies the fairest
	// available expectation for the developer who ran it, and filtering it out would
	// silently score them against nothing.
	page, err := s.ModelBenchmark(ctx, game, 1)
	if err != nil {
		return DeveloperBoard{}, err
	}
	rows, err := s.repo.DeveloperModelSplit(ctx, season, game, start, end)
	if err != nil {
		return DeveloperBoard{}, err
	}

	reported := game
	if reported == "" {
		reported = ArenaAll
	}
	return DeveloperBoard{
		Season: season, Game: reported, SeasonStart: start, SeasonEnd: end,
		Arenas: Arenas, MinGames: minGames,
		Developers: BuildDeveloperEdges(rows, page.Models, minGames),
	}, nil
}

// Intelligence scoring constants, mirroring the P-Index v2 intelligence dimension
// (migration 0051). They are duplicated as named constants rather than re-read from
// pindex_config because this is a public read path that must not depend on whether an
// operator has activated a config version — but the VALUES must stay in step, which
// is why they are named after their source.
const (
	intelWLegal       = 0.4
	intelWReliability = 0.4
	intelWSpeed       = 0.2
	intelLatencyFast  = 500.0  // ms/decision at or below which speed scores full marks
	intelLatencySlow  = 8000.0 // ms/decision at or above which speed scores nothing
	intelMinDecisions = 200    // below this, the sample is too small to score honestly
	intelScale        = 1000
)

// prelimMinGames is the number of finished rated games below which a model's figures
// are tagged Preliminary.
//
// At 30 decisive games the 95% interval on a 50% win rate is still roughly ±18
// points, so this is not "now it is accurate" — it is the point below which the
// numbers are actively misleading if read as a ranking. The published confidence
// interval remains the honest guide; the tag exists so nobody has to compute one to
// know a row is thin.
const prelimMinGames = 30

// wilsonHalfWidth95 returns the half-width of the 95% Wilson score interval for k
// successes in n trials — the "±" on a win rate.
//
// Wilson rather than the textbook normal approximation because the normal interval
// misbehaves exactly where a young leaderboard lives: at 4 wins from 4 games it
// reports ±0, claiming a 100% win rate is certain. Returns 0 for n == 0, where there
// is no rate to bound.
func wilsonHalfWidth95(k, n int) float64 {
	if n <= 0 {
		return 0
	}
	const z = 1.96
	nf := float64(n)
	p := float64(k) / nf
	denom := 1 + z*z/nf
	return (z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))) / denom
}

// deriveModelStat computes every derived figure on a model row, and on each of its
// per-arena rows, from the raw counters the store summed.
//
// All derivation lives here rather than in SQL so the aggregate and the per-arena
// breakdown cannot drift: a "tokens per match" that means one thing on the total row
// and another on an arena row is worse than not showing it.
//
// Every figure stays 0 rather than becoming a guess when its inputs are missing. A
// model with no finished match showing "0 tokens/min" is honest; extrapolating a rate
// from a partial match would publish a number nobody could reproduce.
func deriveModelStat(m *ModelStat) {
	// Derived from COVERAGE, not from the raw rank. The rank says the best tier at which
	// this model was ever identified; coverage says how much of the row that tier actually
	// describes. One gateway-verified call out of ten thousand decisions used to be enough to
	// stamp the whole row "verified", which is the state an agent would engineer to keep a
	// badge while avoiding the audit. Tier can only ever downgrade, never promote.
	m.Attribution = Tier(m.AttrRank, m.Verified)
	m.Class = Classify(m.Provider, m.Model)
	m.Games = m.Wins + m.Losses + m.Ties
	if decisive := m.Wins + m.Losses; decisive > 0 {
		m.WinRate = float64(m.Wins) / float64(decisive)
		m.WinRateCI = wilsonHalfWidth95(m.Wins, decisive)
	}
	m.Preliminary = m.Games < prelimMinGames

	// Wall-clock per match, over matches that actually contributed a usable clock.
	if m.TimedMatches > 0 {
		m.AvgMatchSeconds = m.PlaySeconds / float64(m.TimedMatches)
	}
	// Economics are per MATCH (every match burns tokens), outcomes are per GAME.
	if m.Matches > 0 {
		m.TokensPerMatch = float64(m.Tokens) / float64(m.Matches)
		m.DecisionsPerMatch = float64(m.Decisions) / float64(m.Matches)
		m.CostPerMatch = m.costBasis() / float64(m.Matches)
	}
	if m.Decisions > 0 {
		m.TokensPerDecision = float64(m.Tokens) / float64(m.Decisions)
		m.LegalRate = float64(m.Legal) / float64(m.Decisions)
		m.FallbackRate = float64(m.Fallbacks) / float64(m.Decisions)
	}
	// Cost- and tokens-to-win: the figure that actually decides which model to run,
	// and the one a cumulative total hides. Undefined with no wins — left at 0 rather
	// than reported as infinity.
	if m.Wins > 0 {
		m.TokensPerWin = float64(m.Tokens) / float64(m.Wins)
		m.CostPerWin = m.costBasis() / float64(m.Wins)
	}
	if m.PlaySeconds > 0 && m.Tokens > 0 {
		m.TokensPerMin = float64(m.Tokens) / (m.PlaySeconds / 60)
	}
	m.Intelligence = intelligenceScore(m.Decisions, m.LegalRate, m.FallbackRate, m.AvgLatencyMs)

	for i := range m.Arenas {
		a := &m.Arenas[i]
		a.Games = a.Wins + a.Losses + a.Ties
		if decisive := a.Wins + a.Losses; decisive > 0 {
			a.WinRate = float64(a.Wins) / float64(decisive)
			a.WinRateCI = wilsonHalfWidth95(a.Wins, decisive)
		}
		if a.Matches > 0 {
			a.TokensPerMatch = float64(a.Tokens) / float64(a.Matches)
		}
		if a.Decisions > 0 {
			a.LegalRate = float64(a.LegalInternal) / float64(a.Decisions)
		}
		if a.TimedMatchesInternal > 0 {
			a.AvgMatchSeconds = a.PlaySecondsInternal / float64(a.TimedMatchesInternal)
		}
	}
}

// costBasis is the USD figure the per-match and per-win cost columns divide.
//
// Delegates to CostForRanking so this and the group rows cannot drift on a rule that decides
// a published ranking. The rule used to be "verified cost when there is any", which meant one
// routed call in a thousand made the whole row's cost-per-win 1000x too cheap while labelling
// it with the tier a reader trusts most. See CostForRanking for both directions of the attack.
func (m *ModelStat) costBasis() float64 {
	amount, basis := CostForRanking(m.VerifiedCostUSD, m.EstCostUSD, m.Verified)
	m.CostBasis = basis
	return amount
}

// intelligenceScore is the 0..1000 quality composite, using the SAME weights and
// thresholds as the P-Index intelligence dimension (migration 0051).
//
// Returns 0 below intelMinDecisions: a model that played three turns must not be able
// to top a public leaderboard on a lucky run, and the UI renders 0 as "not enough
// decisions to score" rather than as a score of zero.
func intelligenceScore(decisions int64, legalRate, fallbackRate float64, avgLatencyMs int) int {
	if decisions < intelMinDecisions {
		return 0
	}
	speed := 1.0
	if lat := float64(avgLatencyMs); lat > intelLatencyFast {
		if lat >= intelLatencySlow {
			speed = 0
		} else {
			speed = 1 - (lat-intelLatencyFast)/(intelLatencySlow-intelLatencyFast)
		}
	}
	score := intelWLegal*clamp01(legalRate) +
		intelWReliability*clamp01(1-fallbackRate) +
		intelWSpeed*speed
	return int(clamp01(score) * intelScale)
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// Standing returns an agent's rank + totals for the current season.
//
// With no arena named, this reports the agent's PRIMARY arena — the one it has played
// most this season — and says which in Standing.Game. It used to silently answer for
// Goofspiel, so an agent that had only ever played Mafia was told it was unranked.
func (s *Service) Standing(ctx context.Context, agentPublicID, game string) (Standing, bool, error) {
	if game == ArenaAll {
		game = ""
	}
	return s.repo.AgentStanding(ctx, s.CurrentSeason(), game, agentPublicID)
}

// ── metrics ──────────────────────────────────────────────────────────────────

type metrics struct {
	eloUpdates prometheus.Counter
	// ratingsVoided counts matches where integrity removed at least one seat from the
	// rating update, or removed enough that nothing could be rated.
	//
	// Published as a counter because the RATE is the interesting number and the platform
	// has already learned that exclusion numerators without denominators cannot be turned
	// into a rate by a reader. Divide by elo_updates_total plus this.
	ratingsVoided prometheus.Counter
}

func newMetrics(reg *prometheus.Registry) *metrics {
	m := &metrics{
		eloUpdates: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "elo_updates_total", Help: "Matches whose ELO change was applied.",
		}),
		ratingsVoided: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "rating_integrity_voided_total",
			Help: "Matches where integrity removed a seat from, or voided, the rating update.",
		}),
	}
	reg.MustRegister(m.eloUpdates, m.ratingsVoided)
	return m
}

// arenaRecords extracts per-arena decisive results for normalisation.
//
// The all-arena page carries a per-arena breakdown; a single-arena page does not, and there
// the row's own totals ARE that arena's results. A cross-arena row with no breakdown cannot
// be normalised at all and returns nothing, which Compute reports as not Comparable — the
// honest outcome, since pooling it is exactly the bug being fixed.
func arenaRecords(m ModelStat, pageGame string) []arenanorm.Record {
	if len(m.Arenas) > 0 {
		out := make([]arenanorm.Record, 0, len(m.Arenas))
		for _, a := range m.Arenas {
			out = append(out, arenanorm.Record{Arena: a.Game, Wins: a.Wins, Losses: a.Losses})
		}
		return out
	}
	if pageGame == "" {
		return nil
	}
	return []arenanorm.Record{{Arena: pageGame, Wins: m.Wins, Losses: m.Losses}}
}

// orderModels normalises and sorts a benchmark page in place.
//
// Extracted from benchmarkPage so the ordering can be tested without a database. The
// ordering rule is the part most likely to be quietly wrong, and it was: see the comment
// inside for what it replaced.
func orderModels(out []ModelStat, game string) {
	// Best first, ordered by arena-normalised performance.
	//
	// Intelligence USED to be the primary key, for a reason that was half right: it was
	// the only figure normalised across arenas, since avg ELO is not comparable between a
	// Glicko 1v1 arena and a TrueSkill N-player one. But Intelligence is
	// 0.4*legal + 0.4*(1-fallback) + 0.2*speed, which contains NO information about
	// winning. Two agents that both play legally with no fallbacks both score
	// 800 + 200*speed, so the order was decided entirely by latency — on a page titled
	// "which model wins on Pyyol", with the win-rate column underneath contradicting it.
	// The latency band made it worse: 500/8000ms against a measured p50 of 6,988ms
	// (migration 0078) scores the median honest agent 0.135.
	//
	// The replacement keeps the property that motivated the original choice — cross-arena
	// comparability — and adds the one it lacked. Each arena's rate is lifted over that
	// arena's own MEASURED population baseline, and rows are ranked on the Wilson lower
	// bound, matching modelboard and skill/ranking rather than the ladder's bare point
	// estimates. Intelligence survives as a displayed column and now only separates rows
	// whose evidence is otherwise identical.
	baseRecords := make([]arenanorm.Record, 0, len(out)*len(Arenas))
	for i := range out {
		baseRecords = append(baseRecords, arenaRecords(out[i], game)...)
	}
	baselines := arenanorm.PopulationBaselines(baseRecords)
	for i := range out {
		out[i].Normalized = arenanorm.Compute(arenaRecords(out[i], game), baselines)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		// A row with no normalisable evidence is UNMEASURED, not worst. It sinks, but it
		// is still shown, for the same reason Preliminary rows are.
		if a.Normalized.Comparable != b.Normalized.Comparable {
			return a.Normalized.Comparable
		}
		if a.Normalized.LiftLower != b.Normalized.LiftLower {
			return a.Normalized.LiftLower > b.Normalized.LiftLower
		}
		if a.Normalized.Decisive != b.Normalized.Decisive {
			return a.Normalized.Decisive > b.Normalized.Decisive
		}
		if a.Intelligence != b.Intelligence {
			return a.Intelligence > b.Intelligence
		}
		if a.AvgElo != b.AvgElo {
			return a.AvgElo > b.AvgElo
		}
		return a.Provider+"/"+a.Model < b.Provider+"/"+b.Model
	})

}
