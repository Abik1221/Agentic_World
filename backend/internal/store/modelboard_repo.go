package store

import (
	"context"
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
-- The verified model: what the provider's own response named, on a call PROVEN to belong to a
-- decision in this match. Latest such call wins, because an agent that switched models mid-match
-- finished on the later one.
verified AS (
  SELECT DISTINCT ON (mc.agent_id, mc.match_id)
         mc.agent_id, mc.match_id,
         NULLIF(mc.provider,'') || '/' || NULLIF(mc.model,'') AS model_key
    FROM agent_model_calls mc
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
     WHERE COALESCE(d.scaffold,'') <> ''
     GROUP BY d.agent_id, d.match_id, d.scaffold
  ) ranked WHERE rn = 1
),
-- Per-seat coverage: distinct decisions proven, over decisions logged.
cov AS (
  SELECT d.agent_id, d.match_id,
         COUNT(*)::bigint AS logged,
         COALESCE(bd.bound, 0)::bigint AS bound
    FROM agent_match_decisions d
    LEFT JOIN (
      SELECT agent_id, match_id, COUNT(DISTINCT round)::bigint AS bound
        FROM agent_match_bound_decisions GROUP BY agent_id, match_id
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
 ORDER BY s.match_id, s.agent_public_id`

// Seats reads the model board's input for one window.
//
// Returns EVERY eligible-match seat, including ones the board will exclude. The exclusions and
// their reasons are computed in modelboard.BuildComparisons so they can be published: a model
// absent from the board is a claim about that model, and "never verified" has to be
// distinguishable from "never played".
// publishableHosts are the upstreams whose responses may be attributed to a model. See
// llmgw.PublishableUpstreamHosts for why the operator has to declare an override.
func (r *ModelBoardRepo) Seats(ctx context.Context, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, error) {
	return r.seatsFor(ctx, modelBoardSeatDeveloperCTE, game, start, end, publishableHosts)
}

// HarnessSeats reads the same shape of input for the PLATFORM's own benchmark matches.
//
// Same estimator, same attribution rule, same coverage arithmetic — only the eligibility CTE
// differs, so a rating on one board means what a rating on the other means. That is the
// whole reason the two share this file instead of the harness getting its own query that
// would drift from this one within a release.
func (r *ModelBoardRepo) HarnessSeats(ctx context.Context, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, error) {
	return r.seatsFor(ctx, modelBoardSeatHarnessCTE, game, start, end, publishableHosts)
}

func (r *ModelBoardRepo) seatsFor(ctx context.Context, seatCTE, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, error) {
	rows, err := r.db.Query(ctx, seatCTE+modelBoardSeatsTailSQL, game, start, end, publishableHosts)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []modelboard.Seat
	for rows.Next() {
		var s modelboard.Seat
		var bound, logged int64
		if err := rows.Scan(&s.MatchID, &s.Game, &s.AgentID, &s.DeveloperID, &s.Result,
			&s.Model, &s.Scaffold, &bound, &logged, &s.Role); err != nil {
			return nil, err
		}
		// Coverage is computed here rather than in SQL so the clamp and the
		// unknown-versus-zero distinction live in exactly one place, shared with the boards.
		if logged > 0 {
			s.CoverageKnown = true
			if bound > logged {
				bound = logged
			}
			s.Coverage = float64(bound) / float64(logged)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// HarnessSeatSource adapts the repo's harness query to modelboard.SeatSource, so the SAME
// service type can be instantiated twice — once per board — with no branch inside it.
//
// An adapter rather than a flag on the service: a boolean would mean every future reader of
// modelboard has to hold "which board am I" in their head while reading the fit, and one of
// them eventually would not. Here the choice is made once, at construction, and the fit code
// never learns there is more than one board.
type HarnessSeatSource struct{ Repo *ModelBoardRepo }

func (h HarnessSeatSource) Seats(ctx context.Context, game string, start, end time.Time, publishableHosts []string) ([]modelboard.Seat, error) {
	return h.Repo.HarnessSeats(ctx, game, start, end, publishableHosts)
}
