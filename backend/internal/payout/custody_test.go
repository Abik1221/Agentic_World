package payout

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	solana "github.com/gagliardetto/solana-go"
	"github.com/prometheus/client_golang/prometheus"
)

// perAccountBalances answers per token account, so a split-custody deployment can be
// tested with a small float and a full vault — the whole point of the split.
type perAccountBalances struct {
	byAccount map[string]int64
	// errFor fails the lookup for one specific account, so the vault can be made
	// unreadable while the hot balance still reads fine — the realistic partial failure.
	errFor string
	calls  []string
}

func (p *perAccountBalances) TokenAccountBalance(_ context.Context, acct string) (int64, error) {
	p.calls = append(p.calls, acct)
	if p.errFor != "" && p.errFor == acct {
		return 0, errors.New("solana rpc: connection reset")
	}
	return p.byAccount[acct], nil
}

type stubFees struct {
	lamports int64
	err      error
}

func (s stubFees) LamportBalance(context.Context, string) (int64, error) {
	return s.lamports, s.err
}

const usdcPerDollar = 1_000_000 // 6-decimal USDC base units in $1

// Single-wallet custody must behave EXACTLY as before: custody is the hot balance and
// no second RPC call is made. This is the existing devnet deployment, and the split is
// only worth adding if it cannot disturb the setup that is already running.
func TestSingleWalletCustodyUnchanged(t *testing.T) {
	bal := &perAccountBalances{byAccount: map[string]int64{"hot": 50 * usdcPerDollar}}
	m := NewSolvencyMonitor(stubLiability{cents: 1000}, bal, "hot", testLog(), prometheus.NewRegistry())
	// Passing the same account as the vault is how main wires it when custody is not
	// split; it must collapse to single-wallet mode rather than double-counting.
	m.SetVault("hot")

	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(bal.calls) != 1 {
		t.Fatalf("read %d accounts (%v); single-wallet mode must read exactly one", len(bal.calls), bal.calls)
	}
	hot, vault, split, ok := m.CustodyBreakdown()
	if !ok {
		t.Fatal("no breakdown after a successful check")
	}
	if split {
		t.Fatal("reported split custody when both accounts are the same")
	}
	if hot != 5000 || vault != 0 {
		t.Fatalf("hot/vault = %d/%d cents, want 5000/0", hot, vault)
	}
	// LastReading reports custody, which in single-wallet mode is just the hot balance —
	// so the existing admin dashboard figure is untouched.
	if bal, _, _, _ := m.LastReading(); bal != 5000 {
		t.Fatalf("LastReading balance = %d, want 5000 (unchanged from single-wallet behaviour)", bal)
	}
}

// Split custody: the platform is SOLVENT (vault covers the liability) while the hot
// wallet cannot cover it. That is a top-up task, not an insolvency, and conflating the
// two is what would make a split-custody deployment page its operator constantly.
func TestSplitCustodySolventButHotUnderfunded(t *testing.T) {
	bal := &perAccountBalances{byAccount: map[string]int64{
		"hot":   5 * usdcPerDollar,   // $5 float
		"vault": 500 * usdcPerDollar, // $500 held where the server cannot sign
	}}
	m := NewSolvencyMonitor(stubLiability{cents: 10_000}, bal, "hot", testLog(), prometheus.NewRegistry()) // owes $100
	m.SetVault("vault")

	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	custody, liability, _, ok := m.LastReading()
	if !ok {
		t.Fatal("no reading after a successful check")
	}
	if custody != 50_500 {
		t.Fatalf("custody = %d cents, want 50500 (hot + vault)", custody)
	}
	if custody < liability {
		t.Fatalf("custody %d < liability %d: the platform should read as solvent", custody, liability)
	}
	hot, vault, split, _ := m.CustodyBreakdown()
	if !split || hot != 500 || vault != 50_000 {
		t.Fatalf("breakdown = hot %d, vault %d, split %v; want 500/50000/true", hot, vault, split)
	}
	// The float cannot settle the queue, so approval must refuse — with the USDC reason.
	ok, reason := m.CanPay(10_000)
	if ok {
		t.Fatal("CanPay allowed a payout the float cannot cover")
	}
	if !strings.Contains(reason, "top up the float") {
		t.Fatalf("reason = %q; want the cold→hot top-up instruction", reason)
	}
}

// An unreadable vault must NOT fail the whole pass. The hot-balance-vs-liability
// comparison is the check that matters most, and losing it because a secondary RPC call
// timed out would silence the monitor exactly when the network is flaky.
//
// It must also not be reported as an insolvency: custody is hot-only in that state and
// therefore UNDERSTATES what the platform holds, so a flaky RPC would otherwise announce
// a shortfall — indistinguishable, to the operator reading it, from a theft.
func TestUnreadableVaultDoesNotFailThePass(t *testing.T) {
	bal := &perAccountBalances{
		byAccount: map[string]int64{"hot": 1 * usdcPerDollar, "vault": 900 * usdcPerDollar},
		errFor:    "vault",
	}
	m := NewSolvencyMonitor(stubLiability{cents: 100_000}, bal, "hot", testLog(), prometheus.NewRegistry())
	m.SetVault("vault")

	hotCents, liability, err := m.Check(context.Background())
	if err != nil {
		t.Fatalf("an unreadable VAULT failed the whole pass: %v", err)
	}
	if hotCents != 100 || liability != 100_000 {
		t.Fatalf("Check = %d/%d cents; want the hot balance (100) and liability (100000) still reported", hotCents, liability)
	}
	_, _, _, ok := m.LastReading()
	if !ok {
		t.Fatal("no reading recorded when only the vault read failed")
	}
	// Vault is explicitly NOT known, so consumers can tell "we hold nothing there" from
	// "we could not look".
	if _, vault, _, _ := m.CustodyBreakdown(); vault != 0 {
		t.Fatalf("vault = %d cents; an unreadable vault must report 0, not a guess", vault)
	}
	m.mu.RLock()
	vaultKnown := m.last.VaultKnown
	m.mu.RUnlock()
	if vaultKnown {
		t.Fatal("VaultKnown is true after the vault read failed; the shortfall alarm would fire on a network blip")
	}
}

// CanPay must FAIL OPEN before the first observation. A monitor that blocked payouts
// during its own cold start would halt every approval for the first interval after
// every deploy.
func TestCanPayFailsOpenWithNoReading(t *testing.T) {
	m := NewSolvencyMonitor(stubLiability{}, &perAccountBalances{}, "hot", testLog(), prometheus.NewRegistry())
	if ok, reason := m.CanPay(1_000_000); !ok {
		t.Fatalf("CanPay refused before any reading (%q); it must fail open on unknown", reason)
	}
}

// Likewise for a reading too old to trust: an RPC outage must not become a payout
// outage. The broadcast itself will report the real problem.
func TestCanPayFailsOpenOnStaleReading(t *testing.T) {
	bal := &perAccountBalances{byAccount: map[string]int64{"hot": 0}}
	m := NewSolvencyMonitor(stubLiability{cents: 10_000}, bal, "hot", testLog(), prometheus.NewRegistry())
	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Fresh reading with an empty wallet: refused, as it should be.
	if ok, _ := m.CanPay(10_000); ok {
		t.Fatal("CanPay allowed a payout from an empty wallet on a FRESH reading")
	}
	// Age the reading past the staleness horizon.
	m.mu.Lock()
	m.staleAfter = time.Minute
	m.last.ObservedAt = time.Now().Add(-time.Hour)
	m.mu.Unlock()
	if ok, reason := m.CanPay(10_000); !ok {
		t.Fatalf("CanPay refused on a STALE reading (%q); it must fail open", reason)
	}
}

// Low SOL must be reported as low SOL. "Top up USDC" and "fund the wallet with SOL" are
// different actions, and the wrong sentence sends the operator to move the wrong asset
// while every cash-out keeps failing.
func TestCanPayReportsLowSOLDistinctly(t *testing.T) {
	bal := &perAccountBalances{byAccount: map[string]int64{"hot": 1000 * usdcPerDollar}} // plenty of USDC
	m := NewSolvencyMonitor(stubLiability{cents: 100}, bal, "hot", testLog(), prometheus.NewRegistry())
	m.SetFeeWatch(stubFees{lamports: 1_000_000}, "HotWalletPubkey", 50_000_000) // 0.001 SOL vs 0.05 floor

	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	ok, reason := m.CanPay(100)
	if ok {
		t.Fatal("CanPay allowed a payout with the wallet below its SOL floor")
	}
	if !strings.Contains(reason, "SOL") {
		t.Fatalf("reason = %q; want it to name SOL, not the USDC balance", reason)
	}
	lamports, minLamports, known := m.FeeFuel()
	if !known || lamports != 1_000_000 || minLamports != 50_000_000 {
		t.Fatalf("FeeFuel = %d/%d/%v; want 1000000/50000000/true", lamports, minLamports, known)
	}
}

// With no fee check configured (the devnet default, HOT_WALLET_MIN_SOL_LAMPORTS=0),
// nothing about SOL may block a payout — and FeeFuel must report NOT-known rather than a
// zero balance, which is the opposite conclusion.
func TestNoFeeWatchNeverBlocks(t *testing.T) {
	bal := &perAccountBalances{byAccount: map[string]int64{"hot": 100 * usdcPerDollar}}
	m := NewSolvencyMonitor(stubLiability{}, bal, "hot", testLog(), prometheus.NewRegistry())
	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, _, known := m.FeeFuel(); known {
		t.Fatal("FeeFuel reported a known SOL balance with no fee provider wired")
	}
	if ok, reason := m.CanPay(100); !ok {
		t.Fatalf("CanPay refused (%q) with the SOL check disabled", reason)
	}
}

// The sweep alert has to name a destination. An operator told to "sweep the excess" with
// no address looks one up under time pressure, which is when the wrong one gets pasted.
func TestSweepNeededReportsExcessAndDestination(t *testing.T) {
	bal := &perAccountBalances{byAccount: map[string]int64{"hot": 900 * usdcPerDollar}}
	m := NewSolvencyMonitor(stubLiability{}, bal, "hot", testLog(), prometheus.NewRegistry())
	m.SetExposureCap(50_000) // $500 ceiling
	m.SetColdAddress("ColdVaultAddress")

	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	excess, capCents, cold := m.SweepNeeded()
	if excess != 40_000 {
		t.Fatalf("excess = %d cents, want 40000 ($900 held against a $500 cap)", excess)
	}
	if capCents != 50_000 || cold != "ColdVaultAddress" {
		t.Fatalf("cap/cold = %d/%q; want 50000/ColdVaultAddress", capCents, cold)
	}
}

// No cap set (the devnet default) means no sweep is ever requested.
func TestNoCapNeverRequestsASweep(t *testing.T) {
	bal := &perAccountBalances{byAccount: map[string]int64{"hot": 1_000_000 * usdcPerDollar}}
	m := NewSolvencyMonitor(stubLiability{}, bal, "hot", testLog(), prometheus.NewRegistry())
	if _, _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if excess, _, _ := m.SweepNeeded(); excess != 0 {
		t.Fatalf("excess = %d with no cap configured; want 0", excess)
	}
}

// ---- payout rails -----------------------------------------------------------------

type stubTokenAccounts struct {
	info TokenAccountIdentity
	err  error
}

func (s stubTokenAccounts) TokenAccount(context.Context, string) (TokenAccountIdentity, error) {
	return s.info, s.err
}

// newTestTransferrer builds a real transferrer over a dummy RPC URL (never dialed at
// construction), returning the hot pubkey and the source ATA it was pointed at.
func newTestTransferrer(t *testing.T, sourceATA solana.PublicKey, mint solana.PublicKey) (*SolanaTransferrer, string) {
	t.Helper()
	hot, err := solana.NewRandomPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewSolanaTransferrer("http://127.0.0.1:1", hot.String(), mint.String(), sourceATA.String(), 6)
	if err != nil {
		t.Fatal(err)
	}
	return tr, hot.PublicKey().String()
}

// The failure this check exists for: a hot key that does not own the account it signs
// transfers from. The token program rejects it, so EVERY cash-out fails at broadcast
// while nothing is wrong at deploy time — and a mainnet cutover pairs a fresh key with a
// fresh ATA, which is exactly when it happens.
func TestVerifyRailsCatchesWrongAuthority(t *testing.T) {
	mint := solana.NewWallet().PublicKey()
	ata := solana.NewWallet().PublicKey()
	tr, hotPub := newTestTransferrer(t, ata, mint)

	someoneElse := solana.NewWallet().PublicKey().String()
	ok, detail := tr.VerifyRails(context.Background(), stubTokenAccounts{
		info: TokenAccountIdentity{Mint: mint.String(), Owner: someoneElse},
	})
	if ok {
		t.Fatal("VerifyRails passed an ATA the hot wallet has no authority over")
	}
	if !strings.Contains(detail, hotPub) || !strings.Contains(detail, someoneElse) {
		t.Fatalf("detail = %q; it must name BOTH the real owner and the hot wallet so the fix is obvious", detail)
	}
}

// A payout account holding the wrong mint would move (or fail to move) the wrong asset.
func TestVerifyRailsCatchesWrongMint(t *testing.T) {
	mint := solana.NewWallet().PublicKey()
	ata := solana.NewWallet().PublicKey()
	tr, hotPub := newTestTransferrer(t, ata, mint)

	ok, detail := tr.VerifyRails(context.Background(), stubTokenAccounts{
		info: TokenAccountIdentity{Mint: solana.NewWallet().PublicKey().String(), Owner: hotPub},
	})
	if ok {
		t.Fatalf("VerifyRails passed a payout account holding the wrong mint: %s", detail)
	}
}

// A zero identity means "not a token account at all" — a real misconfiguration, and it
// must be reported rather than treated as unknown.
func TestVerifyRailsCatchesNonTokenAccount(t *testing.T) {
	tr, _ := newTestTransferrer(t, solana.NewWallet().PublicKey(), solana.NewWallet().PublicKey())
	ok, detail := tr.VerifyRails(context.Background(), stubTokenAccounts{})
	if ok {
		t.Fatal("VerifyRails passed an address that is not an SPL token account")
	}
	if !strings.Contains(detail, "not an SPL token account") {
		t.Fatalf("detail = %q; want it to say the address is not a token account", detail)
	}
}

// An RPC failure is UNKNOWN, not wrong. Reporting it as a misconfiguration would cry
// wolf on every network blip, and the recourse an operator learns is to stop reading the
// alert.
func TestVerifyRailsTreatsRPCFailureAsUnknown(t *testing.T) {
	tr, _ := newTestTransferrer(t, solana.NewWallet().PublicKey(), solana.NewWallet().PublicKey())
	ok, detail := tr.VerifyRails(context.Background(), stubTokenAccounts{err: errors.New("dial tcp: refused")})
	if !ok {
		t.Fatal("VerifyRails reported a misconfiguration when the RPC merely failed")
	}
	if !strings.Contains(detail, "unverified") {
		t.Fatalf("detail = %q; want it marked unverified rather than ok", detail)
	}
}

// The happy path: right mint, right authority.
func TestVerifyRailsPassesCorrectRails(t *testing.T) {
	mint := solana.NewWallet().PublicKey()
	ata := solana.NewWallet().PublicKey()
	tr, hotPub := newTestTransferrer(t, ata, mint)

	ok, detail := tr.VerifyRails(context.Background(), stubTokenAccounts{
		info: TokenAccountIdentity{Mint: mint.String(), Owner: hotPub},
	})
	if !ok || detail != "ok" {
		t.Fatalf("VerifyRails = %v, %q; want true, \"ok\"", ok, detail)
	}
	if tr.SourceATA() != ata.String() {
		t.Fatalf("SourceATA = %s, want %s", tr.SourceATA(), ata)
	}
}
