package store

// Collusion evidence for the multi-seat games.
//
// # No new tables
//
// I expected to have to persist a move list for Mafia and Monopoly and said so. That was
// wrong: both games already write every decision into match_events as structured JSON —
// Mafia emits {"type":"vote","payload":{"from":N,"target":N}} and Monopoly emits
// {"type":"trade_executed","payload":{"proposer":N,"target":N,"give_props":[...],...}} — and
// match_players maps seat to agent. The evidence was there the whole time; what was missing
// was anything that read it.
//
// So this file is queries, not schema. That matters beyond convenience: a second copy of the
// move record would be a second thing to keep in step with the engine, and the copy would be
// the one that drifted.
//
// # Why the seat->agent join is inside the query
//
// A seat number means nothing across matches — seat 3 is a different developer every table.
// Every query here resolves to agent public ids before returning, so a caller can never
// accidentally correlate "seat 3" with "seat 3" and manufacture a ring out of seating.
//
// # Cost
//
// Both queries are bounded by (match window x events per match) and filtered on type before
// any JSON is touched. The type filter is what keeps them cheap: a Mafia table emits far more
// chat and moderator lines than votes.

import (
	"context"
	"time"

	"github.com/agent-arena/arena/internal/antifraud"
)

// These methods hang off AntifraudRepo rather than a separate type, so antifraud.Repo keeps
// ONE implementation. Two repos behind one interface is how a caller ends up wired to the
// half that has not been updated.

// PairVotes returns the rounds in which BOTH agents voted, in the same Mafia match.
//
// Rounds where either seat did not vote are excluded by the join, not encoded as a shared
// "abstain". Two seats both being silent is not evidence of coordination, and counting it as
// agreement would flag every quiet table — which on a staked game means holding honest
// developers' money.
//
// Pairing is on (match, seq-of-the-voting-phase) so two votes are compared only when they
// were cast about the same decision. Comparing a day-1 vote against a day-4 vote would
// measure nothing.
func (r *AntifraudRepo) PairVotes(ctx context.Context, agentA, agentB string, since time.Time) ([]antifraud.VotePair, error) {
	rows, err := r.db.Query(ctx, `
		WITH seats AS (
		    SELECT mp.match_id, mp.seat, a.public_id
		      FROM match_players mp
		      JOIN agents a ON a.id = mp.agent_id
		     WHERE a.public_id IN ($1, $2)
		),
		votes AS (
		    SELECT e.match_id, e.seq,
		           (e.payload->>'from')::int   AS from_seat,
		           (e.payload->>'target')::int AS target
		      FROM match_events e
		      JOIN matches m ON m.id = e.match_id
		     WHERE e.type = 'vote'
		       AND m.game = 'mafia'
		       AND m.finished_at >= $3
		),
		-- One row per (match, voting round). A Mafia day has one lynch vote per seat, so
		-- grouping on the day keeps the two seats' votes about the SAME decision together.
		day AS (
		    SELECT v.match_id, v.from_seat, v.target,
		           (SELECT count(*) FROM match_events p
		             WHERE p.match_id = v.match_id AND p.type = 'phase' AND p.seq < v.seq) AS phase_no
		      FROM votes v
		)
		SELECT da.target, db.target
		  FROM day da
		  JOIN seats sa ON sa.match_id = da.match_id AND sa.seat = da.from_seat AND sa.public_id = $1
		  JOIN day db  ON db.match_id = da.match_id AND db.phase_no = da.phase_no
		  JOIN seats sb ON sb.match_id = db.match_id AND sb.seat = db.from_seat AND sb.public_id = $2
		 ORDER BY da.match_id, da.phase_no`,
		agentA, agentB, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []antifraud.VotePair
	for rows.Next() {
		var a, b int
		if err := rows.Scan(&a, &b); err != nil {
			return nil, err
		}
		out = append(out, antifraud.VotePair{TargetA: a, TargetB: b})
	}
	return out, rows.Err()
}

// PairTrades returns executed Monopoly trades between the two agents, valued from A's side.
//
// EXECUTED only. A proposed or rejected trade moves nothing, and counting offers would let one
// agent frame another by spamming absurd proposals it knows will be declined.
//
// The proposer's AGENT is resolved in SQL, per match. An earlier version mapped seat numbers
// to agents globally across the window and noted in its own comment that this was
// approximate — it is worse than approximate: seat 3 is a different developer in every match,
// so a global map flips the sign of the transfer whenever the two agents sat in each other's
// usual seats, and a flipped sign turns a gifter into a victim. Exactness here is not
// fastidiousness; the direction IS the finding.
func (r *AntifraudRepo) PairTrades(ctx context.Context, agentA, agentB string, since time.Time) ([]antifraud.Trade, error) {
	rows, err := r.db.Query(ctx, `
		WITH seats AS (
		    SELECT mp.match_id, mp.seat, a.public_id
		      FROM match_players mp
		      JOIN agents a ON a.id = mp.agent_id
		     WHERE a.public_id IN ($1, $2)
		)
		SELECT sp.public_id AS proposer_agent,
		       COALESCE((e.payload->>'give_cash')::int, 0),
		       COALESCE((e.payload->>'want_cash')::int, 0),
		       COALESCE(e.payload->'give_props', '[]'::jsonb),
		       COALESCE(e.payload->'want_props', '[]'::jsonb)
		  FROM match_events e
		  JOIN matches m ON m.id = e.match_id
		  JOIN seats sp ON sp.match_id = e.match_id
		                AND sp.seat = (e.payload->>'proposer')::int
		  JOIN seats st ON st.match_id = e.match_id
		                AND st.seat = (e.payload->>'target')::int
		 WHERE e.type = 'trade_executed'
		   AND m.game = 'monopoly'
		   AND m.finished_at >= $3
		   AND sp.public_id <> st.public_id
		 ORDER BY e.match_id, e.seq`,
		agentA, agentB, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []antifraud.Trade
	for rows.Next() {
		var proposer string
		var giveCash, wantCash int
		var giveProps, wantProps []int
		if err := rows.Scan(&proposer, &giveCash, &wantCash, &giveProps, &wantProps); err != nil {
			return nil, err
		}
		giveVal := int64(giveCash) + propValue(giveProps)
		wantVal := int64(wantCash) + propValue(wantProps)
		if proposer == agentA {
			out = append(out, antifraud.Trade{GaveA: giveVal, GaveB: wantVal})
		} else {
			out = append(out, antifraud.Trade{GaveA: wantVal, GaveB: giveVal})
		}
	}
	return out, rows.Err()
}

// propValue prices a bundle of traded property indices.
//
// It priced them at the Monopoly board's list price, read from that engine. The engine is
// gone with the arena, and no shipped game trades property, so there is nothing left to
// value and this returns zero.
//
// The function is KEPT, along with PairTrades and the Trade half of the collusion score,
// because none of that is Monopoly-specific reasoning — it is "did assets flow one way
// between two agents", which is how property-gifting collusion is caught in ANY trading
// game. Deleting the seam would mean rebuilding it, and rebuilding a fraud control is
// where the subtle mistakes get made. Restoring it for a future trading arena is a matter
// of pricing that game's assets here.
func propValue(indices []int) int64 {
	_ = indices
	return 0
}
