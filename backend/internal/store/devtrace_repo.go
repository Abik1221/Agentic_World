package store

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/devtrace"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DevTraceRepo backs the trace read path from Postgres: which agents a user owns, and
// what those agents actually did.
//
// It used to answer only the first question, because /traces was a thin window onto a
// separate telemetry service. That made a first-class page as available as an external
// dependency, and the page spent most of its life saying the trace store was unreachable.
// The arena's own match log has the answers — see internal/devtrace/local.go.
type DevTraceRepo struct{ db *pgxpool.Pool }

func NewDevTraceRepo(db *pgxpool.Pool) *DevTraceRepo { return &DevTraceRepo{db: db} }

var (
	_ devtrace.Repo      = (*DevTraceRepo)(nil)
	_ devtrace.LocalRepo = (*DevTraceRepo)(nil)
)

// OwnedAgentIDs returns the public ids of the user's own agents.
//
// House agents are excluded: they are platform-run opponents, not the developer's
// work, and their reasoning is exactly the kind of thing a developer should not be
// able to read. A user with no agents gets an empty slice, which the caller turns
// into an empty result rather than an unfiltered query.
func (r *DevTraceRepo) OwnedAgentIDs(ctx context.Context, userPublicID string) ([]string, error) {
	rows, err := r.db.Query(ctx,
		`SELECT a.public_id FROM agents a
		 WHERE a.owner_user_id = (SELECT id FROM users WHERE public_id = $1)
		   AND a.kind <> 'house'
		 ORDER BY a.created_at`, userPublicID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0, 4)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// matchActivityQuery joins the match log to the caller's own seats.
//
// ATTRIBUTION is the whole difficulty. A match_events row belongs to a MATCH, not to an
// agent; match_players is what says which seat is whose. So this produces one row per
// (event, caller's agent in that match) and the service picks out the parts of each payload
// belonging to that seat.
//
// Scope, spelled out because this query is the thing that must not leak:
//   - `a.public_id = ANY($1)` is restricted to the agents the ownership gate already
//     resolved, so a row can only ever be attributed to an agent the caller owns.
//   - Event types are allow-listed here as well as in the mapper. `card_sealed` carries NO
//     card value by design (an opponent must not learn a move before both seats commit),
//     and `round_revealed` is public the instant it is emitted — it is what spectators and
//     the replay see. Nothing hidden about the opponent is selected.
//   - Ordered by event id, not created_at: several events in one round share a timestamp to
//     the millisecond, so id is what makes a bound stable.
const matchActivityQuery = `
SELECT m.public_id, m.game, a.public_id, mp.seat, me.type, me.payload, me.created_at
  FROM match_events me
  JOIN matches       m  ON m.id  = me.match_id
  JOIN match_players mp ON mp.match_id = m.id
  JOIN agents        a  ON a.id  = mp.agent_id
 WHERE a.public_id = ANY($1)
   AND me.created_at >= $2
   AND me.type IN ('match_created','prize_revealed','card_sealed','round_revealed','agent_says','match_finished')
 ORDER BY me.id DESC
 LIMIT $3`

func (r *DevTraceRepo) MatchActivity(
	ctx context.Context, agentPublicIDs []string, since time.Time, limit int,
) ([]devtrace.MatchRow, error) {
	if len(agentPublicIDs) == 0 {
		return nil, nil
	}
	// Fetched generously relative to the caller's limit: several raw rows collapse into one
	// timeline entry (a round's prize reveal, the seal and the reveal become a single
	// decision line), so bounding the SQL at the entry count would truncate the newest
	// round mid-way and report it with no latency.
	fetch := limit * 6
	if fetch <= 0 || fetch > 6000 {
		fetch = 6000
	}
	rows, err := r.db.Query(ctx, matchActivityQuery, agentPublicIDs, since.UTC(), fetch)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]devtrace.MatchRow, 0, 256)
	for rows.Next() {
		var m devtrace.MatchRow
		if err := rows.Scan(&m.MatchPublicID, &m.Game, &m.AgentPublicID, &m.Seat,
			&m.Type, &m.Payload, &m.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AgentRegistrations answers "when did this agent come into existence", so a developer who
// has not played yet is shown something true rather than an empty timeline that reads as a
// broken page.
func (r *DevTraceRepo) AgentRegistrations(
	ctx context.Context, agentPublicIDs []string,
) ([]devtrace.Registration, error) {
	if len(agentPublicIDs) == 0 {
		return nil, nil
	}
	rows, err := r.db.Query(ctx,
		`SELECT public_id, name, created_at FROM agents
		  WHERE public_id = ANY($1) ORDER BY created_at DESC`, agentPublicIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []devtrace.Registration
	for rows.Next() {
		var reg devtrace.Registration
		if err := rows.Scan(&reg.AgentPublicID, &reg.Name, &reg.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, reg)
	}
	return out, rows.Err()
}
