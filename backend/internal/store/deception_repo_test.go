package store

import (
	"context"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/deception"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Runs the deception index against a real database.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run DeceptionIndex
func TestDeceptionIndexAgainstLiveDatabase(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to score a real ledger of matches")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer pool.Close()

	scores, err := NewDeceptionRepo(pool).SeatScores(ctx, 50)
	if err != nil {
		t.Fatalf("scoring: %v", err)
	}
	if len(scores) == 0 {
		// Not a pass. Mafia produced zero finished matches for most of this platform's life,
		// and a silent green tick here would say the index works when it has scored nothing.
		t.Skip("no finished mafia matches with recorded votes yet — nothing to score")
	}

	for _, s := range scores {
		t.Log("  " + s.Describe())
		// Every seat must be scored by exactly ONE metric. Both would mean a mafia seat is
		// being read as accurate; neither (with votes cast) means a role fell through.
		_, mis := s.Misdirection()
		_, acc := s.Accuracy()
		if mis && acc {
			t.Errorf("seat %d (%s) scored as BOTH deceptive and accurate", s.Seat, s.Role)
		}
		if s.VotesCast > 0 && !mis && !acc {
			t.Errorf("seat %d has %d votes but no applicable metric — role %q was not classified",
				s.Seat, s.VotesCast, s.Role)
		}
		if s.VotesOnMafia+s.VotesOnTown > s.VotesCast {
			t.Errorf("seat %d: partition exceeds total (%d+%d > %d)",
				s.Seat, s.VotesOnMafia, s.VotesOnTown, s.VotesCast)
		}
	}

	for role, a := range deception.Aggregate(scores) {
		t.Logf("  %-10s seats=%d votes=%d on_mafia=%d on_town=%d",
			role, a.Seats, a.VotesCast, a.VotesOnMafia, a.VotesOnTown)
	}
}
