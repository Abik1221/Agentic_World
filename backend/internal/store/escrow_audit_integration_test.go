package store

import (
	"context"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// payout_holds is what explains retained escrow — a payout SPLIT on its own does not.
//
// # What this pins, and the mistake it records
//
// held_settlements is not a second hold record. payout_holds says a match's payout IS held;
// held_settlements carries the multi-winner split to replay on release. Every deny branch of
// the real gate calls RecordHold, so a genuinely held match always has both.
//
// A match with a split and NO hold state is therefore a defect, not a false alarm: a deny path
// wrote the payout map and forgot the hold, leaving coins retained with nothing marking them
// retained and nothing for an admin release to claim. It must keep firing.
//
// I got this backwards first. A critical fired on three such matches:
//
//	LEDGER INTEGRITY VIOLATION check=escrow_unexplained severity=critical rows=2700
//
// and I widened the check to accept a split alone. The three came from a test whose denyGate
// stub refused without recording a hold — a state production cannot reach — so the widening
// would have masked exactly the defect the check exists to catch. The fixture was wrong, not
// the audit. This test now pins the strict rule in both directions.
//
// Needs a real Postgres: the bug lives in one SQL statement's knowledge of which tables record
// a hold, and a fake repo would simply return whatever it was told.
func TestOnlyAPayoutHoldExplainsRetainedEscrow(t *testing.T) {
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
		_, _ = pool.Exec(ctx, `DELETE FROM payout_holds WHERE match_id IN
			(SELECT id FROM matches WHERE public_id = $1)`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM ledger_transactions WHERE metadata->>'match' = $1`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM match_players WHERE match_id IN
			(SELECT id FROM matches WHERE public_id = $1)`, matchID)
		_, _ = pool.Exec(ctx, `DELETE FROM matches WHERE public_id = $1`, matchID)
	}
	cleanup()
	t.Cleanup(cleanup)

	repo := NewLedgerRepo(pool)

	// Asserted as a DELTA against this match's own stake, never as the global figure. The audit
	// reconciles the whole database, so any unrelated unexplained escrow — a stale fixture, a
	// genuine open incident — would otherwise decide the result of this test.
	unexplained := func() int64 {
		t.Helper()
		rep, err := repo.AuditLedger(ctx)
		if err != nil {
			t.Fatalf("AuditLedger: %v", err)
		}
		return findingCount(rep.Findings, "escrow_unexplained")
	}
	const stake = 900 // 300 bid × 3 seats
	baseline := unexplained()

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


	// WITHOUT the hold record the audit must call this out — otherwise the test would pass for
	// the wrong reason, and a check that never fires is not a check.
	withMatch := unexplained()
	if withMatch < baseline+stake {
		t.Fatalf("unexplained escrow rose by %d when a staked, finished match with NO hold record "+
			"was added; want +%d. The escrow check is not looking at this match, so the rest of "+
			"this test would prove nothing", withMatch-baseline, stake)
	}

	// A payout SPLIT alone must NOT clear it. This is the half I originally got wrong: a split
	// with no hold state means a deny path forgot to mark the coins retained, which is a defect
	// the audit should keep shouting about — not evidence that everything is fine.
	if _, err := pool.Exec(ctx,
		`INSERT INTO held_settlements (match_public_id, platform_fee, payouts)
		 VALUES ($1, 90, '{"ag_x": 810}'::jsonb)`, matchID); err != nil {
		t.Fatalf("seed held settlement: %v", err)
	}
	if got := unexplained(); got < baseline+stake {
		t.Errorf("a payout split with NO payout_holds row cleared %d coins from the escrow check. "+
			"That state means a deny path wrote the split and forgot the hold, leaving coins "+
			"retained with nothing marking them retained — masking it is how a real defect goes "+
			"quiet", baseline+stake-got)
	}

	// The HOLD STATE is what explains it, exactly as the real gate records it.
	if _, err := pool.Exec(ctx,
		`INSERT INTO payout_holds (match_id, reason) VALUES ($1, 'itest')`, mid); err != nil {
		t.Fatalf("seed payout hold: %v", err)
	}
	if got := unexplained(); got != baseline {
		t.Errorf("unexplained escrow is %d, want it back at the %d it started from: a match with a "+
			"recorded payout hold is escrow held for review, not escrow with no story", got, baseline)
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
