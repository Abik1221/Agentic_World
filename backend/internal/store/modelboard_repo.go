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
const modelBoardSeatsSQL = `
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
),
-- The verified model: what the provider's own response named, on a call PROVEN to belong to a
-- decision in this match. Latest such call wins, because an agent that switched models mid-match
-- finished on the later one.
verified AS (
  SELECT DISTINCT ON (mc.agent_id, mc.match_id)
         mc.agent_id, mc.match_id,
         NULLIF(mc.provider,'') || '/' || NULLIF(mc.model,'') AS model_key
    FROM agent_model_calls mc
   WHERE mc.bound AND COALESCE(mc.model,'') <> '' AND mc.match_id IS NOT NULL
     -- WHO ANSWERED, not who was asked. provider/model are read from the REQUEST, and the
     -- upstream map decides where that request actually goes; point a provider at a local
     -- stand-in and this row is a bound, well-formed call under the requested model name
     -- that never left the machine. Rows written before migration 0092 carry '' and are
     -- excluded as UNKNOWN provenance rather than assumed real.
     AND mc.upstream_host = ANY($4)
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
	rows, err := r.db.Query(ctx, modelBoardSeatsSQL, game, start, end, publishableHosts)
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
