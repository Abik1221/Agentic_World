package store_test

// Ledger conservation for BOT-FILLED staked tables — the money path group matchmaking
// introduced.
//
// A table that house bots had to fill has more SEATS than STAKEHOLDERS: the humans pay an
// entry fee, the fillers pay nothing. Every settlement path reconstructs what escrow holds
// by multiplying the bid by the number of agents on the match, so if fillers are counted
// there, settlement debits escrow for coins that were never staked and posts the phantom
// difference to platform revenue — draining the shared escrow that backs OTHER live
// matches. Refund is worse: it credits each bot its "stake back", minting coins.
//
// These tests drive the REAL wallet + ledger against Postgres and assert whole-book
// conservation, so nothing here can pass on a fake that merely trusts its inputs.
//
//	PYYOL_TEST_DATABASE_URL=postgres://… go test ./internal/store/ -run Conservation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/store"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// openConservationDB opens (and migrates) the throwaway Postgres these tests need.
// DATABASE_URL is accepted as a fallback because that is what CI's database job sets.
func openConservationDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		dsn = os.Getenv("DATABASE_URL")
	}
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL (or DATABASE_URL) to a Postgres DSN to run the conservation tests")
	}
	if err := store.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// botTableFixture is a staked Mafia table with `humans` real stakeholders and `bots`
// kind='house' fillers, seated in that order.
type botTableFixture struct {
	matchPID string
	humans   []string
	bots     []string
	bid      int64
}

// seedBotFilledMatch writes the rows a real bot-filled table would have: one
// match_players row per SEAT (humans and fillers alike), because that is exactly the
// shape that makes a seat-counting settlement wrong.
func seedBotFilledMatch(t *testing.T, pool *pgxpool.Pool, run string, humans, bots int, bid int64) botTableFixture {
	t.Helper()
	ctx := context.Background()
	fx := botTableFixture{matchPID: "mf_cons_" + run, bid: bid}

	var creatorUserID int64
	mkAgent := func(pub, ownerPub, kind string) int64 {
		var uid, aid int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO users (public_id) VALUES ($1)
			 ON CONFLICT (public_id) DO UPDATE SET updated_at = now() RETURNING id`, ownerPub).Scan(&uid); err != nil {
			t.Fatalf("user %s: %v", ownerPub, err)
		}
		if err := pool.QueryRow(ctx,
			`INSERT INTO agents (public_id, owner_user_id, name, slug, kind) VALUES ($1,$2,$3,$4,$5)
			 ON CONFLICT (public_id) DO UPDATE SET kind = EXCLUDED.kind RETURNING id`,
			pub, uid, pub, pub, kind).Scan(&aid); err != nil {
			t.Fatalf("agent %s: %v", pub, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO wallets (agent_id, kind, balance) SELECT $1,'agent',0
			 WHERE NOT EXISTS (SELECT 1 FROM wallets WHERE agent_id = $1)`, aid); err != nil {
			t.Fatalf("wallet %s: %v", pub, err)
		}
		if creatorUserID == 0 {
			creatorUserID = uid
		}
		return aid
	}

	type seat struct {
		agentID int64
		ownerID int64
	}
	var seats []seat
	for i := 0; i < humans; i++ {
		pub := fmt.Sprintf("ag_cons_h%d_%s", i, run)
		aid := mkAgent(pub, fmt.Sprintf("u_cons_h%d_%s", i, run), "external") // a real developer's agent
		fx.humans = append(fx.humans, pub)
		var oid int64
		_ = pool.QueryRow(ctx, `SELECT owner_user_id FROM agents WHERE id=$1`, aid).Scan(&oid)
		seats = append(seats, seat{aid, oid})
	}
	for i := 0; i < bots; i++ {
		pub := fmt.Sprintf("ag_house_cons_b%d_%s", i, run)
		aid := mkAgent(pub, "u_cons_sys_"+run, "house") // all fillers share one system owner
		fx.bots = append(fx.bots, pub)
		var oid int64
		_ = pool.QueryRow(ctx, `SELECT owner_user_id FROM agents WHERE id=$1`, aid).Scan(&oid)
		seats = append(seats, seat{aid, oid})
	}

	var matchID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, rake_pct, engine_version, prize_seed_commit,
		     creator_owner_user_id, rated, finished_at)
		 VALUES ($1,'mafia','finished',$2,10,'test','x',$3,false, now()) RETURNING id`,
		fx.matchPID, bid, creatorUserID).Scan(&matchID); err != nil {
		t.Fatalf("insert match: %v", err)
	}
	for i, s := range seats {
		if _, err := pool.Exec(ctx,
			`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat, final_score, coins_delta)
			 VALUES ($1,$2,$3,$4,0,0)`, matchID, s.agentID, s.ownerID, i+1); err != nil {
			t.Fatalf("insert seat %d: %v", i+1, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM matches WHERE public_id = $1`, fx.matchPID)
	})
	return fx
}

func newWalletSvc(t *testing.T, pool *pgxpool.Pool) (*wallet.Service, *ledger.Service, *slog.Logger) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := prometheus.NewRegistry()
	ledgerSvc := ledger.New(store.NewLedgerRepo(pool), reg)
	svc := wallet.New(ledgerSvc, store.NewWalletRepo(pool), platform.NewClock(), wallet.Config{CoinCents: 1}, reg)
	return svc, ledgerSvc, log
}

// Settling a bot-filled table must debit escrow for exactly what the HUMANS staked. If
// settlement counts seats instead of stakeholders it debits 12×bid against a 4×bid escrow,
// which shows up here as escrow drift and inflated platform revenue.
func TestBotFilledTableSettlementConservation(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	svc, ledgerSvc, log := newWalletSvc(t, pool)

	run := time.Now().Format("150405.000")
	const humans, bots = 4, 8
	const bid = int64(500)
	fx := seedBotFilledMatch(t, pool, run, humans, bots, bid)

	// Fund only the humans, then stake only the humans — exactly what startMatch does.
	for i, ag := range fx.humans {
		if err := svc.Mint(ctx, ag, bid, fmt.Sprintf("mint:%s:%d", run, i)); err != nil {
			t.Fatalf("mint %s: %v", ag, err)
		}
	}
	escrowBefore := sysBalance(t, pool, ledger.SysEscrow)
	revenueBefore := sysBalance(t, pool, ledger.SysPlatformRevenue)
	totalBefore := grandTotal(t, pool)

	if err := svc.StakeMafiaTable(ctx, fx.matchPID, fx.humans, bid); err != nil {
		t.Fatalf("stake: %v", err)
	}
	staked := bid * humans
	if got := sysBalance(t, pool, ledger.SysEscrow) - escrowBefore; got != staked {
		t.Fatalf("escrow took %d, want %d (only the %d humans stake)", got, staked, humans)
	}
	assertNoDrift(t, ledgerSvc, log, "after staking a bot-filled table")

	// Settle: 10% rake on what was actually staked, rest to the single human winner.
	fee := staked / 10
	payouts := map[string]int64{fx.humans[0]: staked - fee}
	if err := svc.SettleMafiaTable(ctx, fx.matchPID, fee, payouts); err != nil {
		t.Fatalf("settle: %v", err)
	}

	if got := sysBalance(t, pool, ledger.SysEscrow); got != escrowBefore {
		t.Errorf("escrow did not return to its starting balance: %d, want %d — settlement "+
			"debited coins that were never staked", got, escrowBefore)
	}
	if got := sysBalance(t, pool, ledger.SysPlatformRevenue) - revenueBefore; got != fee {
		t.Errorf("platform revenue moved %d, want exactly the %d rake — a phantom "+
			"remainder from counting bot seats would inflate this", got, fee)
	}
	if got := grandTotal(t, pool); got != totalBefore {
		t.Errorf("grand total changed by %d — coins were created or destroyed", got-totalBefore)
	}
	// No filler may hold coins.
	for _, b := range fx.bots {
		if bal := agentBalance(t, pool, b); bal != 0 {
			t.Errorf("house filler %s holds %d coins, want 0", b, bal)
		}
	}
	assertNoDrift(t, ledgerSvc, log, "after settling a bot-filled table")
}

// Refunding a bot-filled table (no winner, or an abort) must return each HUMAN its stake
// and nothing to the fillers. Counting seats here would mint coins into the system owner's
// wallet while over-drawing escrow.
func TestBotFilledTableRefundConservation(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	svc, ledgerSvc, log := newWalletSvc(t, pool)

	run := time.Now().Format("150405.000") + "r"
	const humans, bots = 3, 9
	const bid = int64(500)
	fx := seedBotFilledMatch(t, pool, run, humans, bots, bid)

	for i, ag := range fx.humans {
		if err := svc.Mint(ctx, ag, bid, fmt.Sprintf("mintr:%s:%d", run, i)); err != nil {
			t.Fatalf("mint: %v", err)
		}
	}
	escrowBefore := sysBalance(t, pool, ledger.SysEscrow)
	totalBefore := grandTotal(t, pool)

	if err := svc.StakeMafiaTable(ctx, fx.matchPID, fx.humans, bid); err != nil {
		t.Fatalf("stake: %v", err)
	}
	if err := svc.Refund(ctx, fx.matchPID); err != nil {
		t.Fatalf("refund: %v", err)
	}

	if got := sysBalance(t, pool, ledger.SysEscrow); got != escrowBefore {
		t.Errorf("escrow %d after refund, want %d", got, escrowBefore)
	}
	for _, ag := range fx.humans {
		if bal := agentBalance(t, pool, ag); bal != bid {
			t.Errorf("human %s holds %d after refund, want its %d stake back", ag, bal, bid)
		}
	}
	for _, b := range fx.bots {
		if bal := agentBalance(t, pool, b); bal != 0 {
			t.Errorf("house filler %s was refunded %d coins it never staked", b, bal)
		}
	}
	if got := grandTotal(t, pool); got != totalBefore {
		t.Errorf("grand total changed by %d on a refund — coins created or destroyed", got-totalBefore)
	}
	assertNoDrift(t, ledgerSvc, log, "after refunding a bot-filled table")
}

// Settlement must be idempotent: the outbox can redeliver, and the sweeper re-drives a
// match that crashed between settle and finish. A second settle must move nothing.
func TestBotFilledSettlementIdempotentUnderRedelivery(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	svc, ledgerSvc, log := newWalletSvc(t, pool)

	run := time.Now().Format("150405.000") + "i"
	const humans, bots = 5, 7
	const bid = int64(200)
	fx := seedBotFilledMatch(t, pool, run, humans, bots, bid)
	for i, ag := range fx.humans {
		if err := svc.Mint(ctx, ag, bid, fmt.Sprintf("minti:%s:%d", run, i)); err != nil {
			t.Fatalf("mint: %v", err)
		}
	}
	escrowBaseline := sysBalance(t, pool, ledger.SysEscrow)
	if err := svc.StakeMafiaTable(ctx, fx.matchPID, fx.humans, bid); err != nil {
		t.Fatalf("stake: %v", err)
	}
	staked := bid * humans
	fee := staked / 10
	payouts := map[string]int64{fx.humans[0]: staked - fee}

	if err := svc.SettleMafiaTable(ctx, fx.matchPID, fee, payouts); err != nil {
		t.Fatalf("settle 1: %v", err)
	}
	// The first settle must fully unwind THIS table's escrow — no more, no less. Asserted
	// here as well as in the conservation tests so this test cannot pass on a settlement
	// that moved the wrong gross and happened to survive redelivery consistently.
	if got := sysBalance(t, pool, ledger.SysEscrow); got != escrowBaseline {
		t.Fatalf("escrow %d after the first settle, want %d — settlement disbursed a "+
			"different amount than the humans staked", got, escrowBaseline)
	}
	afterFirst := grandTotal(t, pool)
	winnerAfterFirst := agentBalance(t, pool, fx.humans[0])
	escrowAfterFirst := sysBalance(t, pool, ledger.SysEscrow)

	// Redelivery: same match, same split, three more times.
	for i := 0; i < 3; i++ {
		if err := svc.SettleMafiaTable(ctx, fx.matchPID, fee, payouts); err != nil {
			t.Fatalf("settle redelivery %d: %v", i+2, err)
		}
	}
	// And a re-staking attempt, which the sweeper's re-drive could trigger.
	if err := svc.StakeMafiaTable(ctx, fx.matchPID, fx.humans, bid); err != nil {
		t.Fatalf("re-stake: %v", err)
	}

	if got := grandTotal(t, pool); got != afterFirst {
		t.Errorf("grand total moved by %d across redelivery — settlement is not idempotent", got-afterFirst)
	}
	if got := agentBalance(t, pool, fx.humans[0]); got != winnerAfterFirst {
		t.Errorf("winner was paid twice: %d, want %d", got, winnerAfterFirst)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow); got != escrowAfterFirst {
		t.Errorf("escrow moved on redelivery: %d, want %d", got, escrowAfterFirst)
	}
	assertNoDrift(t, ledgerSvc, log, "after settlement redelivery")
}

// Conservation must hold across MANY settlements with varied human/bot splits, bids, and
// winner counts — the per-match tests above cannot see a remainder bug that only shows up
// in aggregate.
func TestManyBotFilledSettlementsConserveTheBook(t *testing.T) {
	pool := openConservationDB(t)
	ctx := context.Background()
	svc, ledgerSvc, log := newWalletSvc(t, pool)

	base := time.Now().Format("150405.000")
	totalBefore := grandTotal(t, pool)
	escrowBefore := sysBalance(t, pool, ledger.SysEscrow)

	// Deterministic spread: seat counts from 2..11 humans, the rest bots, varied bids and
	// winner counts (including a no-winner refund).
	cases := []struct {
		humans, winners int
		bid             int64
	}{
		{2, 1, 500}, {3, 2, 100}, {4, 1, 750}, {5, 3, 200}, {6, 2, 333},
		{7, 1, 1000}, {8, 4, 125}, {9, 3, 900}, {10, 5, 50}, {11, 0, 600},
	}
	for i, tc := range cases {
		run := fmt.Sprintf("%s_m%02d", base, i)
		fx := seedBotFilledMatch(t, pool, run, tc.humans, 12-tc.humans, tc.bid)
		for j, ag := range fx.humans {
			if err := svc.Mint(ctx, ag, tc.bid, fmt.Sprintf("mintm:%s:%d", run, j)); err != nil {
				t.Fatalf("mint: %v", err)
			}
		}
		if err := svc.StakeMafiaTable(ctx, fx.matchPID, fx.humans, tc.bid); err != nil {
			t.Fatalf("stake %d: %v", i, err)
		}
		staked := tc.bid * int64(tc.humans)

		if tc.winners == 0 {
			// No winner ⇒ refund every staker, no rake (the draw path).
			if err := svc.Refund(ctx, fx.matchPID); err != nil {
				t.Fatalf("refund %d: %v", i, err)
			}
		} else {
			fee := staked * 10 / 100
			share := (staked - fee) / int64(tc.winners) // floor; remainder → platform
			payouts := map[string]int64{}
			for _, ag := range fx.humans[:tc.winners] {
				payouts[ag] = share
			}
			if err := svc.SettleMafiaTable(ctx, fx.matchPID, fee, payouts); err != nil {
				t.Fatalf("settle %d: %v", i, err)
			}
		}
		// Escrow must be flat again after every single table.
		if got := sysBalance(t, pool, ledger.SysEscrow); got != escrowBefore {
			t.Fatalf("case %d (%d humans, %d bots, bid %d): escrow %d, want %d — this table "+
				"left coins in (or took coins out of) the shared escrow",
				i, tc.humans, 12-tc.humans, tc.bid, got, escrowBefore)
		}
		assertNoDrift(t, ledgerSvc, log, fmt.Sprintf("case %d", i))
	}

	if got := grandTotal(t, pool); got != totalBefore {
		t.Errorf("grand total drifted by %d across %d settlements — the mint total should be "+
			"the only change and it is already counted", got-totalBefore, len(cases))
	}
}

func agentBalance(t *testing.T, pool *pgxpool.Pool, agentPublicID string) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(w.balance,0) FROM wallets w JOIN agents a ON a.id = w.agent_id
		 WHERE a.public_id = $1`, agentPublicID).Scan(&bal); err != nil {
		t.Fatalf("balance %s: %v", agentPublicID, err)
	}
	return bal
}
