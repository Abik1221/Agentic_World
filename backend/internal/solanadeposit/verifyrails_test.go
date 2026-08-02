package solanadeposit

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/blockchain"
)

type fakeTokenAccounts struct {
	info blockchain.TokenAccountInfo
	err  error
	// asked records which account was queried, so the test can prove the check
	// inspects the ATA we credit against — not the owner address we advertise.
	asked string
}

func (f *fakeTokenAccounts) TokenAccount(_ context.Context, acct string) (blockchain.TokenAccountInfo, error) {
	f.asked = acct
	return f.info, f.err
}

func railsService(cfg Config) *Service {
	s := &Service{cfg: cfg, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	return s
}

// Reuses the package's existing rail constants (service_test.go) so a config
// change cannot leave these two suites describing different platforms.
const (
	mint  = usdcMint
	owner = platOwn
	ata   = platATA
)

func TestVerifyRailsAcceptsAMatchingATA(t *testing.T) {
	chain := &fakeTokenAccounts{info: blockchain.TokenAccountInfo{Mint: mint, Owner: owner}}
	s := railsService(Config{USDCMint: mint, PlatformOwner: owner, PlatformATA: ata})

	ok, detail := s.VerifyRails(context.Background(), chain)
	if !ok {
		t.Fatalf("a correctly configured rail must verify: %s", detail)
	}
	if chain.asked != ata {
		t.Fatalf("the check must inspect the credited ATA, asked for %q", chain.asked)
	}
}

// The failure this whole check exists for: payers are directed at one wallet,
// crediting watches an account belonging to a different one. On-chain everything
// succeeds; nothing is ever credited.
func TestVerifyRailsRejectsAForeignOwner(t *testing.T) {
	chain := &fakeTokenAccounts{info: blockchain.TokenAccountInfo{Mint: mint, Owner: "SomeoneElsesWallet1111111111111111111111111"}}
	s := railsService(Config{USDCMint: mint, PlatformOwner: owner, PlatformATA: ata})

	ok, detail := s.VerifyRails(context.Background(), chain)
	if ok {
		t.Fatal("an ATA owned by another wallet must NOT verify")
	}
	if !strings.Contains(detail, "never credit") {
		t.Fatalf("the operator must be told the consequence, got %q", detail)
	}
}

// An ATA for the wrong token: USDC is sent, the account holds USDT, and
// receivedToPlatform sums nothing.
func TestVerifyRailsRejectsAWrongMint(t *testing.T) {
	chain := &fakeTokenAccounts{info: blockchain.TokenAccountInfo{Mint: regUSDT, Owner: owner}}
	s := railsService(Config{USDCMint: mint, PlatformOwner: owner, PlatformATA: ata})

	if ok, _ := s.VerifyRails(context.Background(), chain); ok {
		t.Fatal("an ATA holding a different mint must NOT verify")
	}
}

func TestVerifyRailsRejectsANonTokenAccount(t *testing.T) {
	chain := &fakeTokenAccounts{err: blockchain.ErrNotToken}
	s := railsService(Config{USDCMint: mint, PlatformOwner: owner, PlatformATA: ata})

	if ok, _ := s.VerifyRails(context.Background(), chain); ok {
		t.Fatal("an address that is not a token account must NOT verify")
	}
}

// An unreachable RPC is UNKNOWN, not WRONG. Reporting a boot-time network blip as
// a misconfiguration would train operators to ignore the one alarm that matters.
func TestVerifyRailsTreatsAnRPCOutageAsUnknown(t *testing.T) {
	chain := &fakeTokenAccounts{err: errors.New("dial tcp: connection refused")}
	s := railsService(Config{USDCMint: mint, PlatformOwner: owner, PlatformATA: ata})

	ok, detail := s.VerifyRails(context.Background(), chain)
	if !ok {
		t.Fatal("an RPC outage must not be reported as a misconfiguration")
	}
	if !strings.HasPrefix(detail, "unverified") {
		t.Fatalf("an outage must be reported as unverified, got %q", detail)
	}
}

func TestVerifyRailsRejectsIncompleteConfig(t *testing.T) {
	chain := &fakeTokenAccounts{info: blockchain.TokenAccountInfo{Mint: mint, Owner: owner}}
	for name, cfg := range map[string]Config{
		"no ata":   {USDCMint: mint, PlatformOwner: owner},
		"no owner": {USDCMint: mint, PlatformATA: ata},
		"no mint":  {PlatformOwner: owner, PlatformATA: ata},
	} {
		if ok, _ := railsService(cfg).VerifyRails(context.Background(), chain); ok {
			t.Errorf("%s: incomplete rails must not verify", name)
		}
	}
}
