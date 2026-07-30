package config

import "testing"

// base returns a Config with Solana deposits fully enabled, so validateSolanaCluster
// actually runs. Individual tests mutate the one field under examination.
func base() *Config {
	return &Config{
		SolanaCluster:       ClusterDevnet,
		SolanaRPCURL:        "https://api.devnet.solana.com",
		SolanaUSDCMint:      "Fr8dGZ3MMwd3WsSYbdkxhAd4vkezTZfnQVn2aqXh5QaU", // devnet mint
		SolanaPlatformOwner: "owner",
		SolanaPlatformATA:   "ata",
	}
}

func errsContain(errs []string, want string) bool {
	for _, e := range errs {
		if len(e) >= len(want) && contains(e, want) {
			return true
		}
	}
	return false
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func TestDevnetHappyPath(t *testing.T) {
	if errs := base().validateSolanaCluster(); len(errs) != 0 {
		t.Fatalf("a coherent devnet config must boot, got: %v", errs)
	}
}

// THE bug this whole switch exists to catch: SOLANA_USDC_MINT defaults to the real
// mainnet USDC mint, so a devnet deploy that forgets to set it settles devnet play
// in real USDC. Nothing else in the system would notice.
func TestDevnetWithUnsetMintIsRefused(t *testing.T) {
	c := base()
	c.SolanaUSDCMint = mainnetUSDCMint // i.e. the default, left untouched
	errs := c.validateSolanaCluster()
	if !errsContain(errs, "MAINNET mint") {
		t.Fatalf("devnet + mainnet mint must be refused, got: %v", errs)
	}
}

func TestMainnetOnATestRPCIsRefused(t *testing.T) {
	c := base()
	c.SolanaCluster = ClusterMainnet
	c.SolanaUSDCMint = mainnetUSDCMint
	c.SolanaRPCURL = "https://api.devnet.solana.com"
	if errs := c.validateSolanaCluster(); !errsContain(errs, "test endpoint") {
		t.Fatalf("mainnet on a devnet RPC must be refused, got: %v", errs)
	}
}

// The inverse, and the expensive one: a devnet-labelled build whose RPC is actually
// mainnet spends real funds during testing.
func TestDevnetOnANonTestRPCIsRefused(t *testing.T) {
	c := base()
	c.SolanaRPCURL = "https://api.mainnet-beta.solana.com"
	if errs := c.validateSolanaCluster(); !errsContain(errs, "REAL funds") {
		t.Fatalf("devnet on a non-devnet RPC must be refused, got: %v", errs)
	}
}

func TestMissingClusterIsRefusedRatherThanDefaulted(t *testing.T) {
	c := base()
	c.SolanaCluster = ""
	errs := c.validateSolanaCluster()
	if !errsContain(errs, "SOLANA_CLUSTER is required") {
		t.Fatalf("an unset cluster must fail, not pick a side: %v", errs)
	}
}

func TestUnknownClusterIsRefused(t *testing.T) {
	c := base()
	c.SolanaCluster = "mainnet" // plausible, wrong
	if errs := c.validateSolanaCluster(); !errsContain(errs, "invalid") {
		t.Fatalf("a near-miss cluster name must fail: %v", errs)
	}
}

// No Solana rail configured at all => nothing to cross-check. Demanding a cluster
// from deployments that never touch chain would just be noise.
func TestNoDepositsConfiguredSkipsTheCheck(t *testing.T) {
	c := &Config{}
	if errs := c.validateSolanaCluster(); len(errs) != 0 {
		t.Fatalf("no deposits configured should not require a cluster: %v", errs)
	}
}

func TestMainnetRequiresAnExposureCapWhenWithdrawalsAreOn(t *testing.T) {
	c := base()
	c.SolanaCluster = ClusterMainnet
	c.SolanaUSDCMint = mainnetUSDCMint
	c.SolanaRPCURL = "https://api.mainnet-beta.solana.com"
	c.SolanaHotWalletSecretEnc = "enc" // withdrawals on
	if errs := c.validateSolanaCluster(); !errsContain(errs, "HOT_WALLET_CAP_CENTS") {
		t.Fatalf("mainnet withdrawals with no cap must be refused: %v", errs)
	}
	c.HotWalletCapCents = 50_000
	if errs := c.validateSolanaCluster(); len(errs) != 0 {
		t.Fatalf("mainnet with a cap set must boot: %v", errs)
	}
}

func TestMainnetRefusesTheFreeCoinEndpoint(t *testing.T) {
	c := base()
	c.SolanaCluster = ClusterMainnet
	c.SolanaUSDCMint = mainnetUSDCMint
	c.SolanaRPCURL = "https://api.mainnet-beta.solana.com"
	c.AllowMint = true
	if errs := c.validateSolanaCluster(); !errsContain(errs, "ALLOW_MINT") {
		t.Fatalf("mainnet with ALLOW_MINT on must be refused: %v", errs)
	}
}
