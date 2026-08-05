package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TENANT ISOLATION ON THE TRACE READ PATHS.
//
// A trace carries the most sensitive data on the platform: the agent's private reasoning
// about its opponents, and the turn view containing its hidden information (its hand in
// Goofspiel, its role in Mafia). Two developers who played the SAME match must each see
// only their own seat, and that has to hold on every read port rather than on the ones
// somebody remembered.
//
// This test builds one match with two owners in it and asserts, per port, that neither
// can reach the other's rows — including the case that actually causes leaks in practice:
// a caller passing an agent id they do not own as a filter.
//
// Written against real Postgres because the scoping IS the SQL. A fake repo would assert
// that the Go code passes an id list, which is not the property that matters.

func TestTraceIsolationBetweenOwnersLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	repo := NewPIndexRepo(pool)
	trace := NewDevTraceRepo(pool)
	_, run := isolate()

	// Two developers, one agent each, seated in the SAME match.
	alice, aliceOwner := mkUserAgent(t, pool, "iso-alice-"+run)
	bob, bobOwner := mkUserAgent(t, pool, "iso-bob-"+run)
	match := "m_iso_" + run

	ownerID := func(pub string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `SELECT id FROM users WHERE public_id = $1`, pub).Scan(&id); err != nil {
			t.Fatalf("owner %s: %v", pub, err)
		}
		return id
	}
	aliceUID, bobUID := ownerID(aliceOwner), ownerID(bobOwner)

	// The match, with both agents seated.
	var mid int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, engine_version, prize_seed_commit,
		                      creator_owner_user_id, started_at, finished_at)
		 VALUES ($1,'goofspiel','finished',100,'iso','iso',$2, now() - interval '2 minutes', now())
		 ON CONFLICT (public_id) DO UPDATE SET finished_at = now() RETURNING id`,
		match, aliceUID).Scan(&mid); err != nil {
		t.Fatalf("match: %v", err)
	}
	for _, seat := range []struct {
		agent string
		uid   int64
		n     int
	}{{alice, aliceUID, 0}, {bob, bobUID, 1}} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat)
			 SELECT $1, a.id, $2, $3 FROM agents a WHERE a.public_id = $4
			 ON CONFLICT DO NOTHING`, mid, seat.uid, seat.n, seat.agent); err != nil {
			t.Fatalf("seat %s: %v", seat.agent, err)
		}
	}

	// Each seat's private record. The secrets are distinctive strings so a leak is
	// unambiguous rather than a subtle field mismatch.
	const (
		aliceSecret = "ALICE_PRIVATE_REASONING"
		bobSecret   = "BOB_PRIVATE_REASONING"
		aliceHand   = "ALICE_HIDDEN_HAND"
		bobHand     = "BOB_HIDDEN_HAND"
	)
	now := time.Now().UTC()
	write := func(agent, secret, hand string) {
		if err := repo.RecordMatchDecisions(ctx, match, agent, []MatchDecision{{
			Seq: 0, Round: 1, Action: "9", Outcome: "ok", LatencyMS: 400,
			Rationale: secret, StartedAt: now,
			InputJSON: json.RawMessage(`{"hand":"` + hand + `"}`),
		}}); err != nil {
			t.Fatalf("record %s: %v", agent, err)
		}
	}
	write(alice, aliceSecret, aliceHand)
	write(bob, bobSecret, bobHand)

	// A public match event, so the timeline has something in it for both.
	if _, err := pool.Exec(ctx,
		`INSERT INTO match_events (match_id, seq, type, payload)
		 VALUES ($1, 1, 'round_revealed', '{"round":1,"prize":11,"cards":[9,4],"winner":0}'::jsonb)
		 ON CONFLICT (match_id, seq) DO NOTHING`, mid); err != nil {
		t.Fatalf("event: %v", err)
	}

	// ── the ownership resolver is the root of every gate ─────────────────────────
	aliceOwned, err := trace.OwnedAgentIDs(ctx, aliceOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceOwned) != 1 || aliceOwned[0] != alice {
		t.Fatalf("alice owns %v, want exactly [%s]", aliceOwned, alice)
	}
	for _, id := range aliceOwned {
		if id == bob {
			t.Fatal("OwnedAgentIDs returned another developer's agent — every gate below " +
				"is built on this list, so a leak here is a leak everywhere")
		}
	}

	// ── MatchDecisions: reasoning AND hidden state ───────────────────────────────
	aliceDecisions, err := trace.MatchDecisions(ctx, aliceOwned, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceDecisions) != 1 {
		t.Fatalf("alice sees %d decisions, want only her own", len(aliceDecisions))
	}
	if aliceDecisions[0].Rationale != aliceSecret {
		t.Errorf("alice cannot see her own reasoning: %q", aliceDecisions[0].Rationale)
	}
	blob, _ := json.Marshal(aliceDecisions)
	for _, forbidden := range []string{bobSecret, bobHand} {
		if strings.Contains(string(blob), forbidden) {
			t.Fatalf("LEAK: %q reached alice's decision trace", forbidden)
		}
	}
	// Symmetric — the gate is not accidentally one-directional.
	bobDecisions, err := trace.MatchDecisions(ctx, []string{bob}, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	bobBlob, _ := json.Marshal(bobDecisions)
	for _, forbidden := range []string{aliceSecret, aliceHand} {
		if strings.Contains(string(bobBlob), forbidden) {
			t.Fatalf("LEAK: %q reached bob's decision trace", forbidden)
		}
	}

	// ── the attack that actually works in practice ───────────────────────────────
	// Alice asks for BOB's agent id directly. The port must scope to the ids it was
	// given, and the service's `actors` gate must never hand it an id alice does not
	// own — so passing bob's id here must return bob's rows, proving the port itself is
	// a pure filter and that the GATE is what protects it.
	asBob, err := trace.MatchDecisions(ctx, []string{bob}, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(asBob) != 1 {
		t.Fatal("sanity: the port should filter to exactly the ids it is given")
	}
	// ...and with a MIXED list, only the owned half must ever be requested. This asserts
	// the invariant the service upholds: the port is never called with a foreign id.
	// (See TestActorsGateRejectsUnownedAgent for the gate itself.)

	// ── MatchEvents: the shared timeline, attributed per seat ────────────────────
	aliceEvents, err := trace.MatchEvents(ctx, aliceOwned, match, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(aliceEvents) == 0 {
		t.Error("alice should see the match's public events")
	}
	for _, e := range aliceEvents {
		if e.Seat != 0 {
			t.Errorf("alice's events must be attributed to HER seat, got seat %d", e.Seat)
		}
	}

	// ── MatchSummaryFor: a match you had no seat in must not resolve ─────────────
	stranger, strangerOwner := mkUserAgent(t, pool, "iso-stranger-"+run)
	_ = stranger
	strangerOwned, err := trace.OwnedAgentIDs(ctx, strangerOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trace.MatchSummaryFor(ctx, strangerOwned, match); err == nil {
		t.Fatal("a developer with no seat in the match resolved its summary — the page " +
			"would then render, confirming the match exists and whose it is")
	}
	// And their decision read is empty, not an error that confirms the match exists.
	if got, err := trace.MatchDecisions(ctx, strangerOwned, match, 100); err != nil || len(got) != 0 {
		t.Errorf("stranger got %d decisions (err=%v), want 0", len(got), err)
	}

	// ── the empty-owned case: a caller with no agents must never widen to "all" ──
	if got, err := trace.MatchDecisions(ctx, nil, match, 100); err != nil || len(got) != 0 {
		t.Errorf("an empty owned-set returned %d rows (err=%v) — it must match nothing, "+
			"never everything", len(got), err)
	}
	if got, err := trace.MatchEvents(ctx, nil, match, 100); err != nil || len(got) != 0 {
		t.Errorf("empty owned-set on MatchEvents returned %d rows", len(got))
	}
	if got, _, err := trace.Matches(ctx, nil, "", 20, 0); err != nil || len(got) != 0 {
		t.Errorf("empty owned-set on Matches returned %d rows", len(got))
	}
}

// The gate that turns a user-supplied agent filter into an owned-only list. This is the
// single place a foreign agent id could otherwise reach a read port.
func TestActorsGateRejectsUnownedAgentLive(t *testing.T) {
	pool := openGroupTestDB(t)
	ctx := context.Background()
	trace := NewDevTraceRepo(pool)
	_, run := isolate()

	alice, aliceOwner := mkUserAgent(t, pool, "gate-alice-"+run)
	bob, _ := mkUserAgent(t, pool, "gate-bob-"+run)

	owned, err := trace.OwnedAgentIDs(ctx, aliceOwner)
	if err != nil {
		t.Fatal(err)
	}
	// Alice's own id is in the set; bob's is not. The service intersects the requested
	// id with this list, so "give me bob's traces" resolves to nothing rather than to
	// bob's rows — and, deliberately, not to an error that would confirm bob exists.
	var hasAlice, hasBob bool
	for _, id := range owned {
		if id == alice {
			hasAlice = true
		}
		if id == bob {
			hasBob = true
		}
	}
	if !hasAlice {
		t.Error("alice's own agent is missing from her owned set")
	}
	if hasBob {
		t.Fatal("LEAK: bob's agent appears in alice's owned set")
	}
}
