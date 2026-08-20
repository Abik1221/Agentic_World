package store

import (
	"context"
	"strings"
	"time"

	"github.com/agent-arena/arena/internal/modelboard"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ModelBoardRepo reads the seats the model board is fitted from.
type ModelBoardRepo struct{ db *pgxpool.Pool }

func NewModelBoardRepo(db *pgxpool.Pool) *ModelBoardRepo { return &ModelBoardRepo{db: db} }

// modelBoardSeatsSQL returns one row per (match, agent) seat with everything the board needs to
// decide whether that seat may be compared and to whom.
//
// $1 game filter (” = every arena) · $2 window start · $3 window end
//
// # Why the model comes from agent_model_calls and nowhere else
//
// The board's entire claim is that it ranks MODELS rather than assertions about models, so the
// only admissible attribution is what the gateway saw the provider return on a call bound to a
// specific decision. The manifest is a developer typing a string. The SDK's per-call report is
// better but still self-reported. Neither can enter here, and a seat with no bound call is
// returned with an empty model so the builder can exclude it and COUNT it — silently dropping
// those rows would make the board look better-attributed than the platform actually is.
//
// bound = true is required, not merely preferred: an unbound call proves the agent talked to a
// provider, not that the call decided a move in this match.
//
// # Why the scaffold is the modal value rather than the latest
//
// A developer can change harness mid-match (an unstable fingerprint). Taking the LAST scaffold
// would attribute the whole match to whichever harness happened to answer the final turn. The
// modal one is the harness that actually played the match, and a seat whose fingerprint churned
// is better excluded by its own instability than mis-attributed to one arbitrary value.
// The two boards differ in exactly one CTE — WHICH SEATS ARE ELIGIBLE — and share every
// line after it. The fit, the attribution rule, the scaffold pairing and the coverage
// arithmetic are literally the same SQL, because the two boards are meant to produce
// numbers that mean the same thing.
//
// The eligibility difference is not cosmetic and is worth stating. The developer board
// requires `m.rated`, which is how it excludes tables a house bot had to fill: crediting a
// model for beating an engine bot would put a fictional opponent into the likelihood.
// Harness matches are UNRATED BY DESIGN — that is what keeps them out of user ratings — so
// the same filter would return nothing. The equivalent guard there is structural: every seat
// at the table must itself be a harness agent, which rules out the same fictional opponent
// by construction rather than by a flag.
const modelBoardSeatDeveloperCTE = `
WITH seat AS (
  SELECT b.match_id, b.game, b.result, b.agent_id,
         a.public_id AS agent_public_id,
         u.public_id AS developer_public_id
    FROM agent_match_benchmark b
    JOIN agents a  ON a.id = b.agent_id AND a.kind = 'external'
    JOIN users  u  ON u.id = a.owner_user_id
    JOIN matches m ON m.public_id = b.match_id
                  AND m.finished_at IS NOT NULL
                  AND m.finished_at >= $2 AND m.finished_at < $3
                  -- Tables that house bots had to fill are unrated; crediting a model for
                  -- beating engine bots would put a fictional opponent in the likelihood.
                  AND m.rated
   WHERE ($1 = '' OR b.game = $1)
),`

const modelBoardSeatHarnessCTE = `
WITH seat AS (
  SELECT b.match_id, b.game, b.result, b.agent_id,
         a.public_id AS agent_public_id,
         u.public_id AS developer_public_id
    FROM agent_match_benchmark b
    JOIN agents a  ON a.id = b.agent_id AND a.kind = 'harness'
    JOIN users  u  ON u.id = a.owner_user_id
    JOIN matches m ON m.public_id = b.match_id
                  AND m.finished_at IS NOT NULL
                  AND m.finished_at >= $2 AND m.finished_at < $3
   WHERE ($1 = '' OR b.game = $1)
     -- Every seat at the table must be a harness agent. A benchmark match that a house bot
     -- or a real developer partly filled is not a controlled comparison, and one such seat
     -- is enough to make the whole match's likelihood describe something else.
     AND NOT EXISTS (
       SELECT 1 FROM agent_match_benchmark ob
         JOIN agents oa ON oa.id = ob.agent_id
        WHERE ob.match_id = b.match_id AND oa.kind <> 'harness'
     )
),`

const modelBoardSeatsTailSQL = `
-- Scoping the child CTEs to the seat set is OPTIONAL, and which boards get it is a
-- MEASURED decision rather than a preference. See seatsTailSQL below for the numbers.
-- The verified model: what the provider's own response named, on a call PROVEN to belong to a
-- decision in this match. Latest such call wins, because an agent that switched models mid-match
-- finished on the later one.
-- The seat keys, once, as a JOINABLE relation.
--
-- Deliberately a JOIN target rather than an IN (...) subquery, and that is not cosmetic.
-- The two boards differ in cardinality by four orders of magnitude — the harness window
-- holds ~44 seats, the developer window ~320,000 — and a semijoin subquery got planned as
-- a nested loop for both. Right for 44, badly wrong for 320,000: it turned two sequential
-- scans into 320,000 index descents into a 22 GB table and the estimated cost went UP.
--
-- As a join against a DISTINCT relation the planner picks per cardinality: an index loop
-- for the small board, a hash join for the large one. That is the property worth having,
-- because these two boards share this SQL precisely so they cannot drift, and a shape that
-- is only correct at one size would have forced them apart.
--
-- DISTINCT because the joins below COUNT. If the seat CTE ever yielded two rows for one
-- (agent, match), a plain join would silently double logged_decisions and with it the
-- coverage ratio that gates publication.
-- MATERIALIZED, and that keyword is doing the work.
--
-- Left to inline, the planner had no honest cardinality for this relation and assumed it
-- was tiny, so it chose an index nested loop for BOTH boards. Right for the harness
-- window's ~44 seats; wrong for the developer window's ~320,000, where it means 320,000
-- index descents into a 22 GB table instead of one hash join.
--
-- Materialising it computes the set once and hands the planner the DISTINCT node's own
-- row estimate, which is accurate at either size — so the join strategy is chosen from
-- the real shape of the data rather than from a default. It also means the set is built
-- once instead of three times, which is what we wanted regardless.
{{SEAT_KEYS}}verified AS (
  SELECT DISTINCT ON (mc.agent_id, mc.match_id)
         mc.agent_id, mc.match_id,
         NULLIF(mc.provider,'') || '/' || NULLIF(mc.model,'') AS model_key
    FROM agent_model_calls mc
{{SCOPE_CALLS}}   -- SCOPED TO THE SEATS THIS WINDOW ACTUALLY ASKS ABOUT.
   --
   -- Semantically a no-op: this CTE is LEFT JOINed to the seat CTE on exactly these two
   -- columns, so a row outside the seat set was always discarded. What it changes is
   -- the cost. Unscoped, the planner had no window predicate to work with and read the
   -- whole table; here it drives from ~40 seats through idx_model_calls_match.
   --
   -- A semijoin rather than a JOIN on purpose: it cannot duplicate mc rows even if
   -- the seat CTE ever yielded two rows for one (agent, match), which matters because the
   -- sibling CTEs below COUNT.
   WHERE mc.bound AND COALESCE(mc.model,'') <> '' AND mc.match_id IS NOT NULL
     -- The provider actually ANSWERED. mc.bound is set from the turn proof BEFORE the
     -- upstream is called, so it says "this request belonged to this decision" and nothing
     -- about whether a model replied. Without this line a rate-limited run is indistinguishable
     -- from a played one: measured in the lab, 108 harness calls to openrouter.ai were 429s and
     -- 401s returning zero tokens, every one of them bound=true, and the two google/gemma models
     -- they named were attributed matches and a WIN RATE on the board having never emitted a
     -- token. The gateway already refuses to bind a MOVE from a non-2xx call (llmgw.go); this
     -- makes attribution agree with it instead of contradicting it.
     AND mc.status BETWEEN 200 AND 299
     -- WHO ANSWERED, not who was asked. provider/model are read from the REQUEST, and the
     -- upstream map decides where that request actually goes; point a provider at a local
     -- stand-in and this row is a bound, well-formed call under the requested model name
     -- that never left the machine. Rows written before migration 0092 carry '' and are
     -- excluded as UNKNOWN provenance rather than assumed real.
     AND mc.upstream_host = ANY($4)
     -- AND THE UPSTREAM MUST HAVE ANSWERED.
     --
     -- bound means the TURN PROOF verified — that this call belongs to this decision. It
     -- does not mean the provider returned anything. A 401 or a 429 is a bound call with no
     -- completion, no tokens and no model output, and reading it as attribution credits a
     -- model for a request it never saw.
     --
     -- Measured, not hypothetical: of 251 bound harness calls in the lab, 108 were OpenRouter
     -- failures (25 unauthorized, 83 rate-limited) and 33 were Groq failures. Without this
     -- line the board ranked two Gemma models whose every single call had been rejected.
     --
     -- The decision-binding path already requires 2xx (llmgw.Proxy); attribution read a
     -- different column and inherited none of that check.
     AND mc.status BETWEEN 200 AND 299
   ORDER BY mc.agent_id, mc.match_id, mc.id DESC
),
-- The harness that actually played the match: the most frequent fingerprint across the seat's
-- decisions, not the last one.
scaffold AS (
  SELECT agent_id, match_id, scaffold FROM (
    SELECT d.agent_id, d.match_id, d.scaffold,
           ROW_NUMBER() OVER (PARTITION BY d.agent_id, d.match_id
                              ORDER BY COUNT(*) DESC, d.scaffold) AS rn
      FROM agent_match_decisions d
{{SCOPE_SCAFFOLD}}     WHERE COALESCE(d.scaffold,'') <> ''
     GROUP BY d.agent_id, d.match_id, d.scaffold
  ) ranked WHERE rn = 1
),
-- Per-seat coverage: distinct decisions proven, over decisions logged.
cov AS (
  SELECT d.agent_id, d.match_id,
         COUNT(*)::bigint AS logged,
         COALESCE(bd.bound, 0)::bigint AS bound
    FROM agent_match_decisions d
{{SCOPE_COV}}    LEFT JOIN (
      SELECT b2.agent_id, b2.match_id, COUNT(DISTINCT b2.round)::bigint AS bound
        FROM agent_match_bound_decisions b2
{{SCOPE_BOUND}}       GROUP BY b2.agent_id, b2.match_id
    ) bd ON bd.agent_id = d.agent_id AND bd.match_id = d.match_id
   GROUP BY d.agent_id, d.match_id, bd.bound
)
SELECT s.match_id, s.game, s.agent_public_id, s.developer_public_id, s.result,
       COALESCE(v.model_key, '')  AS verified_model,
       COALESCE(sc.scaffold, '')  AS scaffold,
       COALESCE(c.bound, 0)       AS bound_decisions,
       COALESCE(c.logged, 0)      AS logged_decisions,
       -- Mafia roles live in the match state, keyed by seat. Carried so the exclusion of team
       -- games can be justified from data rather than only argued in a comment.
       COALESCE(mm.state->'roles'->>(mp.seat::text), '') AS role
  FROM seat s
  LEFT JOIN verified v  ON v.agent_id  = s.agent_id AND v.match_id  = s.match_id
  LEFT JOIN scaffold sc ON sc.agent_id = s.agent_id AND sc.match_id = s.match_id
  LEFT JOIN cov c       ON c.agent_id  = s.agent_id AND c.match_id  = s.match_id
  LEFT JOIN matches mm  ON mm.public_id = s.match_id
  LEFT JOIN match_players mp ON mp.match_id = mm.id AND mp.agent_id = s.agent_id
{{PAIRWISE_FILTER}} ORDER BY s.match_id, s.agent_public_id`

// seatsTailSQL renders the shared tail, optionally scoping the per-decision CTEs to the
// seat set.
//
// ONE source string, two physical strategies, and the reason is measured. The two boards
// differ in cardinality by four orders of magnitude — the harness window holds ~44 seats,
// the developer window ~320,000 — and how to read a 10-million-row table depends entirely
// on which of those you are doing.
//
// On the lab database (agent_match_decisions: 10.1M rows, 22 GB), disk read for one refresh:
//
//	                        unscoped               scoped to the seat set
//	harness board (44)      18.0 GB, 2 seq scans   113 MB    <- 163x better
//	developer board (320k)  20.2 GB, 2 seq scans   53.4 GB   <- 2.6x WORSE
//
// The developer row is why this is a function and not simply "scope everything". Scoped, its
// plan is better by every proxy available — no sequential scan of the big table, an honest
// cardinality from a MATERIALIZED CTE, an estimated cost 2.4x LOWER than unscoped — and it
// reads two and a half times more from disk, because 320,000 index descents scattered across
// 22 GB is more work than two ordered passes. The cost model understates random I/O. The
// measurement is the authority here, not the estimate.
//
// So: scoped for the small board, unscoped for the large one. What must not happen is the two
// boards drifting apart semantically — a rating on one has to mean what a rating on the other
// means — so the SQL has exactly ONE source and the difference is confined to join clauses
// that cannot change which rows come back. Every scoped CTE is LEFT JOINed to seat on
// precisely the keys being scoped, so the two spellings are equivalent by construction;
// verified empirically as well, the harness board returning byte-identical output both ways.
//
// DISTINCT in seat_keys because the CTEs below COUNT: were seat ever to yield two rows for
// one (agent, match), a plain join would double logged_decisions and with it the coverage
// ratio that gates publication.
func seatsTailSQL(scoped bool) string {
	if !scoped {
		return strings.NewReplacer(
			"{{SEAT_KEYS}}", "",
			"{{SCOPE_CALLS}}", "",
			"{{SCOPE_SCAFFOLD}}", "",
			"{{SCOPE_COV}}", "",
			"{{SCOPE_BOUND}}", "",
		).Replace(modelBoardSeatsTailSQL)
	}
	return strings.NewReplacer(
		"{{SEAT_KEYS}}", "seat_keys AS MATERIALIZED (\n  SELECT DISTINCT match_id, agent_id FROM seat\n),\n",
		"{{SCOPE_CALLS}}", "   JOIN seat_keys sk ON sk.match_id = mc.match_id AND sk.agent_id = mc.agent_id\n",
		"{{SCOPE_SCAFFOLD}}", "      JOIN seat_keys sk ON sk.match_id = d.match_id AND sk.agent_id = d.agent_id\n",
		"{{SCOPE_COV}}", "    JOIN seat_keys sk ON sk.match_id = d.match_id AND sk.agent_id = d.agent_id\n",
		"{{SCOPE_BOUND}}", "        JOIN seat_keys sk2 ON sk2.match_id = b2.match_id AND sk2.agent_id = b2.agent_id\n",
	).Replace(modelBoardSeatsTailSQL)
}

// Seats reads the model board's input for one window.
//
// Returns EVERY eligible-match seat, including ones the board will exclude. The exclusions and
// their reasons are computed in modelboard.BuildComparisons so they can be published: a model
// absent from the board is a claim about that model, and "never verified" has to be
// distinguishable from "never played".
// publishableHosts are the upstreams whose responses may be attributed to a model. See
// llmgw.PublishableUpstreamHosts for why the operator has to declare an override.
func (r *ModelBoardRepo) Seats(ctx context.Context, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, map[string]int, error) {
	// UNSCOPED: this window holds ~320,000 seats even after the game filter, and at that
	// cardinality sequential scans win. See seatsTailSQL.
	return r.seatsFor(ctx, modelBoardSeatDeveloperCTE, false, game, start, end, publishableHosts)
}

// HarnessSeats reads the same shape of input for the PLATFORM's own benchmark matches.
//
// Same estimator, same attribution rule, same coverage arithmetic — only the eligibility CTE
// differs, so a rating on one board means what a rating on the other means. That is the
// whole reason the two share this file instead of the harness getting its own query that
// would drift from this one within a release.
func (r *ModelBoardRepo) HarnessSeats(ctx context.Context, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, map[string]int, error) {
	// SCOPED: ~44 seats in this window, and scoping is 163x less disk. See seatsTailSQL.
	return r.seatsFor(ctx, modelBoardSeatHarnessCTE, true, game, start, end, publishableHosts)
}

func (r *ModelBoardRepo) seatsFor(ctx context.Context, seatCTE string, scoped bool, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, map[string]int, error) {
	// THE GAME FILTER RUNS IN POSTGRES, and it is the difference between reading a window and
	// reading the whole table's worth of it.
	//
	// BuildComparisons can only compare seats from a game with a pairwise outcome, and it used
	// to discard every other seat AFTER the query had materialised it, encoded it, shipped it
	// and allocated it in Go. Measured on the developer board: 600,895 seats returned, 593,530
	// of them dropped on this one condition — 98.8% of the rows travelled the whole way to be
	// thrown away. Only 3 seats in that window were eligible in the end.
	//
	// The list is generated from modelboard.PairwiseGames() rather than written here, so this
	// filter cannot disagree with the one BuildComparisons applies. Adding a game to that map
	// is still the only edit needed.
	rows, err := r.db.Query(ctx, renderSeatsSQL(seatCTE, scoped, true),
		game, start, end, publishableHosts, modelboard.PairwiseGames())
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	var out []modelboard.Seat
	for rows.Next() {
		var s modelboard.Seat
		var bound, logged int64
		if err := rows.Scan(&s.MatchID, &s.Game, &s.AgentID, &s.DeveloperID, &s.Result,
			&s.Model, &s.Scaffold, &bound, &logged, &s.Role); err != nil {
			return nil, nil, err
		}
		if logged > 0 {
			s.CoverageKnown = true
			if bound > logged {
				bound = logged
			}
			s.Coverage = float64(bound) / float64(logged)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// And account for what the filter removed. seats_excluded is PUBLISHED — a reader uses it
	// to tell "this model never played" from "this model played and was not measurable" — so a
	// seat dropped in SQL has to be counted with the same reason and the same key it would have
	// received in Go. Reporting zero here while half a million seats were excluded would be
	// worse than publishing nothing.
	excluded, err := r.pairwiseExclusions(ctx, seatCTE, scoped, game, start, end, publishableHosts)
	if err != nil {
		return nil, nil, err
	}
	return out, excluded, nil
}

// pairwiseExclusions counts the seats the game filter removed, keyed exactly as
// BuildComparisons keys them ("game_not_pairwise_<game>").
//
// A separate aggregate rather than a window function beside the rows, because the two answers
// have completely different sizes: a handful of counts against however many eligible seats
// there are, and carrying the counts on every row would put the larger number of copies of the
// smaller answer over the wire.
func (r *ModelBoardRepo) pairwiseExclusions(ctx context.Context, seatCTE string, scoped bool, game string, start, end time.Time, publishableHosts []string) (map[string]int, error) {
	// Wraps the UNFILTERED seat query: this counts precisely the rows the filter removes.
	sql := "WITH seats AS (" + renderSeatsSQL(seatCTE, scoped, false) +
		") SELECT game, count(*) FROM seats WHERE NOT (game = ANY($5)) GROUP BY game"
	rows, err := r.db.Query(ctx, sql, game, start, end, publishableHosts, modelboard.PairwiseGames())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var g string
		var n int
		if err := rows.Scan(&g, &n); err != nil {
			return nil, err
		}
		out["game_not_pairwise_"+g] = n
	}
	return out, rows.Err()
}

// renderSeatsSQL produces the FINAL seat query — no placeholders left.
//
// One function for it because there are two substitutions (the scoping joins and the pairwise
// game filter) and two callers that need the query (the seat read and the exclusion count). Done
// separately in each, a placeholder left unsubstituted in one path would reach Postgres as a
// syntax error, or worse, land inside a comment and silently disable a filter.
// TestNoScopingPlaceholdersReachPostgres asserts against this function for that reason.
//
// `filtered` selects the pairwise game restriction. The exclusion count needs the UNFILTERED
// query — it exists to count what the filter removes — so it is the one caller that passes false.
func renderSeatsSQL(seatCTE string, scoped, filtered bool) string {
	filter := ""
	if filtered {
		// $5 because the shared tail already uses $1..$4 (game, start, end, publishable hosts).
		filter = " WHERE s.game = ANY($5)\n"
	}
	return seatCTE + strings.Replace(seatsTailSQL(scoped), "{{PAIRWISE_FILTER}}", filter, 1)
}

// HarnessSeatSource adapts the repo's harness query to modelboard.SeatSource, so the SAME
// service type can be instantiated twice — once per board — with no branch inside it.
//
// An adapter rather than a flag on the service: a boolean would mean every future reader of
// modelboard has to hold "which board am I" in their head while reading the fit, and one of
// them eventually would not. Here the choice is made once, at construction, and the fit code
// never learns there is more than one board.
type HarnessSeatSource struct{ Repo *ModelBoardRepo }

func (h HarnessSeatSource) Seats(ctx context.Context, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, map[string]int, error) {
	return h.Repo.HarnessSeats(ctx, game, start, end, publishableHosts)
}
