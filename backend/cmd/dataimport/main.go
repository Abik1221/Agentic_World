// Command dataimport moves gateway-VERIFIED play from one arena database into
// another, so a fresh deployment starts with a record instead of empty boards.
//
// # What crosses, and what does not
//
// The selection rule is the platform's own definition of proven play, not a
// convenient approximation of it. A match is exported only when, for that exact
// match, some SEATED non-house agent made a model call the gateway BOUND to a
// decision and which named a model. All four conditions are load-bearing:
//
//   - seated: the arena's lab database holds bound calls whose agent was never a
//     player in the match they name — integration fixtures, written directly.
//     Publishing those as proof would attribute play that did not happen.
//   - bound: an unbound call carries an unverified match/round header. It proves
//     a model was called, not that it was called FOR this decision.
//   - a named model: the same fact the model board's `no_verified_model`
//     exclusion reads, so "verified" means one thing across every surface.
//   - non-house: the house never stakes and is not a competitor.
//
// Everything else is left behind. That is the point: an importer that carried
// whatever it found would populate the boards with the arena's own test traffic,
// which is exactly what the exclusion census exists to keep off them.
//
// # Why the decision SCORES do not cross
//
// Every historical decision row paired its action with the state that action
// PRODUCED rather than the state it was chosen from (fixed in internal/monopoly
// and internal/mafia; see the commit that added marshalDecisionView). The stored
// view is therefore the wrong half of the pair, and the regret values computed
// from it describe positions that never occurred. Rescoring in the target would
// re-derive wrong answers from the same wrong input, so the history cannot be
// repaired by moving it.
//
// Decisions still cross, because their action, latency, token counts and cost are
// state-INDEPENDENT and correct — that is what the LLM-economics surfaces read.
// input_json, skill_regret, skill_best and skill_scorer_version are dropped.
// Decision QUALITY starts accumulating from the first match played after the fix
// deploys, and until then the dimension is honestly empty.
//
// # Why nothing here touches money
//
// No ledger, no balances, no escrow, no holds. Those tables carry conservation
// invariants that the escrow audit and ledger reconciliation workers enforce, and
// CLAUDE.md is explicit that a ledger is never auto-repaired. `ratings.coins_earned`
// DOES cross, because it is a per-season display statistic rather than a claim on
// funds — it is what an agent won at the tables it played, and the boards read it
// directly.
//
// # Usage
//
//	dataimport -mode=export -dsn=$SOURCE_DATABASE_URL -out=bundle.json
//	dataimport -mode=import -dsn=$DATABASE_URL -in=bundle.json [-dry-run]
//
// Import is IDEMPOTENT: every write is an upsert on a natural key (public_id, or
// the composite the table already declares), so re-running converges instead of
// duplicating. Run it twice and the second run changes nothing — which is the
// property that makes it safe to put behind a CI button somebody may press twice.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Bundle is the wire format. Plain JSON rather than a pg_dump so the contents are
// reviewable before anyone applies them to a live database — the whole file can be
// read, diffed and argued with.
type Bundle struct {
	// GeneratedAt is stamped by the exporter. Informational; nothing keys on it.
	GeneratedAt time.Time `json:"generated_at"`
	// Note travels with the data so a reader of the file learns what it is
	// without having to find this source.
	Note string `json:"note"`

	Users      []User      `json:"users"`
	Agents     []Agent     `json:"agents"`
	Matches    []Match     `json:"matches"`
	Seats      []Seat      `json:"seats"`
	Ratings    []Rating    `json:"ratings"`
	RatingChgs []RatingChg `json:"rating_changes"`
	ModelCalls []ModelCall `json:"model_calls"`
	Benchmarks []Benchmark `json:"benchmarks"`
	Decisions  []Decision  `json:"decisions"`
}

// Identity only. No email, no password hash, no wallet, no Stripe id, no TOTP
// secret — none of it is needed to render a public profile, and copying
// credentials between databases is how one environment's breach becomes another's.
type User struct {
	PublicID    string    `json:"public_id"`
	Username    string    `json:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Country     string    `json:"country,omitempty"`
	Segment     string    `json:"segment"`
	CreatedAt   time.Time `json:"created_at"`
}

type Agent struct {
	PublicID    string    `json:"public_id"`
	OwnerUserID string    `json:"owner_user_public_id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	AvatarURL   string    `json:"avatar_url,omitempty"`
	Bio         string    `json:"bio,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type Match struct {
	PublicID      string     `json:"public_id"`
	Game          string     `json:"game"`
	Status        string     `json:"status"`
	Bid           int64      `json:"bid"`
	RakePct       int        `json:"rake_pct"`
	TotalRounds   int        `json:"total_rounds"`
	EngineVersion string     `json:"engine_version"`
	SeedCommit    string     `json:"prize_seed_commit"`
	FairnessMode  string     `json:"fairness_mode"`
	Mode          string     `json:"mode"`
	Rated         bool       `json:"rated"`
	CreatorUser   string     `json:"creator_owner_user_public_id"`
	WinnerAgent   string     `json:"winner_agent_public_id,omitempty"`
	ReplayHash    string     `json:"replay_hash,omitempty"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Seat struct {
	MatchPublicID string `json:"match_public_id"`
	AgentPublicID string `json:"agent_public_id"`
	Seat          int    `json:"seat"`
}

type Rating struct {
	AgentPublicID string  `json:"agent_public_id"`
	Game          string  `json:"game"`
	Season        int     `json:"season"`
	Algo          string  `json:"algo"`
	Elo           int     `json:"elo"`
	RD            float64 `json:"rd"`
	Vol           float64 `json:"vol"`
	Mu            float64 `json:"mu"`
	Sigma         float64 `json:"sigma"`
	Wins          int     `json:"wins"`
	Losses        int     `json:"losses"`
	Ties          int     `json:"ties"`
	CoinsEarned   int64   `json:"coins_earned"`
	CurrentStreak int     `json:"current_streak"`
	BestStreak    int     `json:"best_streak"`
}

type RatingChg struct {
	MatchPublicID string    `json:"match_public_id"`
	AgentPublicID string    `json:"agent_public_id"`
	Game          string    `json:"game"`
	Season        int       `json:"season"`
	RatingBefore  int       `json:"rating_before"`
	RatingAfter   int       `json:"rating_after"`
	RatingDelta   int       `json:"rating_delta"`
	RankInMatch   int       `json:"rank_in_match"`
	CreatedAt     time.Time `json:"created_at"`
}

// ModelCall is the PROOF. Only bound calls naming a model are exported — an
// unbound one proves nothing about which decision it belonged to.
type ModelCall struct {
	AgentPublicID     string    `json:"agent_public_id"`
	MatchID           string    `json:"match_id"`
	Round             int       `json:"round"`
	Provider          string    `json:"provider"`
	Model             string    `json:"model"`
	PromptTokens      int       `json:"prompt_tokens"`
	CompletionTokens  int       `json:"completion_tokens"`
	CachedReadTokens  int       `json:"cached_read_tokens"`
	CachedWriteTokens int       `json:"cached_write_tokens"`
	ReasoningTokens   int       `json:"reasoning_tokens"`
	LatencyMS         int64     `json:"latency_ms"`
	Status            int       `json:"status"`
	Streamed          bool      `json:"streamed"`
	CreatedAt         time.Time `json:"created_at"`
}

type Benchmark struct {
	MatchID          string  `json:"match_id"`
	AgentPublicID    string  `json:"agent_public_id"`
	Game             string  `json:"game"`
	Result           string  `json:"result"`
	Decisions        int     `json:"decisions"`
	Legal            int     `json:"legal"`
	Fallbacks        int     `json:"fallbacks"`
	LatencySumMS     int64   `json:"latency_sum_ms"`
	PromptTokens     int64   `json:"prompt_tokens"`
	CompletionTokens int64   `json:"completion_tokens"`
	ReasoningTokens  int64   `json:"reasoning_tokens"`
	CachedTokens     int64   `json:"cached_tokens"`
	Tokens           int64   `json:"tokens"`
	EstimatedCost    float64 `json:"estimated_cost"`
	Illegal          int     `json:"illegal"`
	Timeouts         int     `json:"timeouts"`
	TransportErrors  int     `json:"transport_errors"`
	// The attribution ladder, carried in full. `observed_*` is what the gateway
	// SAW; `declared_*` is what the agent said about itself. Collapsing them into
	// one pair would throw away the distinction the model board ranks on.
	ObservedProvider string    `json:"observed_provider"`
	ObservedModel    string    `json:"observed_model"`
	DeclaredProvider string    `json:"declared_provider"`
	DeclaredModel    string    `json:"declared_model"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Decision deliberately carries NO input_json and NO skill_* fields. See the
// package comment: the stored view is the post-move state, so those columns
// describe a position the action was not chosen from.
type Decision struct {
	MatchID          string    `json:"match_id"`
	AgentPublicID    string    `json:"agent_public_id"`
	Seq              int       `json:"seq"`
	Round            int       `json:"round"`
	Action           string    `json:"action"`
	Outcome          string    `json:"outcome"`
	LatencyMS        int64     `json:"latency_ms"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	ReasoningTokens  int       `json:"reasoning_tokens"`
	CachedTokens     int       `json:"cached_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	EstimatedCost    float64   `json:"estimated_cost"`
	CreatedAt        time.Time `json:"created_at"`
}

// verifiedMatchesCTE is the single definition of "a match worth publishing",
// written once and reused by every export query so no two of them can select a
// different population. $1 is the season filter (0 = every season).
const verifiedMatchesCTE = `
WITH verified AS (
    SELECT DISTINCT m.id, m.public_id
      FROM matches m
      JOIN match_players mp        ON mp.match_id = m.id
      JOIN agents a                ON a.id = mp.agent_id AND a.kind <> 'house'
      JOIN agent_model_calls mc    ON mc.match_id = m.public_id
                                  AND mc.agent_id = mp.agent_id
                                  AND mc.bound
                                  AND COALESCE(mc.model,'') <> ''
     WHERE m.status = 'finished'
)`

func main() {
	mode := flag.String("mode", "", "export | import")
	dsn := flag.String("dsn", "", "database URL (defaults to $DATABASE_URL)")
	out := flag.String("out", "bundle.json", "export: file to write")
	in := flag.String("in", "bundle.json", "import: file to read")
	dryRun := flag.Bool("dry-run", false, "import: roll back instead of committing, and report what would change")
	firstParty := flag.Bool("first-party", true, "import: mark imported agents as platform-operated")
	flag.Parse()

	if *dsn == "" {
		*dsn = os.Getenv("DATABASE_URL")
	}
	if *dsn == "" {
		fatal(errors.New("no -dsn and no DATABASE_URL"))
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, *dsn)
	if err != nil {
		fatal(fmt.Errorf("connect: %w", err))
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		fatal(fmt.Errorf("ping: %w", err))
	}

	switch *mode {
	case "export":
		b, err := export(ctx, pool)
		if err != nil {
			fatal(err)
		}
		f, err := os.Create(*out)
		if err != nil {
			fatal(err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		if err := enc.Encode(b); err != nil {
			fatal(err)
		}
		summarize("exported", b, *out)
	case "import":
		raw, err := os.ReadFile(*in)
		if err != nil {
			fatal(err)
		}
		var b Bundle
		if err := json.Unmarshal(raw, &b); err != nil {
			fatal(fmt.Errorf("parse %s: %w", *in, err))
		}
		if err := importBundle(ctx, pool, &b, *dryRun, *firstParty); err != nil {
			fatal(err)
		}
		verb := "imported"
		if *dryRun {
			verb = "would import (dry run, rolled back)"
		}
		summarize(verb, &b, *in)
	default:
		fatal(errors.New("-mode must be export or import"))
	}
}

func summarize(verb string, b *Bundle, file string) {
	fmt.Printf("%s from %s:\n", verb, file)
	fmt.Printf("  users            %d\n", len(b.Users))
	fmt.Printf("  agents           %d\n", len(b.Agents))
	fmt.Printf("  matches          %d\n", len(b.Matches))
	fmt.Printf("  seats            %d\n", len(b.Seats))
	fmt.Printf("  ratings          %d\n", len(b.Ratings))
	fmt.Printf("  rating changes   %d\n", len(b.RatingChgs))
	fmt.Printf("  bound model calls%d\n", len(b.ModelCalls))
	fmt.Printf("  benchmarks       %d\n", len(b.Benchmarks))
	fmt.Printf("  decisions        %d (scores deliberately absent)\n", len(b.Decisions))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "dataimport:", err)
	os.Exit(1)
}

func export(ctx context.Context, db *pgxpool.Pool) (*Bundle, error) {
	b := &Bundle{
		GeneratedAt: time.Now().UTC(),
		Note: "Gateway-verified play only: every match here had a seated, non-house agent " +
			"make a model call the gateway bound to a decision, naming a model. Decision " +
			"scores are deliberately absent — the stored views they were computed from are " +
			"the post-move state. No ledger, balance or escrow data is included.",
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT DISTINCT u.public_id, COALESCE(u.username::text,''), COALESCE(u.display_name,''),
		       COALESCE(u.avatar_url,''), COALESCE(u.country,''), u.segment, u.created_at
		  FROM verified v
		  JOIN match_players mp ON mp.match_id = v.id
		  JOIN agents a ON a.id = mp.agent_id AND a.kind <> 'house'
		  JOIN users u  ON u.id = a.owner_user_id`,
		&b.Users, func(r pgx.Row, x *User) error {
			return r.Scan(&x.PublicID, &x.Username, &x.DisplayName, &x.AvatarURL,
				&x.Country, &x.Segment, &x.CreatedAt)
		}); err != nil {
		return nil, fmt.Errorf("users: %w", err)
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT DISTINCT a.public_id, u.public_id, a.slug, a.name, a.kind, a.status,
		       COALESCE(a.avatar_url,''), COALESCE(a.bio,''), a.created_at
		  FROM verified v
		  JOIN match_players mp ON mp.match_id = v.id
		  JOIN agents a ON a.id = mp.agent_id AND a.kind <> 'house'
		  JOIN users u  ON u.id = a.owner_user_id`,
		&b.Agents, func(r pgx.Row, x *Agent) error {
			return r.Scan(&x.PublicID, &x.OwnerUserID, &x.Slug, &x.Name, &x.Kind, &x.Status,
				&x.AvatarURL, &x.Bio, &x.CreatedAt)
		}); err != nil {
		return nil, fmt.Errorf("agents: %w", err)
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT m.public_id, m.game, m.status, m.bid, m.rake_pct, m.total_rounds,
		       m.engine_version, m.prize_seed_commit, m.fairness_mode, m.mode, m.rated,
		       cu.public_id, COALESCE(wa.public_id,''), COALESCE(m.replay_hash,''),
		       m.started_at, m.finished_at, m.created_at
		  FROM verified v
		  JOIN matches m ON m.id = v.id
		  JOIN users cu  ON cu.id = m.creator_owner_user_id
		  LEFT JOIN agents wa ON wa.id = m.winner_agent_id`,
		&b.Matches, func(r pgx.Row, x *Match) error {
			return r.Scan(&x.PublicID, &x.Game, &x.Status, &x.Bid, &x.RakePct, &x.TotalRounds,
				&x.EngineVersion, &x.SeedCommit, &x.FairnessMode, &x.Mode, &x.Rated,
				&x.CreatorUser, &x.WinnerAgent, &x.ReplayHash, &x.StartedAt, &x.FinishedAt,
				&x.CreatedAt)
		}); err != nil {
		return nil, fmt.Errorf("matches: %w", err)
	}

	// Seats include HOUSE agents deliberately: a match with a seat missing is a
	// match whose player count does not add up, and the replay would misrender.
	// The house is a participant even though it is never ranked.
	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT m.public_id, a.public_id, mp.seat
		  FROM verified v
		  JOIN matches m ON m.id = v.id
		  JOIN match_players mp ON mp.match_id = m.id
		  JOIN agents a ON a.id = mp.agent_id`,
		&b.Seats, func(r pgx.Row, x *Seat) error {
			return r.Scan(&x.MatchPublicID, &x.AgentPublicID, &x.Seat)
		}); err != nil {
		return nil, fmt.Errorf("seats: %w", err)
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT DISTINCT a.public_id, rt.game, rt.season, rt.algo, rt.elo, rt.rd, rt.vol,
		       rt.mu, rt.sigma, rt.wins, rt.losses, rt.ties, rt.coins_earned,
		       rt.current_streak, rt.best_streak
		  FROM verified v
		  JOIN match_players mp ON mp.match_id = v.id
		  JOIN agents a  ON a.id = mp.agent_id AND a.kind <> 'house'
		  JOIN ratings rt ON rt.agent_id = a.id`,
		&b.Ratings, func(r pgx.Row, x *Rating) error {
			return r.Scan(&x.AgentPublicID, &x.Game, &x.Season, &x.Algo, &x.Elo, &x.RD, &x.Vol,
				&x.Mu, &x.Sigma, &x.Wins, &x.Losses, &x.Ties, &x.CoinsEarned,
				&x.CurrentStreak, &x.BestStreak)
		}); err != nil {
		return nil, fmt.Errorf("ratings: %w", err)
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT m.public_id, a.public_id, mrc.game, mrc.season, mrc.rating_before,
		       mrc.rating_after, mrc.rating_delta, mrc.rank_in_match, mrc.created_at
		  FROM verified v
		  JOIN matches m ON m.id = v.id
		  JOIN match_rating_changes mrc ON mrc.match_id = m.id
		  JOIN agents a ON a.id = mrc.agent_id AND a.kind <> 'house'`,
		&b.RatingChgs, func(r pgx.Row, x *RatingChg) error {
			return r.Scan(&x.MatchPublicID, &x.AgentPublicID, &x.Game, &x.Season,
				&x.RatingBefore, &x.RatingAfter, &x.RatingDelta, &x.RankInMatch, &x.CreatedAt)
		}); err != nil {
		return nil, fmt.Errorf("rating changes: %w", err)
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT a.public_id, mc.match_id, COALESCE(mc.round,0), mc.provider, mc.model,
		       mc.prompt_tokens, mc.completion_tokens, mc.cached_read_tokens,
		       mc.cached_write_tokens, mc.reasoning_tokens, mc.latency_ms, mc.status,
		       mc.streamed, mc.created_at
		  FROM verified v
		  JOIN agent_model_calls mc ON mc.match_id = v.public_id
		  JOIN agents a ON a.id = mc.agent_id AND a.kind <> 'house'
		 WHERE mc.bound AND COALESCE(mc.model,'') <> ''`,
		&b.ModelCalls, func(r pgx.Row, x *ModelCall) error {
			return r.Scan(&x.AgentPublicID, &x.MatchID, &x.Round, &x.Provider, &x.Model,
				&x.PromptTokens, &x.CompletionTokens, &x.CachedReadTokens,
				&x.CachedWriteTokens, &x.ReasoningTokens, &x.LatencyMS, &x.Status,
				&x.Streamed, &x.CreatedAt)
		}); err != nil {
		return nil, fmt.Errorf("model calls: %w", err)
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT bm.match_id, a.public_id, bm.game, bm.result, bm.decisions, bm.legal,
		       bm.fallbacks, bm.latency_sum_ms, bm.prompt_tokens, bm.completion_tokens,
		       bm.reasoning_tokens, bm.cached_tokens, bm.tokens, bm.estimated_cost,
		       bm.illegal, bm.timeouts, bm.transport_errors,
		       bm.observed_provider, bm.observed_model, bm.declared_provider,
		       bm.declared_model, bm.updated_at
		  FROM verified v
		  JOIN agent_match_benchmark bm ON bm.match_id = v.public_id
		  JOIN agents a ON a.id = bm.agent_id AND a.kind <> 'house'`,
		&b.Benchmarks, func(r pgx.Row, x *Benchmark) error {
			return r.Scan(&x.MatchID, &x.AgentPublicID, &x.Game, &x.Result, &x.Decisions,
				&x.Legal, &x.Fallbacks, &x.LatencySumMS, &x.PromptTokens,
				&x.CompletionTokens, &x.ReasoningTokens, &x.CachedTokens, &x.Tokens,
				&x.EstimatedCost, &x.Illegal, &x.Timeouts, &x.TransportErrors,
				&x.ObservedProvider, &x.ObservedModel, &x.DeclaredProvider,
				&x.DeclaredModel, &x.UpdatedAt)
		}); err != nil {
		return nil, fmt.Errorf("benchmarks: %w", err)
	}

	if err := collect(ctx, db, verifiedMatchesCTE+`
		SELECT d.match_id, a.public_id, d.seq, d.round, d.action, d.outcome, d.latency_ms,
		       d.provider, d.model, d.prompt_tokens, d.completion_tokens,
		       d.reasoning_tokens, d.cached_tokens, d.total_tokens, d.estimated_cost,
		       d.created_at
		  FROM verified v
		  JOIN agent_match_decisions d ON d.match_id = v.public_id
		  JOIN agents a ON a.id = d.agent_id AND a.kind <> 'house'`,
		&b.Decisions, func(r pgx.Row, x *Decision) error {
			return r.Scan(&x.MatchID, &x.AgentPublicID, &x.Seq, &x.Round, &x.Action,
				&x.Outcome, &x.LatencyMS, &x.Provider, &x.Model, &x.PromptTokens,
				&x.CompletionTokens, &x.ReasoningTokens, &x.CachedTokens, &x.TotalTokens,
				&x.EstimatedCost, &x.CreatedAt)
		}); err != nil {
		return nil, fmt.Errorf("decisions: %w", err)
	}

	return b, nil
}

// collect runs one query and scans every row through fn.
func collect[T any](ctx context.Context, db *pgxpool.Pool, sql string, dst *[]T, fn func(pgx.Row, *T) error) error {
	rows, err := db.Query(ctx, sql)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var x T
		if err := fn(rows, &x); err != nil {
			return err
		}
		*dst = append(*dst, x)
	}
	return rows.Err()
}

// importBundle applies the bundle in ONE transaction.
//
// All of it or none of it: a partial import leaves matches without their seats,
// or ratings for agents that do not exist, and the boards would render a record
// that does not add up. A dry run does the identical work and rolls back, so the
// rehearsal exercises every constraint the real run will.
func importBundle(ctx context.Context, db *pgxpool.Pool, b *Bundle, dryRun, firstParty bool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Users first — everything else resolves an owner or a creator through them.
	// Existing rows are UPDATED only where the incoming value is non-empty, so an
	// import can fill a blank profile but never blank a filled one.
	for _, u := range b.Users {
		if _, err := tx.Exec(ctx,
			`INSERT INTO users (public_id, username, display_name, avatar_url, country, segment, status, created_at)
			 VALUES ($1, NULLIF($2,'')::citext, NULLIF($3,''), NULLIF($4,''), NULLIF($5,''), $6, 'active', $7)
			 ON CONFLICT (public_id) DO UPDATE SET
			   username     = COALESCE(users.username, EXCLUDED.username),
			   display_name = COALESCE(NULLIF(users.display_name,''), EXCLUDED.display_name),
			   avatar_url   = COALESCE(NULLIF(users.avatar_url,''),   EXCLUDED.avatar_url),
			   country      = COALESCE(NULLIF(users.country,''),      EXCLUDED.country),
			   segment      = EXCLUDED.segment`,
			u.PublicID, u.Username, u.DisplayName, u.AvatarURL, u.Country, u.Segment, u.CreatedAt); err != nil {
			return fmt.Errorf("user %s: %w", u.PublicID, err)
		}
	}

	for _, a := range b.Agents {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agents (public_id, owner_user_id, slug, name, kind, status, avatar_url, bio, first_party, created_at)
			 SELECT $1, u.id, $3, $4, $5, $6, NULLIF($7,''), NULLIF($8,''), $9, $10
			   FROM users u WHERE u.public_id = $2
			 ON CONFLICT (public_id) DO UPDATE SET
			   name        = EXCLUDED.name,
			   status      = EXCLUDED.status,
			   avatar_url  = COALESCE(NULLIF(agents.avatar_url,''), EXCLUDED.avatar_url),
			   bio         = COALESCE(NULLIF(agents.bio,''),        EXCLUDED.bio),
			   first_party = EXCLUDED.first_party`,
			a.PublicID, a.OwnerUserID, a.Slug, a.Name, a.Kind, a.Status,
			a.AvatarURL, a.Bio, firstParty, a.CreatedAt); err != nil {
			return fmt.Errorf("agent %s: %w", a.PublicID, err)
		}
	}

	for _, m := range b.Matches {
		if _, err := tx.Exec(ctx,
			`INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
			      engine_version, prize_seed_commit, fairness_mode, mode, rated,
			      creator_owner_user_id, winner_agent_id, replay_hash,
			      started_at, finished_at, created_at)
			 SELECT $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11,
			        cu.id,
			        (SELECT id FROM agents WHERE public_id = NULLIF($13,'')),
			        NULLIF($14,''), $15, $16, $17
			   FROM users cu WHERE cu.public_id = $12
			 ON CONFLICT (public_id) DO UPDATE SET
			   status      = EXCLUDED.status,
			   winner_agent_id = EXCLUDED.winner_agent_id,
			   finished_at = EXCLUDED.finished_at`,
			m.PublicID, m.Game, m.Status, m.Bid, m.RakePct, m.TotalRounds,
			m.EngineVersion, m.SeedCommit, m.FairnessMode, m.Mode, m.Rated,
			m.CreatorUser, m.WinnerAgent, m.ReplayHash, m.StartedAt, m.FinishedAt,
			m.CreatedAt); err != nil {
			return fmt.Errorf("match %s: %w", m.PublicID, err)
		}
	}

	for _, s := range b.Seats {
		if _, err := tx.Exec(ctx,
			`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat)
			 SELECT m.id, a.id, a.owner_user_id, $3 FROM matches m, agents a
			  WHERE m.public_id = $1 AND a.public_id = $2
			 ON CONFLICT DO NOTHING`,
			s.MatchPublicID, s.AgentPublicID, s.Seat); err != nil {
			return fmt.Errorf("seat %s/%s: %w", s.MatchPublicID, s.AgentPublicID, err)
		}
	}

	for _, r := range b.Ratings {
		if _, err := tx.Exec(ctx,
			`INSERT INTO ratings (agent_id, game, season, algo, elo, rd, vol, mu, sigma,
			      wins, losses, ties, coins_earned, current_streak, best_streak)
			 SELECT a.id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
			   FROM agents a WHERE a.public_id = $1
			 ON CONFLICT (agent_id, game, season) DO UPDATE SET
			   elo = EXCLUDED.elo, rd = EXCLUDED.rd, vol = EXCLUDED.vol,
			   mu = EXCLUDED.mu, sigma = EXCLUDED.sigma,
			   wins = EXCLUDED.wins, losses = EXCLUDED.losses, ties = EXCLUDED.ties,
			   coins_earned = EXCLUDED.coins_earned,
			   current_streak = EXCLUDED.current_streak, best_streak = EXCLUDED.best_streak`,
			r.AgentPublicID, r.Game, r.Season, r.Algo, r.Elo, r.RD, r.Vol, r.Mu, r.Sigma,
			r.Wins, r.Losses, r.Ties, r.CoinsEarned, r.CurrentStreak, r.BestStreak); err != nil {
			return fmt.Errorf("rating %s/%s: %w", r.AgentPublicID, r.Game, err)
		}
	}

	for _, c := range b.RatingChgs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO match_rating_changes (match_id, agent_id, game, season,
			      rating_before, rating_after, rating_delta, rank_in_match, created_at)
			 SELECT m.id, a.id, $3, $4, $5, $6, $7, $8, $9 FROM matches m, agents a
			  WHERE m.public_id = $1 AND a.public_id = $2
			 ON CONFLICT DO NOTHING`,
			c.MatchPublicID, c.AgentPublicID, c.Game, c.Season, c.RatingBefore,
			c.RatingAfter, c.RatingDelta, c.RankInMatch, c.CreatedAt); err != nil {
			return fmt.Errorf("rating change %s/%s: %w", c.MatchPublicID, c.AgentPublicID, err)
		}
	}

	for _, c := range b.ModelCalls {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_model_calls (agent_id, match_id, round, bound, provider, model,
			      prompt_tokens, completion_tokens, cached_read_tokens, cached_write_tokens,
			      reasoning_tokens, latency_ms, status, streamed, created_at)
			 SELECT a.id, $2, $3, true, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
			   FROM agents a WHERE a.public_id = $1
			     AND NOT EXISTS (SELECT 1 FROM agent_model_calls x
			                      WHERE x.agent_id = a.id AND x.match_id = $2
			                        AND x.round = $3 AND x.created_at = $14)`,
			c.AgentPublicID, c.MatchID, c.Round, c.Provider, c.Model, c.PromptTokens,
			c.CompletionTokens, c.CachedReadTokens, c.CachedWriteTokens, c.ReasoningTokens,
			c.LatencyMS, c.Status, c.Streamed, c.CreatedAt); err != nil {
			return fmt.Errorf("model call %s/%s: %w", c.AgentPublicID, c.MatchID, err)
		}
	}

	for _, x := range b.Benchmarks {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_match_benchmark (match_id, agent_id, game, result, decisions,
			      legal, fallbacks, latency_sum_ms, prompt_tokens, completion_tokens,
			      reasoning_tokens, cached_tokens, tokens, estimated_cost,
			      illegal, timeouts, transport_errors,
			      observed_provider, observed_model, declared_provider, declared_model,
			      updated_at)
			 SELECT $1, a.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			        $16, $17, $18, $19, $20, $21, $22
			   FROM agents a WHERE a.public_id = $2
			 ON CONFLICT (match_id, agent_id) DO UPDATE SET
			   decisions = EXCLUDED.decisions, legal = EXCLUDED.legal,
			   fallbacks = EXCLUDED.fallbacks, latency_sum_ms = EXCLUDED.latency_sum_ms,
			   prompt_tokens = EXCLUDED.prompt_tokens,
			   completion_tokens = EXCLUDED.completion_tokens,
			   reasoning_tokens = EXCLUDED.reasoning_tokens,
			   cached_tokens = EXCLUDED.cached_tokens, tokens = EXCLUDED.tokens,
			   estimated_cost = EXCLUDED.estimated_cost,
			   illegal = EXCLUDED.illegal, timeouts = EXCLUDED.timeouts,
			   transport_errors = EXCLUDED.transport_errors,
			   observed_provider = EXCLUDED.observed_provider,
			   observed_model = EXCLUDED.observed_model,
			   declared_provider = EXCLUDED.declared_provider,
			   declared_model = EXCLUDED.declared_model,
			   updated_at = EXCLUDED.updated_at`,
			x.MatchID, x.AgentPublicID, x.Game, x.Result, x.Decisions, x.Legal, x.Fallbacks,
			x.LatencySumMS, x.PromptTokens, x.CompletionTokens, x.ReasoningTokens,
			x.CachedTokens, x.Tokens, x.EstimatedCost, x.Illegal, x.Timeouts,
			x.TransportErrors, x.ObservedProvider, x.ObservedModel, x.DeclaredProvider,
			x.DeclaredModel, x.UpdatedAt); err != nil {
			return fmt.Errorf("benchmark %s/%s: %w", x.MatchID, x.AgentPublicID, err)
		}
	}

	// input_json and every skill_* column are left NULL on purpose — see the
	// package comment. A future scorer run will simply find nothing to score here,
	// which is the honest outcome.
	for _, d := range b.Decisions {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_match_decisions (match_id, agent_id, seq, round, action, outcome,
			      latency_ms, provider, model, prompt_tokens, completion_tokens,
			      reasoning_tokens, cached_tokens, total_tokens, estimated_cost, created_at)
			 SELECT $1, a.id, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16
			   FROM agents a WHERE a.public_id = $2
			 ON CONFLICT (match_id, agent_id, seq) DO UPDATE SET
			   action = EXCLUDED.action, outcome = EXCLUDED.outcome,
			   latency_ms = EXCLUDED.latency_ms,
			   provider = EXCLUDED.provider, model = EXCLUDED.model,
			   prompt_tokens = EXCLUDED.prompt_tokens,
			   completion_tokens = EXCLUDED.completion_tokens,
			   reasoning_tokens = EXCLUDED.reasoning_tokens,
			   cached_tokens = EXCLUDED.cached_tokens, total_tokens = EXCLUDED.total_tokens,
			   estimated_cost = EXCLUDED.estimated_cost`,
			d.MatchID, d.AgentPublicID, d.Seq, d.Round, d.Action, d.Outcome, d.LatencyMS,
			d.Provider, d.Model, d.PromptTokens, d.CompletionTokens, d.ReasoningTokens,
			d.CachedTokens, d.TotalTokens, d.EstimatedCost, d.CreatedAt); err != nil {
			return fmt.Errorf("decision %s/%s#%d: %w", d.MatchID, d.AgentPublicID, d.Seq, err)
		}
	}

	if dryRun {
		return nil // deferred Rollback does the work
	}
	return tx.Commit(ctx)
}
