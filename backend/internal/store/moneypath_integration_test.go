package store

import (
	"context"
	"os"
	"testing"

	"github.com/agent-arena/arena/internal/ledger"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// The money path, end to end, against a REAL Postgres and through the REAL ledger
// service — not a stub. Users stake their own coins here, so the properties below
// are the ones that decide whether the platform can be trusted with them:
//
//	1. coins are never created out of nothing (every transaction sums to zero);
//	2. an agent cannot be taken below zero, so a user cannot lose more than they hold;
//	3. a disbursement cannot happen twice, so a replay cannot mint a second payout;
//	4. escrow that belongs to no match row is REPORTED rather than silently held.
//
// (4) is the regression guard for the defect this file was written alongside: the
// audit reconciled escrow by INNER JOINing stakes to `matches`, so a stake whose
// match row was absent was counted in none of held / open / unexplained. The audit
// logged "ledger audit clean" while the wallet held 1,521,400 coins against 7,900
// explained. Behavioural on purpose — a structural test ("a check exists") would
// have passed throughout the period the money was invisible.

func moneyPathPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("PYYOL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PYYOL_TEST_DATABASE_URL to a migrated Postgres to run this")
	}
	// Migrate rather than assume: these in-package integration tests share one
	// database and Go runs them in filename order, so this file can run before the
	// ones that migrate. Migrate is idempotent.
	if err := Migrate(dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	// Registered FIRST so LIFO ordering closes the pool LAST, after the data
	// cleanups have run through it.
	t.Cleanup(pool.Close)
	return pool, ctx
}

// seedFundedAgent returns an agent public id whose wallet holds exactly `coins`,
// funded the way a deposit funds it: a balanced transaction against the clearing
// account, never a direct UPDATE. A test that hand-wrote a balance would be
// asserting conservation against a ledger it had already broken.
func seedFundedAgent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, l *ledger.Service, tag string, coins int64) string {
	t.Helper()
	// SEEDS ITS OWN agent rather than borrowing one.
	//
	// Borrowing an existing agent would mean running against a database somebody
	// cares about, and these fixtures cannot be unwound: wallet balances are STORED
	// columns, so deleting the ledger rows afterwards would leave the balance
	// inflated and the ledger inconsistent — i.e. the cleanup would manufacture
	// exactly the drift these tests exist to detect. Point this at a throwaway
	// database (PYYOL_TEST_DATABASE_URL) and it builds what it needs.
	agentPub := "ag_itest_" + tag
	if _, err := pool.Exec(ctx,
		`INSERT INTO users (public_id) VALUES ($1) ON CONFLICT (public_id) DO NOTHING`,
		"usr_itest_"+tag); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO agents (public_id, owner_user_id, name, slug)
		 SELECT $1, u.id, $2, $3 FROM users u WHERE u.public_id = $4
		 ON CONFLICT (public_id) DO NOTHING`,
		agentPub, "itest "+tag, "itest-"+tag, "usr_itest_"+tag); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	// The agent's wallet is created explicitly, exactly as identity_repo does when an
	// agent is registered — it is not implied by the agent row, and the ledger
	// correctly refuses to post to a wallet that does not exist (wallet_not_found).
	if _, err := pool.Exec(ctx,
		`INSERT INTO wallets (agent_id, kind, balance)
		 SELECT a.id, 'agent', 0 FROM agents a WHERE a.public_id = $1
		 ON CONFLICT DO NOTHING`, agentPub); err != nil {
		t.Fatalf("seed agent wallet: %v", err)
	}
	if coins > 0 {
		if _, err := l.Post(ctx, ledger.Txn{
			Kind:     "deposit",
			Key:      "itest-fund:" + tag,
			Metadata: map[string]any{"itest": tag},
			Postings: []ledger.Posting{
				{Wallet: ledger.SystemWallet(ledger.SysStripeClearing), Amount: -coins},
				{Wallet: ledger.AgentWallet(agentPub), Amount: coins},
			},
		}); err != nil {
			t.Fatalf("fund agent: %v", err)
		}
	}
	return agentPub
}

func walletBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, agentPub string) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(ctx,
		`SELECT w.balance FROM wallets w JOIN agents a ON a.id = w.agent_id
		  WHERE a.public_id = $1 AND w.kind = 'agent'`, agentPub).Scan(&bal); err != nil {
		t.Fatalf("read agent balance: %v", err)
	}
	return bal
}

func systemBalance(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string) int64 {
	t.Helper()
	var bal int64
	if err := pool.QueryRow(ctx,
		`SELECT coalesce(sum(balance),0) FROM wallets WHERE kind = $1`, kind).Scan(&bal); err != nil {
		t.Fatalf("read %s balance: %v", kind, err)
	}
	return bal
}

func dropItestTxns(ctx context.Context, pool *pgxpool.Pool, tag string) {
	_, _ = pool.Exec(ctx, `DELETE FROM ledger_entries WHERE txn_id IN
		(SELECT id FROM ledger_transactions WHERE metadata->>'itest' = $1)`, tag)
	_, _ = pool.Exec(ctx, `DELETE FROM ledger_transactions WHERE metadata->>'itest' = $1`, tag)
}

// A full stake → settle cycle must conserve coins exactly: the winner's gain plus
// the platform's rake equals the losers' loss, to the coin. Nothing is minted.
func TestMoneyPath_StakeAndSettleConservesCoins(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	l := ledger.New(NewLedgerRepo(pool), prometheus.NewRegistry())
	const tag = "moneypath_conserve"

	// The ledger is append-only, so the fixture is unwound by deleting its own rows
	// and letting the balances be recomputed by the same postings, inverted.
	t.Cleanup(func() { dropItestTxns(ctx, pool, tag) })
	dropItestTxns(ctx, pool, tag)

	const bid = 100
	agent := seedFundedAgent(t, ctx, pool, l, tag, 2*bid)

	before := walletBalance(t, ctx, pool, agent)
	escrowBefore := systemBalance(t, ctx, pool, "escrow")
	revenueBefore := systemBalance(t, ctx, pool, ledger.SysPlatformRevenue)

	// Stake: the agent's coins move OUT of its wallet into escrow. Deliberately the
	// same seat twice, standing in for both sides of a two-seat pot, so the
	// arithmetic is checkable on one balance.
	if _, err := l.Post(ctx, ledger.Txn{
		Kind: "stake", Key: "itest-stake:" + tag,
		Metadata: map[string]any{"itest": tag},
		Postings: []ledger.Posting{
			{Wallet: ledger.AgentWallet(agent), Amount: -2 * bid},
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: 2 * bid},
		},
	}); err != nil {
		t.Fatalf("stake: %v", err)
	}
	if got := walletBalance(t, ctx, pool, agent); got != before-2*bid {
		t.Fatalf("after staking, agent balance = %d, want %d", got, before-2*bid)
	}
	if got := systemBalance(t, ctx, pool, "escrow"); got != escrowBefore+2*bid {
		t.Fatalf("escrow = %d, want %d — the stake did not land in escrow", got, escrowBefore+2*bid)
	}

	// Settle at a 10% rake, exactly as wallet.settle posts it.
	const pool2 = 2 * bid
	const rake = pool2 * 10 / 100
	if _, err := l.Post(ctx, ledger.Txn{
		Kind: "settle", Key: "itest-disburse:" + tag,
		Metadata: map[string]any{"itest": tag},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -pool2},
			{Wallet: ledger.AgentWallet(agent), Amount: pool2 - rake},
			{Wallet: ledger.SystemWallet(ledger.SysPlatformRevenue), Amount: rake},
		},
	}); err != nil {
		t.Fatalf("settle: %v", err)
	}

	// Conservation: what the winner did not get, the platform got — and escrow is
	// back where it started. No coins appeared and none vanished.
	if got, want := walletBalance(t, ctx, pool, agent), before-rake; got != want {
		t.Fatalf("after settling, agent balance = %d, want %d (staked %d, won back %d, rake %d)",
			got, want, pool2, pool2-rake, rake)
	}
	if got := systemBalance(t, ctx, pool, "escrow"); got != escrowBefore {
		t.Fatalf("escrow = %d, want %d — escrow did not return to its pre-match level", got, escrowBefore)
	}
	if got, want := systemBalance(t, ctx, pool, ledger.SysPlatformRevenue), revenueBefore+rake; got != want {
		t.Fatalf("platform revenue = %d, want %d", got, want)
	}
}

// A user cannot lose coins they never had. The schema constraint is the last line
// of defence behind every spend path, so it is asserted directly: a posting that
// would overdraw an agent must be REJECTED, and must leave the balance untouched.
func TestMoneyPath_CannotOverdrawAnAgent(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	l := ledger.New(NewLedgerRepo(pool), prometheus.NewRegistry())
	const tag = "moneypath_overdraw"
	t.Cleanup(func() { dropItestTxns(ctx, pool, tag) })
	dropItestTxns(ctx, pool, tag)

	agent := seedFundedAgent(t, ctx, pool, l, tag, 0)
	before := walletBalance(t, ctx, pool, agent)

	// Ask for one coin more than the wallet holds.
	_, err := l.Post(ctx, ledger.Txn{
		Kind: "stake", Key: "itest-overdraw:" + tag,
		Metadata: map[string]any{"itest": tag},
		Postings: []ledger.Posting{
			{Wallet: ledger.AgentWallet(agent), Amount: -(before + 1)},
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: before + 1},
		},
	})
	if err == nil {
		t.Fatalf("overdrawing an agent by 1 coin SUCCEEDED (balance was %d) — a user can be "+
			"taken below zero, which means losing coins they never had", before)
	}
	if got := walletBalance(t, ctx, pool, agent); got != before {
		t.Fatalf("a rejected overdraw still moved money: balance %d, was %d", got, before)
	}
}

// An unbalanced transaction is the shape that mints coins from nothing. The ledger
// must refuse it before it reaches the database.
func TestMoneyPath_RejectsUnbalancedTransactions(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	l := ledger.New(NewLedgerRepo(pool), prometheus.NewRegistry())
	const tag = "moneypath_unbalanced"
	t.Cleanup(func() { dropItestTxns(ctx, pool, tag) })
	dropItestTxns(ctx, pool, tag)

	agent := seedFundedAgent(t, ctx, pool, l, tag, 0)
	before := walletBalance(t, ctx, pool, agent)

	// Credit an agent with nothing debited anywhere: free money.
	if _, err := l.Post(ctx, ledger.Txn{
		Kind: "settle", Key: "itest-mint:" + tag,
		Metadata: map[string]any{"itest": tag},
		Postings: []ledger.Posting{{Wallet: ledger.AgentWallet(agent), Amount: 5_000}},
	}); err == nil {
		t.Fatal("a one-sided posting was accepted — coins can be created from nothing")
	}
	if got := walletBalance(t, ctx, pool, agent); got != before {
		t.Fatalf("balance moved on a rejected mint: %d, was %d", got, before)
	}
}

// Replaying a disbursement must not pay twice. Settle and refund deliberately share
// one per-match key, so this also pins that a refund cannot follow a settle and
// drain the shared escrow a second time.
func TestMoneyPath_DisbursementIsIdempotent(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	l := ledger.New(NewLedgerRepo(pool), prometheus.NewRegistry())
	const tag = "moneypath_idem"
	t.Cleanup(func() { dropItestTxns(ctx, pool, tag) })
	dropItestTxns(ctx, pool, tag)

	const amount = 250
	agent := seedFundedAgent(t, ctx, pool, l, tag, amount)

	// Park the coins in escrow so the payout has somewhere to come from.
	if _, err := l.Post(ctx, ledger.Txn{
		Kind: "stake", Key: "itest-idem-stake:" + tag,
		Metadata: map[string]any{"itest": tag},
		Postings: []ledger.Posting{
			{Wallet: ledger.AgentWallet(agent), Amount: -amount},
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: amount},
		},
	}); err != nil {
		t.Fatalf("stake: %v", err)
	}
	before := walletBalance(t, ctx, pool, agent)

	payout := ledger.Txn{
		Kind: "settle", Key: "itest-idem-disburse:" + tag,
		Metadata: map[string]any{"itest": tag},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -amount},
			{Wallet: ledger.AgentWallet(agent), Amount: amount},
		},
	}
	first, err := l.Post(ctx, payout)
	if err != nil {
		t.Fatalf("first payout: %v", err)
	}
	if !first.Applied {
		t.Fatal("the first payout reported Applied=false")
	}
	second, err := l.Post(ctx, payout)
	if err != nil {
		t.Fatalf("replayed payout errored instead of being a no-op: %v", err)
	}
	if second.Applied {
		t.Fatal("a replayed disbursement was APPLIED — the same pot can be paid out twice")
	}
	if got, want := walletBalance(t, ctx, pool, agent), before+amount; got != want {
		t.Fatalf("agent balance = %d, want %d — the replay moved money a second time", got, want)
	}
}

// THE REGRESSION GUARD for escrow_unattributed.
//
// A stake whose match row does not exist is invisible to the match-driven
// reconciliation (it INNER JOINs to `matches`), so before this check the coins sat
// in escrow and the audit called itself clean. Asserted as a DELTA, because the
// audit reconciles the whole database and unrelated residue must not decide the
// result.
func TestMoneyPath_EscrowWithNoMatchRowIsReported(t *testing.T) {
	pool, ctx := moneyPathPool(t)
	repo := NewLedgerRepo(pool)
	l := ledger.New(repo, prometheus.NewRegistry())
	const tag = "moneypath_orphan"
	const orphanMatch = "m_moneypath_orphan_never_persisted"
	const stake = 400

	cleanup := func() {
		dropItestTxns(ctx, pool, tag)
		_, _ = pool.Exec(ctx, `DELETE FROM matches WHERE public_id = $1`, orphanMatch)
	}
	cleanup()
	t.Cleanup(cleanup)

	residual := func() int64 {
		t.Helper()
		rep, err := repo.AuditLedger(ctx)
		if err != nil {
			t.Fatalf("AuditLedger: %v", err)
		}
		return findingCount(rep.Findings, "escrow_unattributed")
	}
	baseline := residual()

	agent := seedFundedAgent(t, ctx, pool, l, tag, stake)

	// Escrow the stake against a match id that has NO row — the exact residue an
	// activation that failed after taking the money leaves behind.
	if _, err := l.Post(ctx, ledger.Txn{
		Kind: "stake", Key: "itest-orphan-stake:" + tag,
		Metadata: map[string]any{"itest": tag, "match": orphanMatch},
		Postings: []ledger.Posting{
			{Wallet: ledger.AgentWallet(agent), Amount: -stake},
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: stake},
		},
	}); err != nil {
		t.Fatalf("orphan stake: %v", err)
	}

	if got := residual(); got != baseline+stake {
		t.Fatalf("escrow_unattributed = %d, want %d (baseline %d + %d orphaned): escrow that "+
			"belongs to no match row is not being reported, so coins can be held with nothing "+
			"able to release them and the audit would still say clean", got, baseline+stake, baseline, stake)
	}

	// And it must CLEAR once the coins are returned — a check that only ever fires
	// is one an operator learns to ignore.
	if _, err := l.Post(ctx, ledger.Txn{
		Kind: "refund", Key: "itest-orphan-refund:" + tag,
		Metadata: map[string]any{"itest": tag, "match": orphanMatch},
		Postings: []ledger.Posting{
			{Wallet: ledger.SystemWallet(ledger.SysEscrow), Amount: -stake},
			{Wallet: ledger.AgentWallet(agent), Amount: stake},
		},
	}); err != nil {
		t.Fatalf("orphan refund: %v", err)
	}
	if got := residual(); got != baseline {
		t.Fatalf("escrow_unattributed = %d after refunding, want the %d baseline — the check "+
			"does not clear when the money is returned", got, baseline)
	}
}
