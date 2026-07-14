package wallet_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/ledger"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/wallet"
	"github.com/prometheus/client_golang/prometheus"
)

// ── fakes ────────────────────────────────────────────────────────────────────

type fakeLedger struct {
	posts   []ledger.Txn
	balance int64
}

func (f *fakeLedger) Post(_ context.Context, t ledger.Txn) (ledger.ApplyResult, error) {
	f.posts = append(f.posts, t)
	return ledger.ApplyResult{PublicID: "txn_x", Applied: true}, nil
}
func (f *fakeLedger) Balance(context.Context, string) (int64, error)     { return f.balance, nil }
func (f *fakeLedger) UserBalance(context.Context, string) (int64, error) { return f.balance, nil }
func (f *fakeLedger) History(context.Context, string, int) ([]ledger.Line, error) {
	return nil, nil
}
func (f *fakeLedger) UserHistory(context.Context, string, int) ([]ledger.Line, error) {
	return nil, nil
}

type fakeRepo struct {
	settlement wallet.Settlement
	limits     wallet.AgentLimits
	lossSince  int64
	lossCount  int
	active     int
	owner      string
	debt       int64
	repaid     int64
	held       map[string]heldSettlement // persisted multi-winner splits
}

type heldSettlement struct {
	fee     int64
	payouts map[string]int64
}

func (f *fakeRepo) Settlement(context.Context, string) (wallet.Settlement, error) {
	return f.settlement, nil
}

func (f *fakeRepo) SaveHeldSettlement(_ context.Context, matchPublicID string, platformFee int64, payouts map[string]int64) error {
	if f.held == nil {
		f.held = map[string]heldSettlement{}
	}
	f.held[matchPublicID] = heldSettlement{fee: platformFee, payouts: payouts}
	return nil
}

func (f *fakeRepo) HeldSettlement(_ context.Context, matchPublicID string) (int64, map[string]int64, bool, error) {
	h, ok := f.held[matchPublicID]
	if !ok {
		return 0, nil, false, nil
	}
	return h.fee, h.payouts, true, nil
}
func (f *fakeRepo) AgentLimits(context.Context, string) (wallet.AgentLimits, error) {
	return f.limits, nil
}
func (f *fakeRepo) LossSince(context.Context, string, time.Time) (int64, error) {
	return f.lossSince, nil
}
func (f *fakeRepo) LossCountSince(context.Context, string, time.Time) (int, error) {
	return f.lossCount, nil
}
func (f *fakeRepo) ActiveMatchCount(context.Context, string) (int, error)  { return f.active, nil }
func (f *fakeRepo) OwnerOf(context.Context, string) (string, error)        { return f.owner, nil }
func (f *fakeRepo) OutstandingDebt(context.Context, string) (int64, error) { return f.debt, nil }
func (f *fakeRepo) RecordDebt(_ context.Context, _ string, coins int64) error {
	f.debt += coins
	return nil
}
func (f *fakeRepo) RepayDebt(_ context.Context, _ string, coins int64) error {
	f.repaid += coins
	if f.debt -= coins; f.debt < 0 {
		f.debt = 0
	}
	return nil
}
func (f *fakeRepo) UserLifetimeStats(context.Context, string) (wallet.LifetimeStats, error) {
	return wallet.LifetimeStats{}, nil
}
func (f *fakeRepo) OwnerAgents(context.Context, string) ([]wallet.AgentRow, error) { return nil, nil }
func (f *fakeRepo) StakedInActiveMatches(context.Context, string) (int64, error)   { return 0, nil }
func (f *fakeRepo) PendingWithdrawalCoins(context.Context, string) (int64, error)  { return 0, nil }
func (f *fakeRepo) WithdrawableCoins(context.Context, string) (int64, error)       { return 0, nil }

func newSvc(l *fakeLedger, r *fakeRepo) *wallet.Service {
	return wallet.New(l, r, platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()},
		wallet.Config{SessionWindow: 6 * time.Hour}, prometheus.NewRegistry())
}

func assertBalanced(t *testing.T, txn ledger.Txn) {
	t.Helper()
	var sum int64
	for _, p := range txn.Postings {
		sum += p.Amount
	}
	if sum != 0 {
		t.Fatalf("txn %q postings sum to %d, want 0: %+v", txn.Key, sum, txn.Postings)
	}
}

func amountFor(txn ledger.Txn, ref ledger.WalletRef) int64 {
	var total int64
	for _, p := range txn.Postings {
		if p.Wallet == ref {
			total += p.Amount
		}
	}
	return total
}

// ── money ────────────────────────────────────────────────────────────────────

func TestStakeMatchEscrowsBothSeatsAtomically(t *testing.T) {
	fl := &fakeLedger{}
	svc := newSvc(fl, &fakeRepo{})
	if err := svc.StakeMatch(context.Background(), "m_1", "ag_a", "ag_b", 50); err != nil {
		t.Fatalf("StakeMatch: %v", err)
	}
	if len(fl.posts) != 1 {
		t.Fatalf("want exactly 1 atomic stake txn, got %d", len(fl.posts))
	}
	txn := fl.posts[0]
	assertBalanced(t, txn)
	if txn.Key != "stake:m_1" || txn.Kind != ledger.KindStake {
		t.Fatalf("bad stake txn meta: %+v", txn)
	}
	if amountFor(txn, ledger.AgentWallet("ag_a")) != -50 || amountFor(txn, ledger.AgentWallet("ag_b")) != -50 {
		t.Fatalf("both seats must be debited 50: %+v", txn.Postings)
	}
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysEscrow)); got != 100 {
		t.Fatalf("escrow credit = %d, want 100", got)
	}
}

func TestSettleWinnerTakesPoolMinusRake(t *testing.T) {
	fl := &fakeLedger{}
	svc := newSvc(fl, &fakeRepo{})
	if err := svc.Settle(context.Background(), "m_1", "ag_w", 100, 5); err != nil {
		t.Fatalf("Settle: %v", err)
	}
	txn := fl.posts[0]
	assertBalanced(t, txn)
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysEscrow)); got != -100 {
		t.Fatalf("escrow debit = %d, want -100", got)
	}
	if got := amountFor(txn, ledger.AgentWallet("ag_w")); got != 95 {
		t.Fatalf("winner credit = %d, want 95", got)
	}
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysPlatformRevenue)); got != 5 {
		t.Fatalf("rake credit = %d, want 5", got)
	}
}

func TestSettleTieRefundsBothStakes(t *testing.T) {
	fl := &fakeLedger{}
	svc := newSvc(fl, &fakeRepo{settlement: wallet.Settlement{Bid: 50, Agents: []string{"ag_a", "ag_b"}}})
	if err := svc.Settle(context.Background(), "m_1", "", 100, 5); err != nil {
		t.Fatalf("Settle tie: %v", err)
	}
	txn := fl.posts[0]
	assertBalanced(t, txn)
	if got := amountFor(txn, ledger.AgentWallet("ag_a")); got != 50 {
		t.Fatalf("ag_a refund = %d, want 50", got)
	}
	if got := amountFor(txn, ledger.AgentWallet("ag_b")); got != 50 {
		t.Fatalf("ag_b refund = %d, want 50", got)
	}
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysPlatformRevenue)); got != 0 {
		t.Fatalf("ties must take no rake, got %d", got)
	}
}

// denyGate simulates an anti-fraud hold: every payout is withheld.
type denyGate struct{}

func (denyGate) Allow(context.Context, string) (bool, error) { return false, nil }

// G1: a HELD Mafia table must, on admin release, replay the persisted multi-winner
// split — not pay the whole pot winner-take-all through the 2-player path.
func TestSettleHeldMafiaReplaysSplit(t *testing.T) {
	fl := &fakeLedger{}
	// 3 seats × 100 bid = 300 pot; recorded single winner ag_a — exactly what a
	// winner-take-all release would wrongly pay in full.
	repo := &fakeRepo{settlement: wallet.Settlement{Bid: 100, Agents: []string{"ag_a", "ag_b", "ag_c"}, Winner: "ag_a", RakePct: 10}}
	svc := newSvc(fl, repo)
	svc.SetPayoutGate(denyGate{})

	// Surviving winners ag_a and ag_b split 270 (135 each); 30 platform fee.
	if err := svc.SettleMafiaTable(context.Background(), "m_mafia", 30, map[string]int64{"ag_a": 135, "ag_b": 135}); err != nil {
		t.Fatalf("held settle: %v", err)
	}
	if len(fl.posts) != 0 {
		t.Fatalf("a held settlement must not post to the ledger yet; got %d", len(fl.posts))
	}

	if err := svc.SettleHeld(context.Background(), "m_mafia"); err != nil {
		t.Fatalf("SettleHeld: %v", err)
	}
	if len(fl.posts) != 1 {
		t.Fatalf("want exactly 1 settle txn on release, got %d", len(fl.posts))
	}
	txn := fl.posts[0]
	assertBalanced(t, txn)
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysEscrow)); got != -300 {
		t.Fatalf("escrow debit = %d, want -300 (full pot)", got)
	}
	// The split: both winners paid their share, the loser nothing. Winner-take-all
	// would have paid ag_a 270 and ag_b 0 — the bug this guards against.
	if got := amountFor(txn, ledger.AgentWallet("ag_a")); got != 135 {
		t.Fatalf("ag_a credit = %d, want 135 (its share, not the whole pot)", got)
	}
	if got := amountFor(txn, ledger.AgentWallet("ag_b")); got != 135 {
		t.Fatalf("ag_b credit = %d, want 135 (must be paid its share)", got)
	}
	if got := amountFor(txn, ledger.AgentWallet("ag_c")); got != 0 {
		t.Fatalf("ag_c (loser) credit = %d, want 0", got)
	}
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysPlatformRevenue)); got != 30 {
		t.Fatalf("platform fee = %d, want 30", got)
	}
}

func TestRefundReturnsEveryStake(t *testing.T) {
	fl := &fakeLedger{}
	svc := newSvc(fl, &fakeRepo{settlement: wallet.Settlement{Bid: 50, Agents: []string{"ag_a", "ag_b"}}})
	if err := svc.Refund(context.Background(), "m_1"); err != nil {
		t.Fatalf("Refund: %v", err)
	}
	txn := fl.posts[0]
	assertBalanced(t, txn)
	// Refund shares the per-match disbursement key with settle (H2): escrow can be
	// paid out at most once per match, so a settle and a refund can never both apply.
	if txn.Key != "disburse:m_1" || txn.Kind != ledger.KindRefund {
		t.Fatalf("bad refund meta: %+v", txn)
	}
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysEscrow)); got != -100 {
		t.Fatalf("escrow debit = %d, want -100", got)
	}
}

// ── chargeback debt ──────────────────────────────────────────────────────────

func TestReversePartialClawbackBooksDebt(t *testing.T) {
	fl := &fakeLedger{balance: 30}
	repo := &fakeRepo{}
	if err := newSvc(fl, repo).Reverse(context.Background(), "usr_a", "ag_a", 100, "reversal:pi_1"); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	txn := fl.posts[0]
	assertBalanced(t, txn)
	if got := amountFor(txn, ledger.UserWallet("usr_a")); got != -30 {
		t.Fatalf("clawback = %d, want -30 (all the user had)", got)
	}
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysBadDebt)); got != -70 {
		t.Fatalf("bad debt = %d, want -70", got)
	}
	// The shortfall is also recorded as per-agent debt so the payout gate fires.
	if repo.debt != 70 {
		t.Fatalf("recorded agent debt = %d, want 70", repo.debt)
	}
}

func TestCreditRepaysDebtBeforeWallet(t *testing.T) {
	fl := &fakeLedger{}
	repo := &fakeRepo{debt: 70}
	if err := newSvc(fl, repo).Mint(context.Background(), "ag_a", 100, "mint:1"); err != nil {
		t.Fatalf("Mint: %v", err)
	}
	txn := fl.posts[0]
	assertBalanced(t, txn)
	if got := amountFor(txn, ledger.SystemWallet(ledger.SysBadDebt)); got != 70 {
		t.Fatalf("debt repaid into bad_debt = %d, want 70", got)
	}
	if got := amountFor(txn, ledger.AgentWallet("ag_a")); got != 30 {
		t.Fatalf("wallet credit = %d, want 30 (remainder after debt)", got)
	}
	if repo.repaid != 70 || repo.debt != 0 {
		t.Fatalf("repaid=%d debt=%d, want 70 and 0", repo.repaid, repo.debt)
	}
}

func TestTopupCreditsUserTreasury(t *testing.T) {
	fl := &fakeLedger{}
	if err := newSvc(fl, &fakeRepo{}).Topup(context.Background(), "usr_a", 2400, "topup:sess_1"); err != nil {
		t.Fatalf("Topup: %v", err)
	}
	txn := fl.posts[0]
	if got := amountFor(txn, ledger.UserWallet("usr_a")); got != 2400 {
		t.Fatalf("user credit = %d, want 2400", got)
	}
}

// ── limits ───────────────────────────────────────────────────────────────────

func baseLimits() wallet.AgentLimits {
	return wallet.AgentLimits{
		CoinLimitPerMatch: 100, DailyLossLimit: 500, SessionLossLimit: 1000,
		MinWalletBalance: 50, MaxConcurrentMatches: 1, CooldownLosses: 3,
		CooldownSeconds: 300, MaxBid: 200,
	}
}

func codeOf(err error) string {
	var ae *httpx.APIError
	if errors.As(err, &ae) {
		return ae.Code
	}
	return ""
}

func TestCheckJoinLimits(t *testing.T) {
	cases := []struct {
		name     string
		bid      int64
		balance  int64
		tweak    func(*fakeRepo)
		wantCode string // "" means the join is allowed
	}{
		{name: "allowed", bid: 50, balance: 1000, wantCode: ""},
		{name: "invalid bid", bid: 0, balance: 1000, wantCode: "invalid_bid"},
		{name: "min wallet balance", bid: 50, balance: 80, wantCode: "insufficient_balance"}, // need 100
		{
			name: "per match limit", bid: 150, balance: 1000, wantCode: "limit_coin_limit_per_match",
		},
		{
			name: "daily loss", bid: 50, balance: 1000, wantCode: "limit_daily_loss_limit",
			tweak: func(r *fakeRepo) { r.lossSince = 500 },
		},
		{
			name: "session loss", bid: 50, balance: 1000, wantCode: "limit_session_loss_limit",
			tweak: func(r *fakeRepo) { r.lossSince = 600; r.limits.DailyLossLimit = 1000; r.limits.SessionLossLimit = 500 },
		},
		{
			name: "cooldown", bid: 50, balance: 1000, wantCode: "limit_cooldown",
			tweak: func(r *fakeRepo) { r.lossCount = 3 },
		},
		{
			name: "concurrent", bid: 50, balance: 1000, wantCode: "limit_max_concurrent_matches",
			tweak: func(r *fakeRepo) { r.active = 1 },
		},
		{
			name: "max bid", bid: 250, balance: 1000, wantCode: "limit_max_bid",
			tweak: func(r *fakeRepo) { r.limits.CoinLimitPerMatch = 300 }, // pass per-match so max_bid is reached
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{limits: baseLimits()}
			if tc.tweak != nil {
				tc.tweak(repo)
			}
			fl := &fakeLedger{balance: tc.balance}
			err := newSvc(fl, repo).CheckJoin(context.Background(), "ag_a", tc.bid)
			if got := codeOf(err); got != tc.wantCode {
				t.Fatalf("code = %q, want %q (err=%v)", got, tc.wantCode, err)
			}
		})
	}
}
