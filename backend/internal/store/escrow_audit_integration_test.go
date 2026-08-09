package store

import (
	"context"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A HELD Mafia settlement is escrow with a story, and the audit must say so.
//
// # The false critical this exists to stop
//
// There are two hold records. A 1v1 payout withheld by the gate writes payout_holds, keyed by
// matches.id. A Mafia table settled while the gate denies writes held_settlements, keyed by
// match_public_id. The escrow reconciliation knew only the first, so every held Mafia table was
// reported as coins nobody could account for:
//
//	LEDGER INTEGRITY VIOLATION check=escrow_unexplained severity=critical rows=2700
//
// That is worse than a missing alert. A critical that fires on correct behaviour trains
// everyone to read the next real one as noise — and this one names the most alarming condition
// the ledger has ("the stake was taken and there is no story for where it went").
//
// Needs a real Postgres: the whole bug lives in one SQL statement's knowledge of which tables
// record a hold, and a fake repo would simply return whatever it was told.
func TestHeldMafiaSettlementIsExplainedEscrow(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// t.Cleanup, not defer. A deferred Close runs when the test function RETURNS, which is
	// before every t.Cleanup — so a cleanup that deletes rows through this pool was running
	// against a closed pool and silently doing nothing (the deletes ignore their errors).
	// Registered FIRST so LIFO ordering runs it LAST, after the data cleanups.
	t.Cleanup(pool.Close)

	const matchID = "m_escrowaudit_mafia_hold"
	cleanup := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM held_settlements WHERE match_public_id = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM ledger_transactions WHERE metadata->>'match' = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM match_players WHERE match_id IN
			(SELECT id FROM matches WHERE public_id = $1)`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM matches WHERE public_id = $1`, matchID)
	}
	cleanup()
	t.Cleanup(cleanup)

	var ownerID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM users ORDER BY id LIMIT 1`).Scan(&ownerID); err != nil {
		t.Skipf("no users in this database: %v", err)
	}

	// A FINISHED mafia match whose stake was taken and never settled or refunded — which is
	// exactly what a deny-gated settlement leaves behind.
	var mid int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, rake_pct, total_rounds,
		     engine_version, prize_seed_commit, fairness_mode, creator_owner_user_id, finished_at)
		 VALUES ($1,'mafia','finished',300,5,13,'itest','','shuffled',$2, now())
		 RETURNING id`, matchID, ownerID).Scan(&mid); err != nil {
		t.Fatalf("seed match: %v", err)
	}
	// Three seats, because the stake this check reconciles is bid × players. With no players the
	// match is worth zero coins and the finding cannot fire at all — which would make the
	// negative half of this test pass for entirely the wrong reason.
	if _, err := pool.Exec(ctx,
		`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat)
		 SELECT $1, a.id, a.owner_user_id, row_number() OVER (ORDER BY a.id) - 1
		   FROM (SELECT id, owner_user_id FROM agents ORDER BY id LIMIT 3) a`, mid); err != nil {
		t.Fatalf("seed players: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO ledger_transactions (public_id, kind, idempotency_key, metadata)
		 VALUES ('tx_escrowaudit_stake', 'stake', 'idem_escrowaudit_stake',
		         jsonb_build_object('match', $1::text))`,
		matchID); err != nil {
		t.Fatalf("seed stake txn: %v", err)
	}

	repo := NewLedgerRepo(pool)

	// WITHOUT the hold record the audit must call this out — otherwise the test would pass for
	// the wrong reason, and a check that never fires is not a check.
	rep, err := repo.AuditLedger(ctx)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if findingCount(rep.Findings, "escrow_unexplained") == 0 {
		t.Fatal("a staked, finished match with NO hold record of any kind was not flagged — " +
			"the escrow check is not actually looking at this match, so the rest of this test " +
			"would prove nothing")
	}

	// Now record the hold the Mafia deny-gate path writes: the fee and the payout map, stating
	// exactly where the retained coins are going.
	if _, err := pool.Exec(ctx,
		`INSERT INTO held_settlements (match_public_id, platform_fee, payouts)
		 VALUES ($1, 90, '{"ag_x": 810}'::jsonb)`, matchID); err != nil {
		t.Fatalf("seed held settlement: %v", err)
	}

	rep, err = repo.AuditLedger(ctx)
	if err != nil {
		t.Fatalf("Audit: %v", err)
	}
	if n := findingCount(rep.Findings, "escrow_unexplained"); n != 0 {
		t.Errorf("escrow_unexplained fired for %d coins on a match whose retention IS recorded, "+
			"in held_settlements with its fee and payout map. Escrow held for review is not "+
			"escrow with no story", n)
	}
}

// findingCount reports how many rows a named check flagged, or 0 when it did not fire.
func findingCount(findings []ledger.AuditFinding, check string) int64 {
	for _, f := range findings {
		if f.Check == check {
			return f.Count
		}
	}
	return 0
}
