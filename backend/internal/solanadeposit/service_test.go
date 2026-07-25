package solanadeposit

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/blockchain"
	"github.com/agent-arena/arena/internal/platform"
)

const (
	usdcMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	platATA  = "PlatformAtaXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
	platOwn  = "PlatformOwnerXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"
)

// --- fakes -----------------------------------------------------------------

type fakeRepo struct {
	sessions map[string]*Session
	credited map[string]bool // tx_signature set
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{sessions: map[string]*Session{}, credited: map[string]bool{}}
}

func (r *fakeRepo) CreateSession(_ context.Context, s Session) error {
	cp := s
	r.sessions[s.PublicID] = &cp
	return nil
}
func (r *fakeRepo) GetSession(_ context.Context, id string) (Session, error) {
	s, ok := r.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return *s, nil
}
func (r *fakeRepo) GetSessionForUser(_ context.Context, id, user string) (Session, error) {
	s, ok := r.sessions[id]
	if !ok || s.UserPublicID != user {
		return Session{}, ErrNotFound
	}
	return *s, nil
}
func (r *fakeRepo) ListByUser(_ context.Context, user string, _ int) ([]Session, error) {
	var out []Session
	for _, s := range r.sessions {
		if s.UserPublicID == user {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (r *fakeRepo) OpenSessions(_ context.Context, _ int) ([]Session, error) {
	var out []Session
	for _, s := range r.sessions {
		if s.Status == StatusPending || s.Status == StatusDetected {
			out = append(out, *s)
		}
	}
	return out, nil
}
func (r *fakeRepo) MarkDetected(_ context.Context, id, sig string) error {
	if s, ok := r.sessions[id]; ok && s.Status == StatusPending {
		s.Status = StatusDetected
		if sig != "" {
			s.TxSignature = sig
		}
	}
	return nil
}
func (r *fakeRepo) CompleteCredit(_ context.Context, in CreditRecord) (bool, error) {
	already := r.credited[in.TxSignature]
	r.credited[in.TxSignature] = true
	if s, ok := r.sessions[in.SessionPublicID]; ok && s.Status != StatusCompleted {
		s.Status = StatusCompleted
		s.TxSignature = in.TxSignature
		s.AmountReceived = in.AmountBase
		s.CoinsCredited = in.Coins
	}
	return already, nil
}
func (r *fakeRepo) ExpireSession(_ context.Context, id string) error {
	if s, ok := r.sessions[id]; ok && (s.Status == StatusPending || s.Status == StatusDetected) {
		s.Status = StatusExpired
	}
	return nil
}

// fakeChain returns a programmed transaction for any signature it knows.
type fakeChain struct {
	sigs map[string][]blockchain.SignatureInfo // reference → signatures
	txs  map[string]*blockchain.Transaction    // signature → tx (nil = not finalized)
}

func (c *fakeChain) SignaturesForAddress(_ context.Context, ref string, _ int) ([]blockchain.SignatureInfo, error) {
	return c.sigs[ref], nil
}
func (c *fakeChain) GetTransaction(_ context.Context, sig string) (*blockchain.Transaction, error) {
	return c.txs[sig], nil
}

// fakeCrediter mirrors the ledger's idempotency: a repeated idemKey is a no-op.
type fakeCrediter struct {
	seen  map[string]bool
	total map[string]int64 // user → coins actually credited (net of the deposit fee)
	fees  map[string]int64 // user → platform deposit fee taken
	calls int
}

func newFakeCrediter() *fakeCrediter {
	return &fakeCrediter{seen: map[string]bool{}, total: map[string]int64{}, fees: map[string]int64{}}
}
func (c *fakeCrediter) CreditDeposit(_ context.Context, user string, userCoins, feeCoins int64, idemKey string) error {
	c.calls++
	if c.seen[idemKey] {
		return nil // idempotent replay
	}
	c.seen[idemKey] = true
	c.total[user] += userCoins
	c.fees[user] += feeCoins
	return nil
}

// --- helpers ---------------------------------------------------------------

func newSvc(repo Repo, chain Chain, cred Crediter, now time.Time) *Service {
	return New(repo, chain, cred, platform.FixedClock{T: now}, Config{
		USDCMint: usdcMint, PlatformOwner: platOwn, PlatformATA: platATA,
		CoinsPerUSDC: 100, USDCDecimals: 6, SessionTTL: 30 * time.Minute, MinDepositBase: 1_000_000,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// creditTx builds a finalized tx crediting the platform ATA and — crucially —
// includes the session reference in AccountKeys, which the service now re-verifies
// (H3). Pass a mismatching ref to simulate a lying RPC.
func creditTx(sig, ref string, delta int64) *blockchain.Transaction {
	return &blockchain.Transaction{
		Signature: sig, Slot: 100,
		AccountKeys: []string{ref, platATA, platOwn},
		Credits:     []blockchain.TokenCredit{{Account: platATA, Owner: platOwn, Mint: usdcMint, Delta: delta}},
	}
}

// --- tests -----------------------------------------------------------------

func TestCreatePegAndValidation(t *testing.T) {
	svc := newSvc(newFakeRepo(), &fakeChain{}, newFakeCrediter(), time.Unix(1_700_000_000, 0))
	// 5 USDC = 5_000_000 base → 500 coins.
	s, err := svc.Create(context.Background(), "usr_a", 5_000_000)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if s.CoinsExpected != 500 {
		t.Fatalf("coins = %d, want 500", s.CoinsExpected)
	}
	if s.Status != StatusPending || s.Reference == "" {
		t.Fatalf("unexpected session %+v", s)
	}
	// Below the 1 USDC minimum is rejected.
	if _, err := svc.Create(context.Background(), "usr_a", 500_000); err == nil {
		t.Fatal("expected minimum-deposit rejection")
	}
}

func TestPollCreditsFinalizedDeposit(t *testing.T) {
	repo, chain, cred := newFakeRepo(), &fakeChain{sigs: map[string][]blockchain.SignatureInfo{}, txs: map[string]*blockchain.Transaction{}}, newFakeCrediter()
	svc := newSvc(repo, chain, cred, time.Unix(1_700_000_000, 0))
	s, _ := svc.Create(context.Background(), "usr_a", 5_000_000)

	sig := "SoLsIgNaTuRe111"
	chain.sigs[s.Reference] = []blockchain.SignatureInfo{{Signature: sig, ConfirmationStatus: "finalized"}}
	chain.txs[sig] = creditTx(sig, s.Reference, 5_000_000)

	n, err := svc.Poll(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("poll = (%d,%v), want (1,nil)", n, err)
	}
	got, _ := repo.GetSession(context.Background(), s.PublicID)
	if got.Status != StatusCompleted || got.CoinsCredited != 500 {
		t.Fatalf("session not completed correctly: %+v", got)
	}
	if cred.total["usr_a"] != 475 {
		t.Fatalf("credited %d coins net, want 475 (500 gross − 5%% deposit fee)", cred.total["usr_a"])
	}
	if cred.fees["usr_a"] != 25 {
		t.Fatalf("platform deposit fee %d, want 25 (5%% of 500)", cred.fees["usr_a"])
	}

	// Idempotency: a second poll must not double-credit (session already completed,
	// and even if re-seen the ledger idemKey dedupes).
	got.Status = StatusPending // force re-scan to prove idemKey guards the credit
	repo.sessions[s.PublicID].Status = StatusPending
	if _, err := svc.Poll(context.Background()); err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if cred.total["usr_a"] != 475 {
		t.Fatalf("double-credited: total = %d, want 475", cred.total["usr_a"])
	}
}

func TestPollIgnoresUnderpayment(t *testing.T) {
	repo, chain, cred := newFakeRepo(), &fakeChain{sigs: map[string][]blockchain.SignatureInfo{}, txs: map[string]*blockchain.Transaction{}}, newFakeCrediter()
	svc := newSvc(repo, chain, cred, time.Unix(1_700_000_000, 0))
	s, _ := svc.Create(context.Background(), "usr_a", 5_000_000)

	sig := "underpay1"
	chain.sigs[s.Reference] = []blockchain.SignatureInfo{{Signature: sig, ConfirmationStatus: "finalized"}}
	chain.txs[sig] = creditTx(sig, s.Reference, 4_000_000) // only 4 USDC of the 5 expected

	n, _ := svc.Poll(context.Background())
	if n != 0 || cred.total["usr_a"] != 0 {
		t.Fatalf("underpayment credited: n=%d total=%d", n, cred.total["usr_a"])
	}
	got, _ := repo.GetSession(context.Background(), s.PublicID)
	if got.Status == StatusCompleted {
		t.Fatal("session completed on underpayment")
	}
}

func TestPollMarksDetectedWhenUnfinalized(t *testing.T) {
	repo, chain, cred := newFakeRepo(), &fakeChain{sigs: map[string][]blockchain.SignatureInfo{}, txs: map[string]*blockchain.Transaction{}}, newFakeCrediter()
	svc := newSvc(repo, chain, cred, time.Unix(1_700_000_000, 0))
	s, _ := svc.Create(context.Background(), "usr_a", 5_000_000)

	sig := "pending1"
	chain.sigs[s.Reference] = []blockchain.SignatureInfo{{Signature: sig}}
	chain.txs[sig] = nil // seen but not finalized

	svc.Poll(context.Background())
	got, _ := repo.GetSession(context.Background(), s.PublicID)
	if got.Status != StatusDetected {
		t.Fatalf("status = %q, want detected", got.Status)
	}
}

func TestPollExpiresElapsedSession(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	repo := newFakeRepo()
	svc := newSvc(repo, &fakeChain{}, newFakeCrediter(), now)
	s, _ := svc.Create(context.Background(), "usr_a", 5_000_000)

	// A pending session is expired only past the TTL PLUS the grace window (M9), so
	// a late-arriving payment isn't dropped. Just past the TTL it must NOT expire.
	svc.clock = platform.FixedClock{T: now.Add(31 * time.Minute)}
	svc.Poll(context.Background())
	if got, _ := repo.GetSession(context.Background(), s.PublicID); got.Status != StatusPending {
		t.Fatalf("expired within the grace window: status = %q, want pending", got.Status)
	}
	// Past TTL + grace it expires.
	svc.clock = platform.FixedClock{T: now.Add(30*time.Minute + pendingExpiryGrace + time.Minute)}
	svc.Poll(context.Background())
	if got, _ := repo.GetSession(context.Background(), s.PublicID); got.Status != StatusExpired {
		t.Fatalf("status = %q, want expired", got.Status)
	}
}

// TestPollRejectsTxWithoutReference is the H3 regression: a finalized tx that
// credits the platform ATA but whose account list does NOT contain the session
// reference (a lying/MITM RPC returning an unrelated real deposit) must never
// credit coins.
func TestPollRejectsTxWithoutReference(t *testing.T) {
	repo, chain, cred := newFakeRepo(), &fakeChain{sigs: map[string][]blockchain.SignatureInfo{}, txs: map[string]*blockchain.Transaction{}}, newFakeCrediter()
	svc := newSvc(repo, chain, cred, time.Unix(1_700_000_000, 0))
	s, _ := svc.Create(context.Background(), "usr_a", 5_000_000)

	sig := "forged1"
	chain.sigs[s.Reference] = []blockchain.SignatureInfo{{Signature: sig, ConfirmationStatus: "finalized"}}
	// tx credits the platform ATA for the full amount but references a DIFFERENT key.
	chain.txs[sig] = creditTx(sig, "SomeOtherReferenceKeyNotOurs", 5_000_000)

	n, _ := svc.Poll(context.Background())
	if n != 0 || cred.total["usr_a"] != 0 {
		t.Fatalf("credited a tx that did not contain the session reference: n=%d total=%d", n, cred.total["usr_a"])
	}
}

// TestDetectedSessionSurvivesLongGrace is the M9 regression: a session that has
// SEEN an in-flight payment (detected) must not be expired at the pending grace —
// it's protected far longer so the deposit can finalize and credit.
func TestDetectedSessionSurvivesLongGrace(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	repo := newFakeRepo()
	svc := newSvc(repo, &fakeChain{}, newFakeCrediter(), now)
	s, _ := svc.Create(context.Background(), "usr_a", 5_000_000)
	repo.sessions[s.PublicID].Status = StatusDetected // a referencing payment was seen

	// Well past TTL + pending grace, a detected session is still protected.
	svc.clock = platform.FixedClock{T: now.Add(30*time.Minute + pendingExpiryGrace + time.Hour)}
	svc.Poll(context.Background())
	if got, _ := repo.GetSession(context.Background(), s.PublicID); got.Status != StatusDetected {
		t.Fatalf("detected session expired too early: status = %q, want detected", got.Status)
	}
}
