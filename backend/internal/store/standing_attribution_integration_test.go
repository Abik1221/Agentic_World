package store

import (
	"context"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/rating"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestAgentStandingAttributionIntegration runs the standings query against a REAL Postgres.
//
// The query resolves provider/model by trust across three LATERAL joins and computes coverage
// in two correlated subqueries. None of that is exercised by a unit test, and a hand-built
// query either returns the wrong column or fails outright — this endpoint has already 500'd
// once in this file's history for referencing a column that did not exist (see the comment on
// agent_manifests in the query), which silently hid the console's "your rank" card.
//
// Skipped unless PYYOL_TEST_DATABASE_URL points at a migrated DB.
func TestAgentStandingAttributionIntegration(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()
	repo := NewRatingRepo(pool)

	// Any real agent that has a rating row. The point is that the query EXECUTES and the tier
	// it returns is one of the known names — not that a particular agent has a particular one.
	var agentPublic string
	var season int
	err = pool.QueryRow(ctx,
		`SELECT a.public_id, r.season FROM ratings r JOIN agents a ON a.id = r.agent_id
		  WHERE a.kind <> 'house' ORDER BY r.season DESC, r.elo DESC LIMIT 1`).
		Scan(&agentPublic, &season)
	if err != nil {
		t.Skipf("no rated agent in this database: %v", err)
	}

	s, ok, err := repo.AgentStanding(ctx, season, "", agentPublic)
	if err != nil {
		t.Fatalf("AgentStanding: %v", err)
	}
	if !ok {
		t.Fatalf("no standing for %s in season %d", agentPublic, season)
	}

	switch s.Attribution {
	case rating.AttrVerified, rating.AttrPartial, rating.AttrObserved, rating.AttrDeclared:
	default:
		t.Fatalf("attribution = %q, want one of the four known tiers", s.Attribution)
	}
	// A model name with no provenance is what this change exists to stop, so a named model
	// must always carry a tier.
	if s.Model != "" && s.Attribution == "" {
		t.Error("a model name was returned with no attribution tier — the exact state that let " +
			"a self-declared model read as a confirmed one")
	}
	// Coverage must be internally consistent whatever the data happens to be.
	if s.Verified.BoundDecisions > s.Verified.Decisions && s.Verified.Decisions > 0 {
		t.Errorf("bound %d > decisions %d without clamping",
			s.Verified.BoundDecisions, s.Verified.Decisions)
	}
	if s.Verified.Coverage < 0 || s.Verified.Coverage > 1 {
		t.Errorf("coverage %v out of range", s.Verified.Coverage)
	}
	if !s.Verified.Known && s.Verified.Decisions != 0 {
		t.Errorf("coverage reported unknown while carrying %d decisions", s.Verified.Decisions)
	}
	// The tier can never be stronger than coverage supports.
	if s.Attribution == rating.AttrVerified &&
		(!s.Verified.Known || s.Verified.Coverage < rating.VerifiedCoverageThreshold) {
		t.Errorf("verified tier on coverage %+v — the badge outran its evidence", s.Verified)
	}
	t.Logf("agent=%s tier=%s coverage=%d/%d (%.1f%%) model=%q",
		agentPublic, s.Attribution, s.Verified.BoundDecisions, s.Verified.Decisions,
		s.Verified.Coverage*100, s.Model)
}
