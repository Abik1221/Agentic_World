package store_test

// Ledger conservation for the ABSENCE FORFEIT.
//
// The arena's rule: an agent that starts a staked match and goes dark LOSES it. The match
// settles, the opponent is paid, and the absent seat's stake is forfeit — it is not
// refunded and the match is not voided.
//
// Before this, a seat that went dark tripped the ranked integrity gate (it answered
// nothing, so it proved nothing) and the whole match was VOIDED: both stakes returned.
// That is a real money bug in the shape the arena cares about, because it pays the agent
// that walked away and cancels the win of the agent that showed up and paid for inference.
//
// Asserted against the REAL wallet and ledger on Postgres. A fake wallet cannot see this:
// it would happily report whatever payout it was handed, while the actual defect —
// whether escrow drains to zero and whether coins are conserved — lives in SQL.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run Absence

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
)

// seedHeadsUpMatch writes a finished 2-seat staked Goofspiel match: seat 1 the agent that
// stayed, seat 2 the agent that went dark.
func seedHeadsUpMatch(t *testing.T, pool *pgxpool.Pool, run string, bid int64) (matchPID, present, absent string) {
	t.Helper()
	ctx := context.Background()
	matchPID = "m_abs_" + run
	present = "ag_abs_present_" + run
	absent = "ag_abs_dark_" + run

	var creatorUserID int64
	mk := func(pub, ownerPub string) (agentID, ownerID int64) {
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (public_id) VALUES ($1)
			 ON CONFLICT (public_id) DO UPDATE SET updated_at = now() RETURNING id`, ownerPub).Scan(&ownerID); err != nil {
			t.Fatalf("user %s: %v", ownerPub, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO agents (public_id, owner_user_id, name, slug, kind) VALUES ($1,$2,$3,$4,'external')
			 ON CONFLICT (public_id) DO UPDATE SET kind = EXCLUDED.kind RETURNING id`,
			pub, ownerID, pub, pub).Scan(&agentID); err != nil {
			t.Fatalf("agent %s: %v", pub, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO wallets (agent_id, kind, balance) SELECT $1,'agent',0
			 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE agent_id = $1)`, agentID); err != nil {
			t.Fatalf("wallet %s: %v", pub, err)
		}
		if creatorUserID == 0 {
			creatorUserID = ownerID
		}
		return agentID, ownerID
	}

	pAID, pOID := mk(present, "u_abs_present_"+run)
	aAID, aOID := mk(absent, "u_abs_dark_"+run)

	var matchID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, rake_pct, engine_version, prize_seed_commit,
		     creator_owner_user_id, rated, finished_at)
		 VALUES ($1,'goofspiel','finished',$2,10,'test','x',$3,true, now()) RETURNING id`,
		matchPID, bid, creatorUserID).Scan(&matchID); err != nil {
		t.Fatalf("insert match: %v", err)
	}
	for i, s := range []struct {
		agentID, ownerID int64
	}{{pAID, pOID}, {aAID, aOID}} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat, final_score, coins_delta)
			 VALUES ($1,$2,$3,$4,0,0)`, matchID, s.agentID, s.ownerID, i+1); err != nil {
			t.Fatalf("insert seat %d: %v", i+1, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM matches WHERE public_id = $1`, matchPID)
	})
	return matchPID, present, absent
}

// The forfeit path: settle to the agent that stayed. The absent seat's stake must end up
// with the winner and the platform, and nowhere else.
func TestAbsenceForfeitConservation(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	svc, _, _ := newWalletSvc(t, pool)

	run := time.Now().Format("150405.000")
	const bid = int64(500)
	const rakePct = 10
	matchPID, present, absent := seedHeadsUpMatch(t, pool, run, bid)

	for i, ag := range []string{present, absent} {
		if err := svc.Mint(ctx, ag, bid, fmt.Sprintf("mint:abs:%s:%d", run, i)); err != nil {
			t.Fatalf("mint %s: %v", ag, err)
		}
	}

	escrowBefore := sysBalance(t, pool, ledger.SysEscrow)
	revenueBefore := sysBalance(t, pool, ledger.SysPlatformRevenue)
	totalBefore := grandTotal(t, pool)

	if err := svc.StakeMatch(ctx, matchPID, present, absent, bid); err != nil {
		t.Fatalf("stake: %v", err)
	}
	if got := agentBalance(t, pool, absent); got != 0 {
		t.Fatalf("absent seat holds %d after staking, want 0 — its coins should be in escrow", got)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow) - escrowBefore; got != 2*bid {
		t.Fatalf("escrow took %d, want %d", got, 2*bid)
	}

	// The seat went dark and lost on the board. Settle — do NOT void.
	pool2 := 2 * bid
	if err := svc.Settle(ctx, matchPID, present, pool2, rakePct); err != nil {
		t.Fatalf("settle: %v", err)
	}

	rake := pool2 * rakePct / 100
	wantWinner := pool2 - rake

	if got := agentBalance(t, pool, present); got != wantWinner {
		t.Errorf("winner holds %d, want %d — the agent that showed up must be paid the "+
			"forfeited stake less rake", got, wantWinner)
	}
	if got := agentBalance(t, pool, absent); got != 0 {
		t.Errorf("the ABSENT seat holds %d, want 0. Its stake was returned, which means walking "+
			"away from a staked match is free and the forfeit rule is not in force", got)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow) - escrowBefore; got != 0 {
		t.Errorf("escrow drifted by %d after settlement, want 0 — stranded or over-drawn coins "+
			"here corrupt every other live match sharing this escrow", got)
	}
	if got := sysBalance(t, pool, ledger.SysPlatformRevenue) - revenueBefore; got != rake {
		t.Errorf("platform revenue moved %d, want the rake %d", got, rake)
	}
	// Whole-book conservation. Zero, not the minted amount: a mint is double-entry — it
	// moves coins out of a system wallet rather than conjuring them — so the sum across
	// EVERY wallet is invariant through mint, stake and settle alike. Any drift here is
	// the ledger inventing or destroying money.
	if got := grandTotal(t, pool) - totalBefore; got != 0 {
		t.Errorf("grand total moved %d across mint+stake+settle, want 0 — coins are being "+
			"created or destroyed somewhere on the forfeit path", got)
	}
}

// The behaviour being replaced, priced out on the real ledger.
//
// This is not a regression guard; it is the reason the rule changed. Refunding leaves the
// agent that walked away exactly as rich as it started and takes the win away from the one
// that played. Both outcomes are individually conservative and together they are the wrong
// product: absence becomes free, and showing up becomes a bad bet.
func TestVoidingAnAbsentSeatRefundsTheAgentThatWalkedAway(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	svc, _, _ := newWalletSvc(t, pool)

	run := time.Now().Format("150405.000") + "v"
	const bid = int64(500)
	matchPID, present, absent := seedHeadsUpMatch(t, pool, run, bid)

	for i, ag := range []string{present, absent} {
		if err := svc.Mint(ctx, ag, bid, fmt.Sprintf("mint:void:%s:%d", run, i)); err != nil {
			t.Fatalf("mint %s: %v", ag, err)
		}
	}
	escrowBefore := sysBalance(t, pool, ledger.SysEscrow)
	totalBefore := grandTotal(t, pool)

	if err := svc.StakeMatch(ctx, matchPID, present, absent, bid); err != nil {
		t.Fatalf("stake: %v", err)
	}
	if err := svc.Refund(ctx, matchPID); err != nil {
		t.Fatalf("refund: %v", err)
	}

	// Conservation still holds — the void was never a LEDGER bug, which is exactly why
	// unit tests over a fake wallet never caught it. It is a policy bug, visible only in
	// who ends up holding the coins.
	if got := agentBalance(t, pool, absent); got != bid {
		t.Fatalf("refund left the absent seat with %d, want its full %d back", got, bid)
	}
	if got := agentBalance(t, pool, present); got != bid {
		t.Fatalf("refund left the winner with %d, want %d — it won and received nothing extra",
			got, bid)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow) - escrowBefore; got != 0 {
		t.Errorf("escrow drifted by %d after refund, want 0", got)
	}
	if got := grandTotal(t, pool) - totalBefore; got != 0 {
		t.Errorf("grand total moved %d across mint+stake+refund, want 0", got)
	}
}
