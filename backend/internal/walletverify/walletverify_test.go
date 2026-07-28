package walletverify_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/platform"
	"github.com/agent-arena/arena/internal/walletverify"
	solana "github.com/gagliardetto/solana-go"
)

type fakeRepo struct {
	ch       map[string]walletverify.Challenge
	verified map[string]string // user → verified wallet
}

func newRepo() *fakeRepo {
	return &fakeRepo{ch: map[string]walletverify.Challenge{}, verified: map[string]string{}}
}
func (r *fakeRepo) SaveChallenge(_ context.Context, user, wallet, nonce string, exp time.Time) error {
	r.ch[user] = walletverify.Challenge{WalletAddress: wallet, Nonce: nonce, ExpiresAt: exp}
	return nil
}
func (r *fakeRepo) GetChallenge(_ context.Context, user string) (walletverify.Challenge, bool, error) {
	c, ok := r.ch[user]
	return c, ok, nil
}
func (r *fakeRepo) MarkVerified(_ context.Context, user, wallet string, _ time.Time) error {
	r.verified[user] = wallet
	return nil
}
func (r *fakeRepo) ClearChallenge(_ context.Context, user string) error {
	delete(r.ch, user)
	return nil
}
func (r *fakeRepo) ClearVerified(_ context.Context, user string) error {
	delete(r.verified, user)
	return nil
}

// newWallet returns a fresh Solana keypair as (base58 address, signer).
func newWallet(t *testing.T) (string, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var pk solana.PublicKey
	copy(pk[:], pub)
	return pk.String(), priv
}

// sign produces the base58 signature a Solana wallet would return for `msg`.
func sign(priv ed25519.PrivateKey, msg string) string {
	var s solana.Signature
	copy(s[:], ed25519.Sign(priv, []byte(msg)))
	return s.String()
}

func newSvc(r *fakeRepo) *walletverify.Service {
	return walletverify.New(r, platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()})
}

func TestChallengeVerifyRoundTrip(t *testing.T) {
	repo := newRepo()
	svc := newSvc(repo)
	ctx := context.Background()
	addr, priv := newWallet(t)

	msg, _, err := svc.StartChallenge(ctx, "usr_a", addr)
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if err := svc.Verify(ctx, "usr_a", addr, sign(priv, msg)); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if repo.verified["usr_a"] != addr {
		t.Fatalf("wallet not recorded verified: %q", repo.verified["usr_a"])
	}
}

func TestVerifyRejectsWrongSigner(t *testing.T) {
	repo := newRepo()
	svc := newSvc(repo)
	ctx := context.Background()
	addr, _ := newWallet(t)
	_, other := newWallet(t) // a different key signs

	msg, _, _ := svc.StartChallenge(ctx, "usr_a", addr)
	if err := svc.Verify(ctx, "usr_a", addr, sign(other, msg)); err != walletverify.ErrBadWalletSignature {
		t.Fatalf("wrong signer = %v, want ErrBadWalletSignature", err)
	}
	if _, ok := repo.verified["usr_a"]; ok {
		t.Fatal("must not mark verified on a bad signature")
	}
}

func TestVerifyRejectsTamperedMessage(t *testing.T) {
	repo := newRepo()
	svc := newSvc(repo)
	ctx := context.Background()
	addr, priv := newWallet(t)

	_, _, _ = svc.StartChallenge(ctx, "usr_a", addr)
	// Sign a DIFFERENT message than the stored challenge → rejected.
	if err := svc.Verify(ctx, "usr_a", addr, sign(priv, "pyyol wallet verification: not-the-nonce")); err != walletverify.ErrBadWalletSignature {
		t.Fatalf("tampered message = %v, want ErrBadWalletSignature", err)
	}
}

func TestVerifyNoChallenge(t *testing.T) {
	svc := newSvc(newRepo())
	addr, priv := newWallet(t)
	if err := svc.Verify(context.Background(), "usr_a", addr, sign(priv, "x")); err != walletverify.ErrNoChallenge {
		t.Fatalf("no challenge = %v, want ErrNoChallenge", err)
	}
}

// Unlink takes the wallet back off the account: a verified wallet, then no wallet, and
// a pending challenge cleared along with it.
func TestUnlinkRemovesVerifiedWallet(t *testing.T) {
	repo := newRepo()
	svc := newSvc(repo)
	ctx := context.Background()
	addr, priv := newWallet(t)

	msg, _, err := svc.StartChallenge(ctx, "usr_a", addr)
	if err != nil {
		t.Fatalf("challenge: %v", err)
	}
	if err := svc.Verify(ctx, "usr_a", addr, sign(priv, msg)); err != nil {
		t.Fatalf("verify: %v", err)
	}

	if err := svc.Unlink(ctx, "usr_a"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	if w, ok := repo.verified["usr_a"]; ok {
		t.Fatalf("wallet still linked after unlink: %q", w)
	}
	if _, ok := repo.ch["usr_a"]; ok {
		t.Fatal("pending challenge survived unlink")
	}
}

// Unlinking with nothing linked is a no-op, not an error — the UI can offer "remove"
// without first proving a wallet exists.
func TestUnlinkWithoutLinkedWalletIsNoop(t *testing.T) {
	repo := newRepo()
	if err := newSvc(repo).Unlink(context.Background(), "usr_a"); err != nil {
		t.Fatalf("unlink on empty account: %v", err)
	}
}

func TestVerifyExpiredChallenge(t *testing.T) {
	repo := newRepo()
	// Pre-seed an already-expired challenge.
	repo.ch["usr_a"] = walletverify.Challenge{WalletAddress: "x", Nonce: "n", ExpiresAt: time.Unix(1_600_000_000, 0)}
	svc := newSvc(repo)
	if err := svc.Verify(context.Background(), "usr_a", "x", "sig"); err != walletverify.ErrChallengeExpired {
		t.Fatalf("expired = %v, want ErrChallengeExpired", err)
	}
}
