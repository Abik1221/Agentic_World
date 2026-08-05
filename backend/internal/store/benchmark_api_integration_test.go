package store

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/rating"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// The model-benchmark API end to end: chi route → handler → service → real repo → real
// Postgres → JSON on the wire.
//
// The bug this exists to prevent shipped once already and was invisible to every layer
// of test above the database: AgentStanding referenced agent_manifests.agent_id, a
// column that does not exist, so /v1/rankings/standing answered 500 for every agent and
// the console's "your rank" card silently rendered nothing. Unit tests passed, because
// they ran against a fake repo. Only a request that reaches Postgres catches it.

func benchAPI(t *testing.T, pool *pgxpool.Pool) (*httptest.Server, *rating.Service) {
	t.Helper()
	svc := rating.New(NewRatingRepo(pool), platform.FixedClock{T: time.Now()},
		rating.Config{SeasonLength: 30 * 24 * time.Hour}, prometheus.NewRegistry())
	r := chi.NewRouter()
	// allowDevRoll=false ⇒ no authenticated routes are registered, so a nil
	// authenticator is safe and the public read paths are what we exercise.
	rating.NewHandler(svc, nil, false, nil).Register(r)
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, svc
}

func getJSON(t *testing.T, url string, into any) int {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer res.Body.Close()
	if into != nil && res.StatusCode == http.StatusOK {
		if err := json.NewDecoder(res.Body).Decode(into); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
	}
	return res.StatusCode
}

func TestBenchmarkAPILive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	srv, svc := benchAPI(t, pool)
	season := svc.CurrentSeason()

	_, run := isolate()
	model := "api-model-" + run
	agent, owner := mkUserAgent(t, pool, "bmapi-"+run)
	var ownerID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, owner).Scan(&ownerID); err != nil {
		t.Fatalf("owner: %v", err)
	}

	// One finished Mafia match this season, gateway-verified, plus the rating row that
	// carries ELO and coins.
	writeBenchFixture(t, pool, benchFixture{
		matchID: "m-api-" + run, game: "mafia", agentPub: agent, ownerID: ownerID,
		result: "win", decisions: 250, legal: 245, tokens: 12_000, estCost: 0.12, durationSec: 90,
		verifiedProvider: "anthropic", verifiedModel: model, verifiedCost: 0.30,
	})
	writeRating(t, pool, agent, "mafia", season, 1575, 1, 0, 250)

	// ── the board, all arenas (the default) ─────────────────────────────────────
	var page struct {
		Season      int        `json:"season"`
		Game        string     `json:"game"`
		SeasonStart string     `json:"season_start"`
		MinGames    int        `json:"min_games"`
		Arenas      []string   `json:"arenas"`
		Models      []apiModel `json:"models"`
		Groups      []apiGroup `json:"groups"`
	}
	if code := getJSON(t, srv.URL+"/v1/benchmark/models", &page); code != 200 {
		t.Fatalf("GET /v1/benchmark/models = %d, want 200", code)
	}
	if page.Game != rating.ArenaAll {
		t.Errorf("game = %q, want %q — the default must be every arena", page.Game, rating.ArenaAll)
	}
	if page.Season != season {
		t.Errorf("season = %d, want %d", page.Season, season)
	}
	if page.SeasonStart == "" {
		t.Error("season_start must be published so a reader can tell what window the figures cover")
	}
	if len(page.Arenas) != 3 {
		t.Errorf("arenas = %v, want the three rated arenas", page.Arenas)
	}

	var row *apiModel
	for i := range page.Models {
		if page.Models[i].Model == model {
			row = &page.Models[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("model %q missing from the board", model)
	}

	// The whole metric set the UI renders must arrive populated — a column that is
	// always blank on the wire is a column the page cannot show.
	if row.Attribution != rating.AttrVerified {
		t.Errorf("attribution = %q, want %q", row.Attribution, rating.AttrVerified)
	}
	if row.Matches != 1 || row.Games != 1 || row.Wins != 1 || row.WinRate != 1 {
		t.Errorf("outcomes = %d matches / %d games / %dW / %.2f", row.Matches, row.Games, row.Wins, row.WinRate)
	}
	if row.WinRateCI <= 0 {
		t.Error("win_rate_ci must be non-zero — a 1-from-1 record is not proof of a 100% win rate")
	}
	if !row.Preliminary {
		t.Error("a one-game row must be tagged preliminary")
	}
	if row.AvgElo != 1575 || row.CoinsWon != 250 {
		t.Errorf("elo=%d coins=%d, want 1575/250 from the ratings row", row.AvgElo, row.CoinsWon)
	}
	if row.Tokens != 12_000 || row.TokensPerMatch != 12_000 || row.TokensPerWin != 12_000 {
		t.Errorf("tokens=%d perMatch=%.0f perWin=%.0f", row.Tokens, row.TokensPerMatch, row.TokensPerWin)
	}
	if row.AvgMatchSeconds < 89 || row.AvgMatchSeconds > 91 {
		t.Errorf("avg_match_seconds = %.1f, want ~90", row.AvgMatchSeconds)
	}
	if row.AvgLatencyMs != 400 {
		t.Errorf("avg_latency_ms = %d, want 400", row.AvgLatencyMs)
	}
	if row.LegalRate < 0.979 || row.LegalRate > 0.981 {
		t.Errorf("legal_rate = %.4f, want 245/250", row.LegalRate)
	}
	// 250 decisions clears the intelligence floor, so this row must carry a score.
	if row.Intelligence <= 0 {
		t.Errorf("intelligence = %d, want a score at 250 decisions", row.Intelligence)
	}
	if row.CostBasis != rating.CostVerified {
		t.Errorf("cost_basis = %q, want %q", row.CostBasis, rating.CostVerified)
	}
	if row.CostPerWin < 0.29 || row.CostPerWin > 0.31 {
		t.Errorf("cost_per_win = %.4f, want the gateway-measured 0.30", row.CostPerWin)
	}
	if len(row.Arenas) != 1 || row.Arenas[0].Game != "mafia" || row.Arenas[0].Tokens != 12_000 {
		t.Errorf("arena breakdown = %+v, want one mafia row", row.Arenas)
	}

	// ── classification travels on the wire ──────────────────────────────────────
	// The fixture is a verified Anthropic call, so the row must name Anthropic as the
	// VENDOR, mark it proprietary, and mark it hosted.
	if row.Class.Vendor != "unknown" && row.Class.Vendor != "anthropic" {
		t.Errorf("class.vendor = %q, want anthropic (or unknown for an unrecognised name)", row.Class.Vendor)
	}
	if row.Class.Provider != "anthropic" {
		t.Errorf("class.provider = %q, want anthropic", row.Class.Provider)
	}
	if row.Class.Hosting != "hosted" {
		t.Errorf("class.hosting = %q, want hosted", row.Class.Hosting)
	}

	// ── groups are published and reconcile against the models ───────────────────
	if len(page.Groups) == 0 {
		t.Fatal("no comparison groups on the wire — the compare-by view would be empty")
	}
	kinds := map[string]bool{}
	for _, g := range page.Groups {
		kinds[g.Kind] = true
	}
	for _, want := range []string{"provider", "vendor", "openness", "hosting", "family"} {
		if !kinds[want] {
			t.Errorf("no %q groups published; got kinds %v", want, kinds)
		}
	}
	// Summing any single axis must equal the whole board, or the two disagree.
	for _, kind := range []string{"provider", "vendor", "openness", "hosting", "family"} {
		var matches int
		for _, g := range page.Groups {
			if g.Kind == kind {
				matches += g.Matches
			}
		}
		var total int
		for _, m := range page.Models {
			total += m.Matches
		}
		if matches != total {
			t.Errorf("axis %q sums to %d matches but the board shows %d", kind, matches, total)
		}
	}
	// The fixture's provider group must exist and name the model it pooled.
	var found bool
	for _, g := range page.Groups {
		if g.Kind == "provider" && g.Key == "anthropic" {
			found = true
			if g.Models < 1 {
				t.Errorf("anthropic provider group pooled %d models", g.Models)
			}
		}
	}
	if !found {
		t.Error("the verified anthropic call produced no anthropic provider group")
	}

	// ── narrowing, and rejecting a typo ─────────────────────────────────────────
	if code := getJSON(t, srv.URL+"/v1/benchmark/models?game=mafia", nil); code != 200 {
		t.Errorf("?game=mafia = %d, want 200", code)
	}
	// A misspelled arena must 400, not answer with an empty board that reads as "no
	// model has played".
	if code := getJSON(t, srv.URL+"/v1/benchmark/models?game=goofspeil", nil); code != http.StatusBadRequest {
		t.Errorf("?game=goofspeil = %d, want 400", code)
	}

	// ── the standing endpoint: the query that used to 500 for everyone ───────────
	var st struct {
		Game  string `json:"game"`
		Rank  int    `json:"rank"`
		Total int    `json:"total"`
		Elo   int    `json:"elo"`
		Wins  int    `json:"wins"`
	}
	// No ?game= — the backend must resolve the agent's most-played arena. This agent has
	// only played Mafia, which the old Goofspiel default would have reported as unranked.
	if code := getJSON(t, srv.URL+"/v1/rankings/standing?agent="+agent, &st); code != 200 {
		t.Fatalf("GET /v1/rankings/standing (no arena) = %d, want 200 — this is the 500 regression", code)
	}
	if st.Game != "mafia" {
		t.Errorf("standing arena = %q, want mafia (the agent's only arena)", st.Game)
	}
	if st.Rank < 1 || st.Total < 1 || st.Elo != 1575 || st.Wins != 1 {
		t.Errorf("standing = %+v", st)
	}

	// An agent with no rating row is unranked (404), not an error.
	if code := getJSON(t, srv.URL+"/v1/rankings/standing?agent=ag_definitely_not_real", nil); code != http.StatusNotFound {
		t.Errorf("unranked agent = %d, want 404", code)
	}
	// A missing agent param is a 400.
	if code := getJSON(t, srv.URL+"/v1/rankings/standing", nil); code != http.StatusBadRequest {
		t.Errorf("missing agent = %d, want 400", code)
	}
}

// apiModel mirrors the ModelStat JSON the board publishes — declared here rather than
// reusing rating.ModelStat so a field silently dropped from the wire contract fails
// this test instead of decoding into the struct that defined it.
type apiModel struct {
	Provider        string  `json:"provider"`
	Model           string  `json:"model"`
	Attribution     string  `json:"attribution"`
	Agents          int     `json:"agents"`
	Developers      int     `json:"developers"`
	Matches         int     `json:"matches"`
	Games           int     `json:"games"`
	Wins            int     `json:"wins"`
	Losses          int     `json:"losses"`
	WinRate         float64 `json:"win_rate"`
	WinRateCI       float64 `json:"win_rate_ci"`
	Preliminary     bool    `json:"preliminary"`
	AvgElo          int     `json:"avg_elo"`
	CoinsWon        int64   `json:"coins_won"`
	Tokens          int64   `json:"tokens"`
	TokensPerMatch  float64 `json:"tokens_per_match"`
	TokensPerWin    float64 `json:"tokens_per_win"`
	AvgMatchSeconds float64 `json:"avg_match_seconds"`
	AvgLatencyMs    int     `json:"avg_latency_ms"`
	LegalRate       float64 `json:"legal_rate"`
	Intelligence    int     `json:"intelligence"`
	CostPerWin      float64 `json:"cost_per_win"`
	CostBasis       string  `json:"cost_basis"`
	VerifiedCostUSD float64 `json:"verified_cost_usd"`
	Class           struct {
		Provider string `json:"provider"`
		Vendor   string `json:"vendor"`
		Family   string `json:"family"`
		Openness string `json:"openness"`
		Hosting  string `json:"hosting"`
	} `json:"class"`
	Arenas []struct {
		Game    string `json:"game"`
		Matches int    `json:"matches"`
		Wins    int    `json:"wins"`
		Tokens  int64  `json:"tokens"`
	} `json:"arenas"`
}

// apiGroup mirrors the GroupStat JSON the compare-by view renders.
type apiGroup struct {
	Kind        string   `json:"kind"`
	Key         string   `json:"key"`
	Models      int      `json:"models"`
	Matches     int      `json:"matches"`
	Games       int      `json:"games"`
	Wins        int      `json:"wins"`
	WinRate     float64  `json:"win_rate"`
	WinRateCI   float64  `json:"win_rate_ci"`
	Preliminary bool     `json:"preliminary"`
	Tokens      int64    `json:"tokens"`
	TopModels   []string `json:"top_models"`
}

// The model DETAIL endpoint end to end, including the adoption counts and the "who
// runs this" list. Both are new SQL: the developers count is a DISTINCT over agent
// owners inside the aggregate, and the runners list is a second query that has to
// resolve attribution exactly as the board did or the page would list agents the board
// credited to a different model.
func TestModelDetailAPILive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	srv, svc := benchAPI(t, pool)
	season := svc.CurrentSeason()

	_, run := isolate()
	model := "detail-model-" + run

	// TWO agents owned by ONE developer, plus a THIRD owned by someone else. Adoption
	// must report 2 developers and 3 agents — reporting agents as popularity would let
	// one prolific account look like a trend.
	ownerA := "u_detailA-" + run
	var ownerAID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (public_id, username) VALUES ($1, $2)
		 ON CONFLICT (public_id) DO UPDATE SET updated_at = now() RETURNING id`,
		ownerA, "devA"+run[len(run)-6:]).Scan(&ownerAID); err != nil {
		t.Fatalf("owner A: %v", err)
	}
	mkAgentFor := func(pub string, uid int64) string {
		if _, err := pool.Exec(ctx,
			`INSERT INTO agents (public_id, owner_user_id, name, slug) VALUES ($1,$2,$3,$4)
			 ON CONFLICT (public_id) DO NOTHING`, pub, uid, "Agent "+pub, "slug-"+pub); err != nil {
			t.Fatalf("agent %s: %v", pub, err)
		}
		return pub
	}
	a1 := mkAgentFor("ag_d1-"+run, ownerAID)
	a2 := mkAgentFor("ag_d2-"+run, ownerAID)
	a3, ownerB := mkUserAgent(t, pool, "d3-"+run)
	var ownerBID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, ownerB).Scan(&ownerBID); err != nil {
		t.Fatalf("owner B: %v", err)
	}

	seat := func(agent string, owner int64, i int, result string) {
		writeBenchFixture(t, pool, benchFixture{
			matchID: fmt.Sprintf("m-d-%s-%s-%d", run, agent, i), game: "mafia",
			agentPub: agent, ownerID: owner, result: result,
			decisions: 100, legal: 99, tokens: 5_000, estCost: 0.05, durationSec: 45,
			verifiedProvider: "anthropic", verifiedModel: model, verifiedCost: 0.10,
		})
	}
	seat(a1, ownerAID, 0, "win")
	seat(a1, ownerAID, 1, "win")
	seat(a2, ownerAID, 0, "loss")
	seat(a3, ownerBID, 0, "win")
	for _, a := range []string{a1, a2, a3} {
		writeRating(t, pool, a, "mafia", season, 1520, 1, 0, 10)
	}

	var d struct {
		Season  int      `json:"season"`
		Game    string   `json:"game"`
		Rank    int      `json:"rank"`
		Total   int      `json:"total"`
		Model   apiModel `json:"model"`
		Runners []struct {
			Agent     string  `json:"agent"`
			AgentName string  `json:"agent_name"`
			Username  string  `json:"username"`
			Matches   int     `json:"matches"`
			Wins      int     `json:"wins"`
			WinRate   float64 `json:"win_rate"`
			WinRateCI float64 `json:"win_rate_ci"`
			Elo       int     `json:"elo"`
			Tokens    int64   `json:"tokens"`
		} `json:"runners"`
	}
	url := srv.URL + "/v1/benchmark/model?provider=anthropic&model=" + model
	if code := getJSON(t, url, &d); code != 200 {
		t.Fatalf("GET /v1/benchmark/model = %d, want 200", code)
	}

	// ── adoption: people, not agents ────────────────────────────────────────────
	if d.Model.Developers != 2 {
		t.Errorf("developers = %d, want 2 (three agents, two owners)", d.Model.Developers)
	}
	if d.Model.Agents != 3 {
		t.Errorf("agents = %d, want 3", d.Model.Agents)
	}
	if d.Model.Matches != 4 {
		t.Errorf("matches = %d, want 4", d.Model.Matches)
	}
	if d.Model.Wins != 3 || d.Model.Losses != 1 {
		t.Errorf("record = %dW/%dL, want 3/1", d.Model.Wins, d.Model.Losses)
	}
	if d.Rank < 1 || d.Total < 1 {
		t.Errorf("rank = %d of %d", d.Rank, d.Total)
	}

	// ── the runners list ────────────────────────────────────────────────────────
	if len(d.Runners) != 3 {
		t.Fatalf("runners = %d, want one row per agent: %+v", len(d.Runners), d.Runners)
	}
	// Most active first.
	if d.Runners[0].Agent != a1 || d.Runners[0].Matches != 2 {
		t.Errorf("first runner = %s with %d matches, want %s with 2", d.Runners[0].Agent, d.Runners[0].Matches, a1)
	}
	if d.Runners[0].WinRate != 1 || d.Runners[0].WinRateCI <= 0 {
		t.Errorf("runner rate = %.2f ±%.3f, want 1.0 with a real interval", d.Runners[0].WinRate, d.Runners[0].WinRateCI)
	}
	if d.Runners[0].Elo != 1520 {
		t.Errorf("runner elo = %d, want 1520 from the ratings row", d.Runners[0].Elo)
	}
	if d.Runners[0].Tokens != 10_000 {
		t.Errorf("runner tokens = %d, want 2 matches × 5000", d.Runners[0].Tokens)
	}
	// Runner matches must sum to the model's match count, or the two disagree.
	var sum int
	for _, r := range d.Runners {
		sum += r.Matches
	}
	if sum != d.Model.Matches {
		t.Errorf("runners sum to %d matches but the model says %d", sum, d.Model.Matches)
	}

	// ── errors ──────────────────────────────────────────────────────────────────
	if code := getJSON(t, srv.URL+"/v1/benchmark/model?provider=anthropic&model=nope-"+run, nil); code != http.StatusNotFound {
		t.Errorf("unknown model = %d, want 404", code)
	}
	if code := getJSON(t, srv.URL+"/v1/benchmark/model?provider=anthropic", nil); code != http.StatusBadRequest {
		t.Errorf("missing model = %d, want 400", code)
	}
	if code := getJSON(t, url+"&game=goofspeil", nil); code != http.StatusBadRequest {
		t.Errorf("typo arena = %d, want 400", code)
	}
	// Narrowing to the arena it played still resolves.
	if code := getJSON(t, url+"&game=mafia", nil); code != 200 {
		t.Errorf("?game=mafia = %d, want 200", code)
	}
}
