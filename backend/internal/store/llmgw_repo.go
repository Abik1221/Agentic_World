package store

import (
	"context"
	"errors"
	"time"

	"github.com/agent-arena/arena/internal/llmgw"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LLMGatewayRepo persists what the gateway observed. Implements llmgw.Recorder.
//
// Every write here is off the response path — the gateway records after the bytes have
// reached the agent — so this may be slow without costing an agent a turn. What it must
// never do is fail loudly: a bookkeeping problem must not become the reason a match ends.
type LLMGatewayRepo struct{ db *pgxpool.Pool }

func NewLLMGatewayRepo(db *pgxpool.Pool) *LLMGatewayRepo { return &LLMGatewayRepo{db: db} }

// AgentKind returns an agent's kind for the Lens span. Implements llmgw.KindReader.
//
// An unknown agent answers "" with no error. The gateway caches whatever it gets, and a
// missing row is a real (if rare) state — the same deleted-mid-match case RecordCall
// already tolerates — so turning it into an error would produce a logged failure per call
// for an agent that is simply gone. The caller reads "" as unknown, never as `external`.
func (r *LLMGatewayRepo) AgentKind(ctx context.Context, agentPublicID string) (string, error) {
	var kind string
	err := r.db.QueryRow(ctx,
		`SELECT kind FROM agents WHERE public_id = $1`, agentPublicID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// RecordCall stores one observed call.
//
// Unbound calls are stored too. The bound/total ratio is the COVERAGE a verified board has
// to publish, and without the denominator a cost-per-win metric is perverse: an agent that
// routes 5% of its calls and does the rest elsewhere looks like the cheapest on the
// platform. Discarding unbound rows would hide exactly the attack.
//
// A missing agent is skipped rather than erroring: the agent id came from an authenticated
// principal, so this can only happen if the account was deleted mid-match, and inventing a
// row for a nonexistent agent is worse than dropping one call.
func (r *LLMGatewayRepo) RecordCall(ctx context.Context, c llmgw.Call) error {
	// match_id and round go in as NULL rather than '' / 0 when absent. A call outside any
	// match is a real thing worth costing, and storing round 0 would make it look like a
	// decision that never happened.
	var matchID *string
	var round *int
	if c.MatchID != "" {
		matchID = &c.MatchID
		rd := c.Round
		round = &rd
	}
	_, err := r.db.Exec(ctx,
		// upstream_host is the provenance column (0092): who actually ANSWERED, as opposed to
		// provider/model which are what the developer asked for. The harness benchmark and the
		// model board attribute only calls with a known external host, so recording it here is
		// what keeps a lab stand-in's perfectly well-formed response off a public ranking.
		`INSERT INTO agent_model_calls (
		     agent_id, match_id, round, bound, provider, model,
		     prompt_tokens, completion_tokens, cached_read_tokens, cached_write_tokens,
		     reasoning_tokens, latency_ms, status, streamed, upstream_host)
		 SELECT a.id, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		   FROM agents a WHERE a.public_id = $1`,
		c.AgentPublicID, matchID, round, c.Bound, c.Provider, c.Model,
		c.PromptTokens, c.CompletionTokens, c.CachedReadTokens, c.CachedWriteTokens,
		c.ReasoningTokens, c.LatencyMS, c.Status, c.Streamed, c.UpstreamHost)
	return err
}

// BindDecision marks (match, agent, round) as proven LLM-backed, and records the move the
// model produced for it when the completion carried one.
//
// Reuses the same table the ranked integrity gate reads, so "verified for the leaderboard"
// and "provably LLM-backed for settlement" are one fact rather than two that can disagree.
// Idempotent: an agent may legitimately make several calls for one decision (a best-of-N
// sample, a tool loop), and each carries the same valid proof.
//
// # The conflict rule, which is the whole subtlety here
//
// A turn legitimately produces several calls, and they do not all carry a move. An agent may
// call the model to think, then call it again to decide; or decide first and then make a
// follow-up call for commentary. So:
//
//   - A call WITH a move overwrites whatever was there. Last move wins, because a model that
//     revised its answer stands behind the revision — and because binding the first would let
//     an agent make a throwaway call to pin a move it never intended.
//   - A call WITHOUT a move leaves an existing move ALONE. This is the load-bearing half. If
//     an empty move overwrote, any agent could erase its own binding with one plain follow-up
//     call and opt straight out of the check — the control would be defeated by an accident,
//     let alone an attack.
//
// All three columns key off the SAME condition, which matters: the receipt is an HMAC over
// this move and this completion hash, so a row that mixed a new move with an old hash would
// fail verification and read as tampering rather than as the bookkeeping slip it was.
func (r *LLMGatewayRepo) BindDecision(ctx context.Context, matchID, agentPublicID string, round int, move, completionHash, receipt string) error {
	// Empty strings become NULL so "no move bound" is one value rather than two the readers
	// would each have to remember to check.
	nullable := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	_, err := r.db.Exec(ctx,
		`INSERT INTO agent_match_bound_decisions
		     (match_id, agent_id, round, extracted_move, completion_hash, bind_receipt)
		 SELECT $1, a.id, $3, $4, $5, $6 FROM agents a WHERE a.public_id = $2
		 ON CONFLICT (match_id, agent_id, round) DO UPDATE SET
		     extracted_move  = CASE WHEN EXCLUDED.extracted_move IS NOT NULL
		                            THEN EXCLUDED.extracted_move
		                            ELSE agent_match_bound_decisions.extracted_move END,
		     completion_hash = CASE WHEN EXCLUDED.extracted_move IS NOT NULL
		                            THEN EXCLUDED.completion_hash
		                            ELSE agent_match_bound_decisions.completion_hash END,
		     bind_receipt    = CASE WHEN EXCLUDED.extracted_move IS NOT NULL
		                            THEN EXCLUDED.bind_receipt
		                            ELSE agent_match_bound_decisions.bind_receipt END`,
		matchID, agentPublicID, round, nullable(move), nullable(completionHash), nullable(receipt))
	return err
}

// ExtractedMove reports the move the model produced for one turn.
//
// ok=false means NOTHING IS BOUND for this turn — no verified call, or one that carried no
// recognisable move tool call. Callers must treat that as "the platform has nothing to say",
// never as a mismatch: rejecting on absence would void every honest turn played by an agent
// that has not adopted the structured-move contract, which is currently all of them.
func (r *LLMGatewayRepo) ExtractedMove(ctx context.Context, matchID, agentPublicID string, round int) (string, bool, error) {
	var move *string
	err := r.db.QueryRow(ctx,
		`SELECT bd.extracted_move
		   FROM agent_match_bound_decisions bd
		   JOIN agents a ON a.id = bd.agent_id
		  WHERE bd.match_id = $1 AND a.public_id = $2 AND bd.round = $3`,
		matchID, agentPublicID, round).Scan(&move)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil // unbound turn: not an error, just nothing to compare
		}
		return "", false, err
	}
	if move == nil || *move == "" {
		return "", false, nil
	}
	return *move, true, nil
}

// CoverageFor computes verified coverage for one agent over a match, or over all its play
// when matchID is empty.
//
// Counts DISTINCT decisions, not calls. An agent that made forty calls for one decision has
// covered one decision, and counting calls would let volume manufacture coverage.
//
// # Both sides must count the same unit, and that unit is game-dependent
//
// The numerator counts DISTINCT bound_decisions.round, which is the number the TURN PROOF was
// minted for. The denominator has to count the same thing, and which decision-log column holds
// it differs per game:
//
//	goofspiel  proof slot = the round      → decisions.round  (decisions.seq is a SUBMISSION
//	                                         counter, so a retried round appears twice)
//	mafia      proof slot = MafiaTurn(day, phase) → decisions.seq (day alone would collide
//	                                         across the night and voting phases of one day)
//	monopoly   proof slot = the pre-move NextSeq  → decisions.seq
//
// This counted seq for ALL games, so Goofspiel's denominator included retries while its
// numerator did not. Measured: six seats that bound every single round they played reported
// 76–93% instead of 100%, purely because rounds 7, 8 and 12 had each been submitted twice.
//
// That was a DISPLAY defect rather than a settlement one — the ranked share rule computes its
// own denominator from the engine's round count (see match.rankedIntegrityFailed) and was never
// affected. But this figure is what the Verified badge, `pyyol doctor` and the developer's own
// "verified share" report, so understating it tells an honest developer their agent is partly
// unverified when every decision was bound.
func (r *LLMGatewayRepo) CoverageFor(ctx context.Context, agentPublicID, matchID string) (llmgw.Coverage, error) {
	out := llmgw.Coverage{AgentPublicID: agentPublicID}
	err := r.db.QueryRow(ctx,
		`WITH d AS (
		   SELECT DISTINCT dd.match_id,
		          CASE WHEN m.game = 'goofspiel' THEN dd.round ELSE dd.seq END AS slot
		     FROM agent_match_decisions dd
		     JOIN agents a ON a.id = dd.agent_id
		     -- LEFT JOIN so a decision whose match row has been pruned still counts. Dropping
		     -- it would shrink the denominator and flatter the agent, which is the wrong
		     -- direction for a figure that backs a badge.
		     LEFT JOIN matches m ON m.public_id = dd.match_id
		    WHERE a.public_id = $1 AND ($2 = '' OR dd.match_id = $2)
		 ), b AS (
		   -- INTERSECTED with d, and that is what makes a range binding honest.
		   --
		   -- One completion may now cover several rounds ("plan rounds 4-6"), which is the
		   -- point: batching is cost optimisation and those rounds ARE model-backed. But a span
		   -- is written when the CALL happens, before the later rounds are played. An agent
		   -- that claims rounds 4-6 and then goes dark at round 5 must not be credited for two
		   -- decisions it never made — the platform force-played those turns.
		   --
		   -- So a bound round counts only where the decision log has the matching slot: the
		   -- agent both decided that turn and had a model decide it.
		   SELECT DISTINCT bd.match_id, bd.round
		     FROM agent_match_bound_decisions bd
		     JOIN agents a ON a.id = bd.agent_id
		    WHERE a.public_id = $1 AND ($2 = '' OR bd.match_id = $2)
		      AND EXISTS (SELECT 1 FROM d WHERE d.match_id = bd.match_id AND d.slot = bd.round)
		 )
		 SELECT (SELECT count(*) FROM d), (SELECT count(*) FROM b)`,
		agentPublicID, matchID).Scan(&out.Decisions, &out.BoundDecisions)
	if err != nil {
		return out, err
	}
	if out.Decisions > 0 {
		// Clamped: an agent may bind a round the decision log has no row for (a call for a
		// turn it never answered), which could otherwise report coverage above 100% and
		// make the figure look broken rather than conservative.
		out.Coverage = float64(min(out.BoundDecisions, out.Decisions)) / float64(out.Decisions)
	}
	return out, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ProvenShare is how much of an agent's whole history is cryptographically proven LLM-backed.
//
// Reuses CoverageFor with no match filter, so "proven share" is ONE definition rather than two
// that could drift — the figure the verified badge shows and the figure that overrides the
// timing detector are the same number, computed by the same statement, including its
// per-game proof slot and its intersection with decisions actually made.
func (r *LLMGatewayRepo) ProvenShare(ctx context.Context, agentPublicID string) (bound, decisions int, err error) {
	cov, err := r.CoverageFor(ctx, agentPublicID, "")
	if err != nil {
		return 0, 0, err
	}
	return cov.BoundDecisions, cov.Decisions, nil
}

// RecordVerifiedCost accumulates one observed call into the match's verified spend.
//
// Delegates to the same statement the older gateway used, so the two cannot disagree about how
// spend accumulates while both exist — and so retiring that gateway changes which code CALLS
// this, not what it does.
func (r *LLMGatewayRepo) RecordVerifiedCost(ctx context.Context, c llmgw.VerifiedCost) error {
	return NewPIndexRepo(r.db).RecordVerifiedCost(ctx, VerifiedCall{
		MatchID: c.MatchID, AgentPublicID: c.AgentPublicID, CostUSD: c.CostUSD,
		Provider: c.Provider, Model: c.Model,
		PromptTokens: int64(c.PromptTokens), CompletionTokens: int64(c.CompletionTokens),
		TotalTokens: int64(c.TotalTokens),
	})
}

// RateLimited answers "did a 429 land for this (agent, match, round) inside `within`".
//
// Satisfies match.RateLimitObserver. The gateway already records every proxied call with its
// upstream status, match and round (see RecordCall), so this reads evidence that exists rather
// than adding a new signal — 429s are present in real data today.
//
// # Why this is allowed to grant a deadline extension
//
// A liveness probe says "something is listening". A recorded 429 says "this agent made a real
// model call FOR THIS DECISION and its provider refused it" — the platform watched the attempt.
// Without it, a developer on a free tier forfeits a STAKED match because their provider
// throttled them, having neither played badly nor gone dark.
//
// # Why it is scoped to the round, and fails CLOSED
//
// Round-scoped because a 429 from an earlier turn says nothing about whether this one is being
// attempted; treating it as evidence would hand an idle agent free time.
//
// Any error returns FALSE. An extension is a favour granted on evidence, so the absence of
// evidence — including a database that cannot answer — must mean "no extension", never "extend
// anyway". The opposite direction would let a DB hiccup hold every table open to the ceiling.
func (r *LLMGatewayRepo) RateLimited(ctx context.Context, agentPublicID, matchPublicID string, round int, within time.Duration) bool {
	if agentPublicID == "" || matchPublicID == "" || within <= 0 {
		return false
	}
	var found bool
	err := r.db.QueryRow(ctx,
		`SELECT EXISTS (
		     SELECT 1
		       FROM agent_model_calls c
		       JOIN agents a ON a.id = c.agent_id
		      WHERE a.public_id = $1
		        AND c.match_id = $2
		        AND c.round = $3
		        AND c.status = 429
		        AND c.created_at >= $4
		 )`,
		// The cutoff is computed in Go rather than as a Postgres interval: one fewer moving
		// part than building an interval string, and no chance of a malformed unit silently
		// widening the window.
		agentPublicID, matchPublicID, round, time.Now().Add(-within),
	).Scan(&found)
	if err != nil {
		return false // fail closed — see the note above
	}
	return found
}
