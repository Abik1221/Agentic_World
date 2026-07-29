package solanadeposit

import "testing"

// Distinct names: the package's existing tests already define regUSDC.
const (
	regUSDC = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
	regUSDT = "Es9vMFrzaCERmJfrF4H2FYD4KCoNkY11McCe8BenwNYB"
)

func TestParsesMultipleMintsWithTheirOwnDecimals(t *testing.T) {
	r, err := ParseMints("USDC:" + regUSDC + ":6, USDT:" + regUSDT + ":6")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.All()) != 2 {
		t.Fatalf("got %d mints, want 2", len(r.All()))
	}
	if r.Default().Symbol != "USDC" {
		t.Fatalf("default = %q, want the first configured (USDC)", r.Default().Symbol)
	}
}

// The reason this type exists. A token's decimals must come from ITS OWN entry — a
// shared value credits a 9-decimal token at 6 decimals and pays out 1000x.
func TestDecimalsArePerMintNotGlobal(t *testing.T) {
	r, err := ParseMints("USDC:" + regUSDC + ":6,WSOL:So11111111111111111111111111111111111111112:9")
	if err != nil {
		t.Fatal(err)
	}
	usdc, _ := r.Lookup(regUSDC)
	wsol, _ := r.Lookup("So11111111111111111111111111111111111111112")
	if usdc.Decimals != 6 || wsol.Decimals != 9 {
		t.Fatalf("decimals did not travel with the mint: usdc=%d wsol=%d", usdc.Decimals, wsol.Decimals)
	}
}

// An unknown mint must NOT be credited: neither its decimals nor its dollar value is
// known, so any amount derived from it is invented.
func TestUnknownMintIsNotAccepted(t *testing.T) {
	r, _ := ParseMints("USDC:" + regUSDC + ":6")
	if _, ok := r.Lookup("SomeRandomMint1111111111111111111111111111"); ok {
		t.Fatal("an unlisted mint was accepted")
	}
	if _, err := r.Resolve("SomeRandomMint1111111111111111111111111111"); err == nil {
		t.Fatal("Resolve accepted an unlisted mint")
	}
}

// Substituting a different token than the one requested would send someone into a
// flow built for something else. Empty means "use the default"; wrong means error.
func TestResolveDefaultsOnlyWhenNothingWasAsked(t *testing.T) {
	r, _ := ParseMints("USDC:" + regUSDC + ":6,USDT:" + regUSDT + ":6")
	if m, err := r.Resolve(""); err != nil || m.Symbol != "USDC" {
		t.Fatalf("empty request should default to USDC, got %v %v", m, err)
	}
	if m, err := r.Resolve("usdt"); err != nil || m.Mint != regUSDT {
		t.Fatalf("symbol lookup failed: %v %v", m, err)
	}
	if m, err := r.Resolve(regUSDT); err != nil || m.Symbol != "USDT" {
		t.Fatalf("address lookup failed: %v %v", m, err)
	}
	if _, err := r.Resolve("DOGE"); err == nil {
		t.Fatal("an unaccepted symbol silently resolved to something")
	}
}

func TestMalformedConfigIsRejectedAtBoot(t *testing.T) {
	for _, bad := range []string{
		"",                       // nothing configured
		"USDC:" + regUSDC,        // missing decimals
		"USDC:" + regUSDC + ":x", // non-numeric decimals
		"USDC:" + regUSDC + ":99",
		"USDC:" + regUSDC + ":-1",
		":" + regUSDC + ":6", // no symbol
		"USDC::6",            // no address
		"USDC:" + regUSDC + ":6,USDT:" + regUSDC + ":6", // same mint twice
	} {
		if _, err := ParseMints(bad); err == nil {
			t.Fatalf("accepted malformed config: %q", bad)
		}
	}
}
