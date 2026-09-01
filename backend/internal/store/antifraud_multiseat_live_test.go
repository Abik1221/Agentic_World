//go:build multiseatlive

package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/agent-arena/arena/internal/antifraud"
)

// Live test against a database seeded with a colluding Mafia ring and a Monopoly gifter.
//
//	go test -tags multiseatlive ./internal/store/ -run TestMultiSeatEvidence
//
// Against the real thing because the whole question is whether the SQL reads the event log
// correctly. A fake repo would test the fake.
func TestMultiSeatEvidence(t *testing.T) {
	dsn := os.Getenv("MULTISEAT_TEST_DSN")
	if dsn == "" {
		t.Skip("MULTISEAT_TEST_DSN not set")
	}
	ctx := context.Background()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	r := NewMultiSeatRepo(db)
	since := time.Now().Add(-24 * time.Hour)

	// ── Mafia: ag_ring1 and ag_ring2 always vote together; ag_honest does not ──────────
	ring, err := r.PairVotes(ctx, "ag_ring1", "ag_ring2", since)
	if err != nil {
		t.Fatalf("PairVotes(ring): %v", err)
	}
	if len(ring) == 0 {
		t.Fatal("read no votes for the ring; the query is not seeing the event log")
	}
	k := antifraud.VoteAgreement(ring)
	t.Logf("ring pair: %d co-voted rounds, kappa %.3f, suspect=%v",
		len(ring), k, antifraud.VoteSuspect(ring))
	if k < 0.9 {
		t.Errorf("a perfectly coordinated ring read back at kappa %.3f", k)
	}

	honest, err := r.PairVotes(ctx, "ag_ring1", "ag_honest", since)
	if err != nil {
		t.Fatalf("PairVotes(honest): %v", err)
	}
	hk := antifraud.VoteAgreement(honest)
	t.Logf("honest pair: %d co-voted rounds, kappa %.3f, suspect=%v",
		len(honest), hk, antifraud.VoteSuspect(honest))
	if antifraud.VoteSuspect(honest) {
		t.Errorf("an honest pair was flagged (kappa %.3f)", hk)
	}
	if hk >= k {
		t.Errorf("the honest pair (%.3f) scored at least as high as the ring (%.3f)", hk, k)
	}

	// ── Monopoly: ag_gift hands property to ag_take for nothing ───────────────────────
	trades, err := r.PairTrades(ctx, "ag_gift", "ag_take", since)
	if err != nil {
		t.Fatalf("PairTrades: %v", err)
	}
	if len(trades) == 0 {
		t.Fatal("read no trades; the query is not seeing trade_executed events")
	}
	score, toB := antifraud.TransferAsymmetry(trades)
	t.Logf("gifter pair: %d trades, asymmetry %.3f, A is net giver=%v, suspect=%v",
		len(trades), score, toB, antifraud.TradeSuspect(trades))
	if !antifraud.TradeSuspect(trades) {
		t.Errorf("systematic gifting was not detected (asymmetry %.3f)", score)
	}
	if !toB {
		t.Error("the transfer direction is backwards: ag_gift is the giver")
	}

	// Direction must invert when the pair is queried the other way round. This is what the
	// removed seat-mapping approximation would have got wrong.
	rev, err := r.PairTrades(ctx, "ag_take", "ag_gift", since)
	if err != nil {
		t.Fatal(err)
	}
	_, revToB := antifraud.TransferAsymmetry(rev)
	if revToB {
		t.Error("querying the pair in reverse did not invert the direction; the proposer " +
			"resolution is wrong")
	}
}
