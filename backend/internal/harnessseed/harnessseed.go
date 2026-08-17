// Package harnessseed loads the platform benchmark's published results into the database at
// boot.
//
// # Why results are seeded rather than replayed
//
// The benchmark is real: real models, real provider calls through the gateway, real outcomes.
// It was run against a lab database, and a benchmark nobody can read has not been published.
// Re-running against production is the better path for FUTURE runs and remains the intent;
// this carries across the results already paid for, so the page has something true on it in
// the meantime.
//
// # What makes this safe to run automatically
//
// It is idempotent at the MATCH level: a match whose public id already exists is skipped
// whole, along with every row that hangs off it. That is stronger than per-row upserts,
// because agent_model_calls has no natural key — deduplicating its rows individually would
// mean guessing at one, and a wrong guess silently doubles a model's call count.
//
// It creates no users. The lab minted a throwaway account per seat; those are deliberately
// absent from the export, and every agent here is re-owned by `usr_system`, the identity the
// house bots have used since migration 0017. A seeder that imported `lab+…@pyyol.test` rows
// would put fake developers into production, which is the exact thing the platform-owned
// agent work removed.
//
// Every agent is created with kind='harness', so the public sinks — which select
// kind = 'external' — cannot see them, and every match is written unrated.
package harnessseed

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed data/lab-2026-08.json
var resultsJSON []byte

// SystemOwner is the platform identity every seeded agent hangs off. Must already exist
// (migration 0017); it is never created here, because a second definition of the system
// identity is a second thing to keep in step.
const SystemOwner = "usr_system"

type results struct {
	ExportedAt string `json:"exported_at"`
	Source     string `json:"source"`
	Agents     []struct {
		PublicID    string `json:"public_id"`
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Kind        string `json:"kind"`
	} `json:"agents"`
	Matches []struct {
		PublicID   string  `json:"public_id"`
		Game       string  `json:"game"`
		StartedAt  *string `json:"started_at"`
		FinishedAt *string `json:"finished_at"`
		Status     string  `json:"status"`
		// NOT NULL on matches, and provenance besides: which engine produced this result.
		EngineVersion   string `json:"engine_version"`
		PrizeSeedCommit string `json:"prize_seed_commit"`
		TotalRounds     int    `json:"total_rounds"`
	} `json:"matches"`
	Benchmark []struct {
		MatchID          string  `json:"match_id"`
		Agent            string  `json:"agent"`
		Game             string  `json:"game"`
		Result           *string `json:"result"`
		Decisions        int     `json:"decisions"`
		Legal            int     `json:"legal"`
		Illegal          int     `json:"illegal"`
		Fallbacks        int     `json:"fallbacks"`
		Timeouts         int     `json:"timeouts"`
		TransportErrors  int     `json:"transport_errors"`
		LatencySumMS     int64   `json:"latency_sum_ms"`
		LatencyMinMS     int64   `json:"latency_min_ms"`
		LatencyMaxMS     int64   `json:"latency_max_ms"`
		Tokens           int     `json:"tokens"`
		PromptTokens     int     `json:"prompt_tokens"`
		CompletionTokens int     `json:"completion_tokens"`
		ReasoningTokens  int     `json:"reasoning_tokens"`
		CachedTokens     int     `json:"cached_tokens"`
		EstimatedCost    float64 `json:"estimated_cost"`
		ObservedProvider *string `json:"observed_provider"`
		ObservedModel    *string `json:"observed_model"`
	} `json:"benchmark"`
	ModelCalls []struct {
		MatchID           string  `json:"match_id"`
		Agent             string  `json:"agent"`
		Round             *int    `json:"round"`
		Bound             bool    `json:"bound"`
		Provider          string  `json:"provider"`
		Model             string  `json:"model"`
		UpstreamHost      string  `json:"upstream_host"`
		PromptTokens      int     `json:"prompt_tokens"`
		CompletionTokens  int     `json:"completion_tokens"`
		CachedReadTokens  int     `json:"cached_read_tokens"`
		CachedWriteTokens int     `json:"cached_write_tokens"`
		ReasoningTokens   int     `json:"reasoning_tokens"`
		LatencyMS         int64   `json:"latency_ms"`
		Status            int     `json:"status"`
		Streamed          bool    `json:"streamed"`
		CreatedAt         *string `json:"created_at"`
	} `json:"model_calls"`
	Decisions []struct {
		MatchID          string   `json:"match_id"`
		Agent            string   `json:"agent"`
		Seq              int      `json:"seq"`
		Round            *int     `json:"round"`
		Action           *string  `json:"action"`
		Outcome          *string  `json:"outcome"`
		LatencyMS        *int64   `json:"latency_ms"`
		Provider         *string  `json:"provider"`
		Model            *string  `json:"model"`
		PromptTokens     *int     `json:"prompt_tokens"`
		CompletionTokens *int     `json:"completion_tokens"`
		ReasoningTokens  *int     `json:"reasoning_tokens"`
		CachedTokens     *int     `json:"cached_tokens"`
		TotalTokens      *int     `json:"total_tokens"`
		EstimatedCost    *float64 `json:"estimated_cost"`
		Scaffold         *string  `json:"scaffold"`
		ScaffoldUnstable *bool    `json:"scaffold_unstable"`
		ScaffoldIssue    *string  `json:"scaffold_issue"`
		CreatedAt        *string  `json:"created_at"`
	} `json:"decisions"`
	BoundDecisions []struct {
		MatchID        string  `json:"match_id"`
		Agent          string  `json:"agent"`
		Round          int     `json:"round"`
		ExtractedMove  *string `json:"extracted_move"`
		CompletionHash *string `json:"completion_hash"`
		BindReceipt    *string `json:"bind_receipt"`
		CreatedAt      *string `json:"created_at"`
	} `json:"bound_decisions"`
}

// Seed loads the embedded results. Best-effort by contract: it returns an error, and the
// caller logs rather than refusing to boot. A benchmark that failed to import is a page with
// less on it; a server that will not start is an outage.
// Returns true when it actually imported matches, so the caller can refresh the boards
// rather than leaving the page empty until the next scheduled fit. The worker's first
// refresh runs at boot, BEFORE this seeder, so without that nudge freshly-seeded results are
// invisible for a full refresh interval and read as "the seed did not work".
func Seed(ctx context.Context, db *pgxpool.Pool, log *slog.Logger) (bool, error) {
	var r results
	if err := json.Unmarshal(resultsJSON, &r); err != nil {
		return false, fmt.Errorf("harnessseed: parse embedded results: %w", err)
	}

	// Which matches are already here. Everything else keys off this: a match present means
	// its whole subtree was imported before, and re-importing would double its call counts.
	present := map[string]bool{}
	rows, err := db.Query(ctx, `SELECT public_id FROM matches WHERE public_id = ANY($1)`,
		matchIDs(r))
	if err != nil {
		return false, fmt.Errorf("harnessseed: read existing matches: %w", err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return false, err
		}
		present[id] = true
	}
	rows.Close()

	wanted := 0
	for _, m := range r.Matches {
		if !present[m.PublicID] {
			wanted++
		}
	}
	if wanted == 0 {
		log.Info("harness results already seeded", "matches", len(r.Matches), "source", r.Source)
		return false, nil
	}

	tx, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Agents first: everything below references them, and they are re-owned here rather than
	// carrying whatever owned them in the lab.
	for _, a := range r.Agents {
		if _, err := tx.Exec(ctx,
			`INSERT INTO agents (public_id, owner_user_id, name, slug, description, kind,
			     status, verification_level)
			 SELECT $1, u.id, $2, $3, NULLIF($4,''), 'harness', 'unverified', 'new'
			   FROM users u WHERE u.public_id = $5
			 ON CONFLICT (public_id) DO NOTHING`,
			a.PublicID, a.Name, a.Slug, a.Description, SystemOwner); err != nil {
			return false, fmt.Errorf("harnessseed: agent %s: %w", a.PublicID, err)
		}
	}

	seeded := 0
	for _, m := range r.Matches {
		if present[m.PublicID] {
			continue
		}
		// rated = false, always. These are the platform's results, and every public developer
		// surface reads this flag or the agent kind to stay clean.
		if _, err := tx.Exec(ctx,
			`INSERT INTO matches (public_id, game, status, rated, started_at, finished_at,
			     bid, rake_pct, total_rounds, engine_version, prize_seed_commit,
			     creator_owner_user_id)
			 SELECT $1, $2, COALESCE(NULLIF($3,''),'finished'), false, $4::timestamptz, $5::timestamptz,
			        0, 0, $6, $7, $8, u.id
			   FROM users u WHERE u.public_id = $9
			 ON CONFLICT (public_id) DO NOTHING`,
			m.PublicID, m.Game, m.Status, m.StartedAt, m.FinishedAt,
			m.TotalRounds, m.EngineVersion, m.PrizeSeedCommit, SystemOwner); err != nil {
			return false, fmt.Errorf("harnessseed: match %s: %w", m.PublicID, err)
		}
		seeded++
	}

	for _, b := range r.Benchmark {
		if present[b.MatchID] {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_match_benchmark (match_id, agent_id, game, result, decisions,
			     legal, illegal, fallbacks, timeouts, transport_errors,
			     latency_sum_ms, latency_min_ms, latency_max_ms, tokens,
			     prompt_tokens, completion_tokens, reasoning_tokens, cached_tokens,
			     estimated_cost, observed_provider, observed_model)
			 SELECT $1, a.id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			        $16, $17, $18, $19, $20
			   FROM agents a WHERE a.public_id = $21
			 ON CONFLICT (match_id, agent_id) DO NOTHING`,
			b.MatchID, b.Game, b.Result, b.Decisions, b.Legal, b.Illegal, b.Fallbacks,
			b.Timeouts, b.TransportErrors, b.LatencySumMS, b.LatencyMinMS, b.LatencyMaxMS,
			b.Tokens, b.PromptTokens, b.CompletionTokens, b.ReasoningTokens, b.CachedTokens,
			b.EstimatedCost, b.ObservedProvider, b.ObservedModel, b.Agent); err != nil {
			return false, fmt.Errorf("harnessseed: benchmark %s/%s: %w", b.MatchID, b.Agent, err)
		}
	}

	for _, c := range r.ModelCalls {
		if present[c.MatchID] {
			continue
		}
		// upstream_host and status travel with the row. Both are load-bearing downstream:
		// attribution requires a publishable host AND a 2xx, so importing a call without them
		// would either drop it from the board or, worse, let a failed call count as evidence.
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_model_calls (agent_id, match_id, round, bound, provider, model,
			     prompt_tokens, completion_tokens, cached_read_tokens, cached_write_tokens,
			     reasoning_tokens, latency_ms, status, streamed, upstream_host, created_at)
			 SELECT a.id, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14,
			        COALESCE($15::timestamptz, now())
			   FROM agents a WHERE a.public_id = $16`,
			c.MatchID, c.Round, c.Bound, c.Provider, c.Model, c.PromptTokens, c.CompletionTokens,
			c.CachedReadTokens, c.CachedWriteTokens, c.ReasoningTokens, c.LatencyMS, c.Status,
			c.Streamed, c.UpstreamHost, c.CreatedAt, c.Agent); err != nil {
			return false, fmt.Errorf("harnessseed: model call %s/%s: %w", c.MatchID, c.Agent, err)
		}
	}

	for _, d := range r.Decisions {
		if present[d.MatchID] {
			continue
		}
		// scaffold is the reason this table is imported at all: without a fingerprint a seat
		// cannot be paired, and an unpaired seat contributes nothing to the fit.
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_match_decisions (match_id, agent_id, seq, round, action, outcome,
			     latency_ms, provider, model, prompt_tokens, completion_tokens, reasoning_tokens,
			     cached_tokens, total_tokens, estimated_cost, scaffold, scaffold_unstable,
			     scaffold_issue, created_at)
			 SELECT $1, a.id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15,
			        COALESCE($16, false), $17, COALESCE($18::timestamptz, now())
			   FROM agents a WHERE a.public_id = $19
			 ON CONFLICT (match_id, agent_id, seq) DO NOTHING`,
			d.MatchID, d.Seq, d.Round, d.Action, d.Outcome, d.LatencyMS, d.Provider, d.Model,
			d.PromptTokens, d.CompletionTokens, d.ReasoningTokens, d.CachedTokens, d.TotalTokens,
			d.EstimatedCost, d.Scaffold, d.ScaffoldUnstable, d.ScaffoldIssue, d.CreatedAt,
			d.Agent); err != nil {
			return false, fmt.Errorf("harnessseed: decision %s/%s#%d: %w", d.MatchID, d.Agent, d.Seq, err)
		}
	}

	for _, bd := range r.BoundDecisions {
		if present[bd.MatchID] {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO agent_match_bound_decisions (match_id, agent_id, round,
			     extracted_move, completion_hash, bind_receipt, created_at)
			 SELECT $1, a.id, $2, $3, $4, $5, COALESCE($6::timestamptz, now())
			   FROM agents a WHERE a.public_id = $7
			 ON CONFLICT (match_id, agent_id, round) DO NOTHING`,
			bd.MatchID, bd.Round, bd.ExtractedMove, bd.CompletionHash, bd.BindReceipt,
			bd.CreatedAt, bd.Agent); err != nil {
			return false, fmt.Errorf("harnessseed: bound decision %s/%s#%d: %w",
				bd.MatchID, bd.Agent, bd.Round, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	log.Info("harness results seeded",
		"matches", seeded, "agents", len(r.Agents), "model_calls", len(r.ModelCalls),
		"source", r.Source, "exported_at", r.ExportedAt)
	return true, nil
}

func matchIDs(r results) []string {
	out := make([]string, 0, len(r.Matches))
	for _, m := range r.Matches {
		out = append(out, m.PublicID)
	}
	return out
}

var _ = pgx.ErrNoRows // keep the pgx import meaningful if the queries above change shape
