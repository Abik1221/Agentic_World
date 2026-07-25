package store_test

// End-to-end money-flow test: deposit → coins → 10 agents stake a pooled match →
// winner takes the pool minus the platform fee → winner withdraws to (Stripe) cash.
// Every step asserts DOUBLE-ENTRY CONSERVATION (the ledger reconciler finds zero drift
// and the grand total of all wallet balances is invariant), so a "money swallow" — coins
// created or destroyed anywhere in the pipeline — fails the test.
//
// It also deliberately probes the money/UX gaps found in review:
//   - deposited coins are NOT withdrawable (only net match winnings are), so "withdraw
//     anytime" is false for a player who only deposited — asserted explicitly.
//   - the platform fee is actually captured (not silently lost).
//
// Run against a throwaway Postgres:
//   PYYOL_TEST_DATABASE_URL=postgres://postgres:pw@localhost:5545/arena?sslmode=disable \
//     go test ./internal/store/ -run TestMoneyFlowE2E -v

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/demo"
	"github.com/agent-arena/arena/internal/ledger"
	"github.com/agent-arena/arena/internal/payout"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/store"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// testBank adapts *ledger.Service to payout.Bank (the ~50-line adapter that lives in
// cmd/server as package main; reproduced here so the test can drive a real withdrawal).
type testBank struct{ l *ledger.Service }

func (b testBank) post(id, key string, ps []ledger.Posting) error {
	_, err := b.l.Post(context.Background(), ledger.Txn{
		Kind: ledger.KindSettle, Key: key + id, Postings: ps,
	})
	return err
}
func (b testBank) Hold(_ context.Context, id, agent string, coins int64) error {
	return b.post(id, "wh-hold:", []ledger.Posting{
		{Wallet: ledger.AgentWallet(agent), Amount: -coins},
		{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: coins},
	})
}
func (b testBank) Release(_ context.Context, id, agent string, coins int64) error {
	return b.post(id, "wh-release:", []ledger.Posting{
		{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -coins},
		{Wallet: ledger.AgentWallet(agent), Amount: coins},
	})
}
func (b testBank) Payout(_ context.Context, id, _ string, coins, feeCoins int64) error {
	return b.post(id, "wh-payout:", []ledger.Posting{
		{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -coins},
		{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: feeCoins},
		{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: coins - feeCoins},
	})
}
func (b testBank) ReversePayout(_ context.Context, id, _ string, coins, feeCoins int64) error {
	return b.post(id, "wh-reverse:", []ledger.Posting{
		{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: coins},
		{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: -feeCoins},
		{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -(coins - feeCoins)},
	})
}

func sysBalance(t *testing.T, pool *pgxpool.Pool, kind string) int64 {
	t.Helper()
	var b int64
	err := pool.QueryRow(context.Background(),
		`SELECT COALESCE(balance,0) FROM wallets WHERE kind=$1 AND agent_id IS NULL AND user_id IS NULL`, kind).Scan(&b)
	if err != nil {
		t.Fatalf("sysBalance(%s): %v", kind, err)
	}
	return b
}

func grandTotal(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var b int64
	if err := pool.QueryRow(context.Background(), `SELECT COALESCE(SUM(balance),0) FROM wallets`).Scan(&b); err != nil {
		t.Fatalf("grandTotal: %v", err)
	}
	return b
}

func TestMoneyFlowE2E_TenAgents(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a Postgres DSN (the test migrates it)")
	}
	if err := store.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	reg := prometheus.NewRegistry()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ledgerSvc := ledger.New(store.NewLedgerRepo(pool), reg)
	walletSvc := wallet.New(ledgerSvc, store.NewWalletRepo(pool), platform.NewClock(), wallet.Config{CoinCents: 1}, reg)

	// ── Deposit → coins: seed 10 agents, each funded (mint = the deposit credit) ──
	agents, err := store.NewIdentityRepo(pool).EnsureDevAgents(ctx, 10, walletSvc, log)
	if err != nil {
		t.Fatalf("seed agents: %v", err)
	}
	if len(agents) != 10 {
		t.Fatalf("want 10 agents, got %d", len(agents))
	}
	// KYC: give every owner a Stripe connect id so a withdrawal can be requested.
	for _, a := range agents {
		if _, err := pool.Exec(ctx, `UPDATE users SET stripe_connect_id='acct_'||public_id WHERE public_id=$1`, a.OwnerPublicID); err != nil {
			t.Fatalf("set connect: %v", err)
		}
	}
	// Every agent must have at least the entry fee to play.
	const entryFee = int64(500)
	for _, a := range agents {
		bal, _ := ledgerSvc.Balance(ctx, a.PublicID)
		if bal < entryFee {
			t.Fatalf("agent %s underfunded: %d", a.PublicID, bal)
		}
	}

	// Conservation baseline (must hold before we touch anything).
	assertNoDrift(t, ledgerSvc, log, "baseline")
	total0 := grandTotal(t, pool)

	// ── Play a pooled match among the 10 agents ──────────────────────────────────
	matchPID := fmt.Sprintf("m_e2e_%d", time.Now().UnixNano())
	winner := agents[0]
	ids := make([]string, len(agents))
	for i, a := range agents {
		ids[i] = a.PublicID
	}
	pool5k := entryFee * int64(len(agents)) // 5000
	fee := pool5k / 10                      // 10% platform fee = 500
	winnerPayout := pool5k - fee            // 4500

	seedFinishedMatch(t, pool, matchPID, winner, agents, entryFee, winnerPayout)

	// snapshot balances just before the money moves
	before := map[string]int64{}
	for _, a := range agents {
		before[a.PublicID], _ = ledgerSvc.Balance(ctx, a.PublicID)
	}
	escrow0, rev0 := sysBalance(t, pool, ledger.SysEscrow), sysBalance(t, pool, ledger.SysPlatformRevenue)

	// stake: each agent's entry fee → escrow
	if err := walletSvc.StakeMafiaTable(ctx, matchPID, ids, entryFee); err != nil {
		t.Fatalf("stake: %v", err)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow) - escrow0; got != pool5k {
		t.Fatalf("escrow after stake: +%d want +%d", got, pool5k)
	}
	for _, a := range agents {
		bal, _ := ledgerSvc.Balance(ctx, a.PublicID)
		if bal != before[a.PublicID]-entryFee {
			t.Fatalf("agent %s after stake: %d want %d", a.PublicID, bal, before[a.PublicID]-entryFee)
		}
	}

	// settle: winner takes pool − fee; platform keeps the fee (routes through the gate)
	if err := walletSvc.SettleMafiaTable(ctx, matchPID, fee, map[string]int64{winner.PublicID: winnerPayout}); err != nil {
		t.Fatalf("settle: %v", err)
	}

	// ── Assert the settlement is exactly right and nothing was swallowed ──────────
	if got := sysBalance(t, pool, ledger.SysEscrow) - escrow0; got != 0 {
		t.Fatalf("escrow not zeroed after settle: net %d (money stuck in escrow)", got)
	}
	if got := sysBalance(t, pool, ledger.SysPlatformRevenue) - rev0; got != fee {
		t.Fatalf("platform fee not captured: +%d want +%d", got, fee)
	}
	wBal, _ := ledgerSvc.Balance(ctx, winner.PublicID)
	if wBal != before[winner.PublicID]-entryFee+winnerPayout {
		t.Fatalf("winner balance: %d want %d", wBal, before[winner.PublicID]-entryFee+winnerPayout)
	}
	for _, a := range agents[1:] {
		bal, _ := ledgerSvc.Balance(ctx, a.PublicID)
		if bal != before[a.PublicID]-entryFee {
			t.Fatalf("loser %s should be down exactly the entry fee: %d want %d", a.PublicID, bal, before[a.PublicID]-entryFee)
		}
	}
	assertNoDrift(t, ledgerSvc, log, "after settle")
	if grandTotal(t, pool) != total0 {
		t.Fatalf("grand total changed across stake+settle: %d -> %d (coins created/destroyed)", total0, grandTotal(t, pool))
	}
	t.Logf("✅ pool %d → winner %d + platform fee %d; escrow zeroed; books balanced", pool5k, winnerPayout, fee)

	// ── Withdraw the winnings back to (Stripe) cash ───────────────────────────────
	payoutSvc := payout.New(
		store.NewPayoutRepo(pool), testBank{ledgerSvc}, payout.DevTransferrer{}, platform.NewClock(),
		payout.Config{CoinCents: 1, SellFeePct: 5, MinCoins: 1, Clearing: time.Nanosecond, Chain: payout.ChainStripe},
		log, reg,
	)

	avail, _, err := payoutSvc.Available(ctx, winner.OwnerPublicID, winner.PublicID, 1000)
	if err != nil {
		t.Fatalf("winner Available: %v", err)
	}
	if avail < winnerPayout-entryFee {
		t.Fatalf("winner withdrawable %d should cover this match's net winnings %d", avail, winnerPayout-entryFee)
	}

	revBeforeWd := sysBalance(t, pool, ledger.SysPlatformRevenue)
	wBefore, _ := ledgerSvc.Balance(ctx, winner.PublicID)
	const wd = int64(1000)
	w, err := payoutSvc.Request(ctx, winner.OwnerPublicID, winner.PublicID, wd)
	if err != nil {
		t.Fatalf("winner Request: %v", err)
	}
	if err := payoutSvc.Approve(ctx, "", w.PublicID); err != nil { // adminUserID="" = platform admin (bypasses maker-checker)
		t.Fatalf("winner Approve: %v", err)
	}
	got, err := store.NewPayoutRepo(pool).Get(ctx, w.PublicID)
	if err != nil || got.Status != "paid" {
		t.Fatalf("withdrawal not paid: status=%q err=%v", got.Status, err)
	}
	wAfter, _ := ledgerSvc.Balance(ctx, winner.PublicID)
	if wAfter != wBefore-wd {
		t.Fatalf("winner debited wrong: %d want %d", wAfter, wBefore-wd)
	}
	sellFee := wd * 5 / 100 // 50 (5% platform withdrawal fee)
	if got := sysBalance(t, pool, ledger.SysPlatformRevenue) - revBeforeWd; got != sellFee {
		t.Fatalf("withdrawal sell fee not captured: +%d want +%d", got, sellFee)
	}
	assertNoDrift(t, ledgerSvc, log, "after withdraw")
	t.Logf("✅ withdraw %d coins → %d cash out, %d sell fee to platform; books balanced", wd, wd-sellFee, sellFee)

	// ── BLIND SPOT: a depositor/loser cannot withdraw — deposited coins are locked ─
	loser := agents[1]
	lbal, _ := ledgerSvc.Balance(ctx, loser.PublicID)
	lavail, _, _ := payoutSvc.Available(ctx, loser.OwnerPublicID, loser.PublicID, 1)
	if lavail != 0 {
		t.Fatalf("expected loser withdrawable 0, got %d", lavail)
	}
	_, reqErr := payoutSvc.Request(ctx, loser.OwnerPublicID, loser.PublicID, 100)
	if reqErr == nil {
		t.Fatalf("loser withdrawal should have been refused (no net winnings)")
	}
	t.Logf("⚠️  FINDING: loser holds %d coins but withdrawable=0 and Request refused (%v). "+
		"Deposited coins are NOT withdrawable — 'withdraw anytime' is false for funds that were "+
		"never won in a match. Must be surfaced in UX or it reads as a money trap.", lbal, reqErr)
}

// assertNoDrift runs the ledger reconciler and fails if any wallet's balance disagrees
// with the sum of its entries (the authoritative whole-book conservation check).
func assertNoDrift(t *testing.T, l *ledger.Service, log *slog.Logger, stage string) {
	t.Helper()
	n, err := l.NewReconciler(log, 0).RunOnce(context.Background())
	if err != nil {
		t.Fatalf("reconcile (%s): %v", stage, err)
	}
	if n != 0 {
		t.Fatalf("LEDGER DRIFT at %s: %d wallet(s) out of balance (money swallowed/created)", stage, n)
	}
}

// seedFinishedMatch inserts the matches + match_players rows a real settlement would
// have written, so Settlement() (bid/agents) and Withdrawable() (coins_delta) resolve.
func seedFinishedMatch(t *testing.T, pool *pgxpool.Pool, matchPID string, winner demo.Agent, agents []demo.Agent, entryFee, winnerPayout int64) {
	t.Helper()
	ctx := context.Background()
	var winnerAgentID, creatorUserID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM agents WHERE public_id=$1`, winner.PublicID).Scan(&winnerAgentID); err != nil {
		t.Fatalf("winner agent id: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT owner_user_id FROM agents WHERE public_id=$1`, winner.PublicID).Scan(&creatorUserID); err != nil {
		t.Fatalf("creator id: %v", err)
	}
	var matchID int64
	err := pool.QueryRow(ctx,
		`INSERT INTO matches (public_id, game, status, bid, rake_pct, engine_version, prize_seed_commit,
		     creator_owner_user_id, winner_agent_id, finished_at)
		 VALUES ($1,'mafia','finished',$2,10,'test','x',$3,$4, now()) RETURNING id`,
		matchPID, entryFee, creatorUserID, winnerAgentID).Scan(&matchID)
	if err != nil {
		t.Fatalf("insert match: %v", err)
	}
	for seat, a := range agents {
		var agentID, ownerID int64
		if err := pool.QueryRow(ctx, `SELECT id, owner_user_id FROM agents WHERE public_id=$1`, a.PublicID).Scan(&agentID, &ownerID); err != nil {
			t.Fatalf("agent id %s: %v", a.PublicID, err)
		}
		delta := -entryFee // losers: down the entry fee
		if a.PublicID == winner.PublicID {
			delta = winnerPayout - entryFee // winner: pool share minus own stake
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO match_players (match_id, agent_id, owner_user_id, seat, final_score, coins_delta)
			 VALUES ($1,$2,$3,$4,$5,$6)`, matchID, agentID, ownerID, seat, 0, delta); err != nil {
			t.Fatalf("insert match_player: %v", err)
		}
	}
}

// denyGate refuses every settlement — simulating a fraud flag / admin hold.
type denyGate struct{}

func (denyGate) Allow(context.Context, string) (bool, error) { return false, nil }

// TestMoneyFlowE2E_MonopolyRespectsFraudGate verifies the fix: under a deny-all gate,
// BOTH Mafia and Monopoly now HOLD escrow (winner unpaid, pending review) instead of
// auto-paying, and a cleared hold releases the exact split via SettleHeld. This closes
// the earlier gap where staked Monopoly settled straight through a flag.
func TestMoneyFlowE2E_MonopolyRespectsFraudGate(t *testing.T) {
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL")
	}
	if err := store.Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	reg := prometheus.NewRegistry()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ledgerSvc := ledger.New(store.NewLedgerRepo(pool), reg)
	walletSvc := wallet.New(ledgerSvc, store.NewWalletRepo(pool), platform.NewClock(), wallet.Config{CoinCents: 1}, reg)
	walletSvc.SetPayoutGate(denyGate{}) // every match is "flagged"

	agents, err := store.NewIdentityRepo(pool).EnsureDevAgents(ctx, 10, walletSvc, log)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	trio := []string{agents[0].PublicID, agents[1].PublicID, agents[2].PublicID}
	const fee = int64(300)
	gross := fee * int64(len(trio)) // 900
	winner := trio[0]

	// ── Mafia under a DENY gate: escrow must be RETAINED (held for review) ──
	mafiaID := fmt.Sprintf("m_mafia_hold_%d", time.Now().UnixNano())
	seedFinishedMatch(t, pool, mafiaID, agents[0], agents[:3], fee, gross-90)
	esc0 := sysBalance(t, pool, ledger.SysEscrow)
	if err := walletSvc.StakeMafiaTable(ctx, mafiaID, trio, fee); err != nil {
		t.Fatalf("mafia stake: %v", err)
	}
	wBefore, _ := ledgerSvc.Balance(ctx, winner)
	if err := walletSvc.SettleMafiaTable(ctx, mafiaID, 90, map[string]int64{winner: gross - 90}); err != nil {
		t.Fatalf("mafia settle: %v", err)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow) - esc0; got != gross {
		t.Fatalf("MAFIA should HOLD escrow under a deny gate: escrow delta %d want %d", got, gross)
	}
	if wAfter, _ := ledgerSvc.Balance(ctx, winner); wAfter != wBefore {
		t.Fatalf("MAFIA winner should NOT be paid while held: %d -> %d", wBefore, wAfter)
	}
	t.Logf("✅ Mafia respects the gate: escrow retained, winner unpaid (held for review)")

	// ── Monopoly under the SAME DENY gate: now HOLDS (gap fixed) ──
	monoID := fmt.Sprintf("m_mono_hold_%d", time.Now().UnixNano())
	esc1 := sysBalance(t, pool, ledger.SysEscrow)
	if err := walletSvc.StakeMonopolyTable(ctx, monoID, trio, fee); err != nil {
		t.Fatalf("mono stake: %v", err)
	}
	wBefore2, _ := ledgerSvc.Balance(ctx, winner)
	monoPayouts := map[string]int64{winner: gross - 90}
	if err := walletSvc.SettleMonopolyTable(ctx, monoID, gross, 90, monoPayouts); err != nil {
		t.Fatalf("mono settle: %v", err)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow) - esc1; got != gross {
		t.Fatalf("MONOPOLY should now HOLD escrow under a deny gate: escrow delta %d want %d", got, gross)
	}
	if wAfter2, _ := ledgerSvc.Balance(ctx, winner); wAfter2 != wBefore2 {
		t.Fatalf("MONOPOLY winner must NOT be paid while held: %d -> %d", wBefore2, wAfter2)
	}
	t.Logf("✅ FIXED: Monopoly now respects the fraud gate — escrow retained, winner unpaid (held for review)")

	// ── Admin release (gate cleared): SettleHeld pays the held Monopoly split exactly ──
	if err := walletSvc.SettleHeld(ctx, monoID); err != nil {
		t.Fatalf("mono SettleHeld: %v", err)
	}
	if got := sysBalance(t, pool, ledger.SysEscrow) - esc1; got != 0 {
		t.Fatalf("escrow not drained after held release: net %d", got)
	}
	if wAfter, _ := ledgerSvc.Balance(ctx, winner); wAfter != wBefore2+(gross-90) {
		t.Fatalf("held-release payout wrong: %d want %d", wAfter, wBefore2+(gross-90))
	}
	assertNoDrift(t, ledgerSvc, log, "monopoly held release")
	t.Logf("✅ held Monopoly released cleanly via SettleHeld (winner +%d, books balanced)", gross-90)
}
