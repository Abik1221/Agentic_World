package config

import "testing"

// mainnetBase is a coherent mainnet config with Solana withdrawals enabled, so the
// mainnet-only custody requirements actually run. Tests mutate the one field under test.
func mainnetBase() *Config {
	return &Config{
		SolanaCluster:            ClusterMainnet,
		SolanaRPCURL:             "https://api.mainnet-beta.solana.com",
		SolanaUSDCMint:           mainnetUSDCMint,
		SolanaPlatformOwner:      "PlatformOwnerPubkey",
		SolanaPlatformATA:        "PlatformATA",
		SolanaHotWalletSecretEnc: "sealed", // ⇒ WithdrawalsSolana() is true
		HotWalletCapCents:        500_00,
		HotWalletMinSOLLamports:  50_000_000,
		SolanaColdWalletAddress:  "ColdWalletPubkey",
	}
}

func TestMainnetCustodyHappyPath(t *testing.T) {
	if errs := mainnetBase().validateSolanaCluster(); len(errs) != 0 {
		t.Fatalf("a coherent mainnet config must boot, got: %v", errs)
	}
}

// PayoutATA is the single accessor the rest of the code uses, so the single-wallet and
// split deployments take the same path and neither is a special case.
func TestPayoutATAFallsBackToDepositAccount(t *testing.T) {
	c := &Config{SolanaPlatformATA: "deposits"}
	if got := c.PayoutATA(); got != "deposits" {
		t.Fatalf("PayoutATA = %q with no payout account set, want the deposit account", got)
	}
	if c.CustodySplit() {
		t.Fatal("CustodySplit is true with no payout account configured")
	}

	c.SolanaPayoutATA = "float"
	if got := c.PayoutATA(); got != "float" {
		t.Fatalf("PayoutATA = %q, want the configured payout account", got)
	}
	if !c.CustodySplit() {
		t.Fatal("CustodySplit is false with distinct deposit and payout accounts")
	}

	// Both set to the same value is the single-wallet setup spelled out redundantly, and
	// must NOT read as a split — an operator who thinks custody is split when it is not
	// believes the hot wallet is a small float when it holds everything.
	c.SolanaPayoutATA = "deposits"
	if c.CustodySplit() {
		t.Fatal("CustodySplit is true when both accounts are the same")
	}
}

// ---- cluster-derived network constants ---------------------------------------------

// The cluster determines the mint, the endpoint and the explorer. Deriving them is what
// removes the failure the old default created: SOLANA_USDC_MINT used to default to the
// REAL mainnet mint on every cluster, so a devnet deploy that forgot it settled devnet
// play against real USDC with nothing in the logs saying so.
func TestClusterDerivesNetworkConstants(t *testing.T) {
	for _, tc := range []struct{ cluster, mint, rpc string }{
		{ClusterMainnet, mainnetUSDCMint, "https://api.mainnet-beta.solana.com"},
		{ClusterDevnet, devnetUSDCMint, "https://api.devnet.solana.com"},
	} {
		c := &Config{SolanaCluster: tc.cluster}
		c.applyClusterDefaults()
		if c.SolanaUSDCMint != tc.mint {
			t.Errorf("%s mint = %q, want %q", tc.cluster, c.SolanaUSDCMint, tc.mint)
		}
		if c.SolanaRPCURL != tc.rpc {
			t.Errorf("%s rpc = %q, want %q", tc.cluster, c.SolanaRPCURL, tc.rpc)
		}
		if c.SolanaExplorerTx == "" {
			t.Errorf("%s explorer template is empty", tc.cluster)
		}
	}
}

// The devnet explorer link must carry the cluster, or every devnet receipt points at a
// mainnet lookup that finds nothing — which reads to the user as verification FAILING
// rather than as a wrong link.
func TestDevnetExplorerCarriesTheCluster(t *testing.T) {
	c := &Config{SolanaCluster: ClusterDevnet}
	c.applyClusterDefaults()
	if !contains(c.SolanaExplorerTx, "cluster=devnet") {
		t.Fatalf("devnet explorer = %q; want the cluster in the query string", c.SolanaExplorerTx)
	}
}

// Explicit env vars must still win, under their EXISTING names, so a private RPC or a
// custom devnet token keeps working and no CI secret has to be renamed.
func TestExplicitValuesOverrideClusterDefaults(t *testing.T) {
	c := &Config{
		SolanaCluster:    ClusterMainnet,
		SolanaRPCURL:     "https://my-node.helius-rpc.com/?api-key=x",
		SolanaUSDCMint:   mainnetUSDTMint, // a different accepted stablecoin
		SolanaExplorerTx: "https://explorer.solana.com/tx/%s",
	}
	c.applyClusterDefaults()
	if c.SolanaRPCURL != "https://my-node.helius-rpc.com/?api-key=x" {
		t.Fatalf("rpc = %q; an explicit endpoint must not be overwritten", c.SolanaRPCURL)
	}
	if c.SolanaUSDCMint != mainnetUSDTMint {
		t.Fatalf("mint = %q; an explicit mint must not be overwritten", c.SolanaUSDCMint)
	}
	if c.SolanaExplorerTx != "https://explorer.solana.com/tx/%s" {
		t.Fatalf("explorer = %q; an explicit template must not be overwritten", c.SolanaExplorerTx)
	}
}

// An unknown or unset cluster must leave everything alone. Guessing a network here would
// be more dangerous than an empty value, and validateSolanaCluster is what reports it.
func TestUnknownClusterDerivesNothing(t *testing.T) {
	for _, cluster := range []string{"", "mainnet", "nonsense"} {
		c := &Config{SolanaCluster: cluster}
		c.applyClusterDefaults()
		if c.SolanaUSDCMint != "" || c.SolanaRPCURL != "" || c.SolanaExplorerTx != "" {
			t.Fatalf("cluster %q derived values (%q/%q/%q); it must derive nothing",
				cluster, c.SolanaUSDCMint, c.SolanaRPCURL, c.SolanaExplorerTx)
		}
	}
}

// A cluster-only config is now complete enough to boot the deposit rail once the
// deployment-specific accounts are supplied — the whole point of deriving.
func TestClusterPlusAccountsIsEnoughToBoot(t *testing.T) {
	c := &Config{
		SolanaCluster:       ClusterDevnet,
		SolanaPlatformOwner: "owner",
		SolanaPlatformATA:   "ata",
	}
	c.applyClusterDefaults()
	if errs := c.validateSolanaCluster(); len(errs) != 0 {
		t.Fatalf("cluster + accounts must be a complete devnet config, got: %v", errs)
	}
	if !c.DepositsEnabled() {
		t.Fatal("deposits are not enabled by cluster + owner + ATA alone")
	}
}

// Solana half-configured with NO cluster must still refuse to boot loudly.
//
// This is the regression the derivation could have introduced: with the mint no longer
// defaulting, DepositsEnabled() goes false, and gating the cluster check on that would
// turn "you forgot SOLANA_CLUSTER" into deposits silently switched off — a deployment
// that looks healthy and quietly cannot take money.
func TestPartialSolanaConfigWithoutClusterStillFails(t *testing.T) {
	for name, c := range map[string]*Config{
		"rpc only":   {SolanaRPCURL: "https://api.devnet.solana.com"},
		"ata only":   {SolanaPlatformATA: "ata"},
		"owner only": {SolanaPlatformOwner: "owner"},
		"mint only":  {SolanaUSDCMint: mainnetUSDCMint},
	} {
		c.applyClusterDefaults()
		if errs := c.validateSolanaCluster(); !errsContain(errs, "SOLANA_CLUSTER is required") {
			t.Errorf("%s: want a loud missing-cluster failure, got: %v", name, errs)
		}
	}
}

// Nothing Solana-related at all stays silent — demanding a cluster from a deployment that
// never touches chain would just be noise.
func TestNoSolanaConfigNeedsNoCluster(t *testing.T) {
	c := &Config{}
	c.applyClusterDefaults()
	if errs := c.validateSolanaCluster(); len(errs) != 0 {
		t.Fatalf("a deployment with no Solana config must not require a cluster: %v", errs)
	}
}

// A devnet deployment must be entirely unaffected by the new settings: no cold address,
// no SOL floor, no payout account. That is the setup already running.
func TestDevnetNeedsNoCustodySettings(t *testing.T) {
	c := &Config{
		SolanaCluster:            ClusterDevnet,
		SolanaRPCURL:             "https://api.devnet.solana.com",
		SolanaUSDCMint:           "Fr8dGZ3MMwd3WsSYbdkxhAd4vkezTZfnQVn2aqXh5QaU",
		SolanaPlatformOwner:      "owner",
		SolanaPlatformATA:        "ata",
		SolanaHotWalletSecretEnc: "sealed", // withdrawals on, still no custody config
	}
	if errs := c.validateSolanaCluster(); len(errs) != 0 {
		t.Fatalf("devnet must boot with no custody settings at all, got: %v", errs)
	}
}

// The cap tells the operator to sweep; with no destination configured that instruction is
// incomplete, and it gets completed under time pressure from whatever address is to hand.
func TestMainnetRequiresColdWalletAddress(t *testing.T) {
	c := mainnetBase()
	c.SolanaColdWalletAddress = ""
	errs := c.validateSolanaCluster()
	if !errsContain(errs, "SOLANA_COLD_WALLET_ADDRESS must be set") {
		t.Fatalf("mainnet withdrawals with no cold address must refuse to boot, got: %v", errs)
	}
}

// Zero floor means nothing ever reports that the signing wallet is out of SOL — a
// failure that stops every cash-out at once while every USDC check stays green.
func TestMainnetRequiresSOLFloor(t *testing.T) {
	c := mainnetBase()
	c.HotWalletMinSOLLamports = 0
	errs := c.validateSolanaCluster()
	if !errsContain(errs, "HOT_WALLET_MIN_SOL_LAMPORTS must be > 0") {
		t.Fatalf("mainnet withdrawals with no SOL floor must refuse to boot, got: %v", errs)
	}
}

// A "cold" address the server can sign for is not cold. Checked on every cluster,
// because discovering it after the cutover is discovering it too late.
func TestColdAddressMustNotBeTheHotWallet(t *testing.T) {
	c := mainnetBase()
	c.SolanaColdWalletAddress = c.SolanaPlatformOwner
	if errs := c.validateSolanaCluster(); !errsContain(errs, "same as SOLANA_PLATFORM_OWNER") {
		t.Fatalf("a cold address equal to the platform owner must be rejected, got: %v", errs)
	}

	// Devnet too — the check is not mainnet-gated.
	d := &Config{
		SolanaCluster:           ClusterDevnet,
		SolanaRPCURL:            "https://api.devnet.solana.com",
		SolanaUSDCMint:          "Fr8dGZ3MMwd3WsSYbdkxhAd4vkezTZfnQVn2aqXh5QaU",
		SolanaPlatformOwner:     "owner",
		SolanaPlatformATA:       "ata",
		SolanaColdWalletAddress: "owner",
	}
	if errs := d.validateSolanaCluster(); !errsContain(errs, "same as SOLANA_PLATFORM_OWNER") {
		t.Fatalf("the cold-address check must apply on devnet as well, got: %v", errs)
	}
}

// Sweeping to one of our own token accounts is not cold storage either.
func TestColdAddressMustNotBeAPlatformTokenAccount(t *testing.T) {
	c := mainnetBase()
	c.SolanaColdWalletAddress = c.SolanaPlatformATA
	if errs := c.validateSolanaCluster(); !errsContain(errs, "one of the platform token accounts") {
		t.Fatalf("a cold address equal to the deposit ATA must be rejected, got: %v", errs)
	}

	c = mainnetBase()
	c.SolanaPayoutATA = "FloatATA"
	c.SolanaColdWalletAddress = "FloatATA"
	if errs := c.validateSolanaCluster(); !errsContain(errs, "one of the platform token accounts") {
		t.Fatalf("a cold address equal to the payout ATA must be rejected, got: %v", errs)
	}
}

// Redundantly setting the payout account to the deposit account is legal but misleading,
// so it warns rather than failing: the deployment works, but the operator's mental model
// of where the money sits does not match reality.
func TestRedundantPayoutATAWarnsWithoutFailing(t *testing.T) {
	c := mainnetBase()
	c.SolanaPayoutATA = c.SolanaPlatformATA
	errs := c.validateSolanaCluster()
	if len(errs) != 0 {
		t.Fatalf("a redundant payout ATA must not block boot, got: %v", errs)
	}
	if !errsContain(c.Warnings, "custody is NOT split") {
		t.Fatalf("expected a warning that custody is not split, got: %v", c.Warnings)
	}
}

// Split custody on mainnet is the target configuration; it must boot cleanly and warn
// about nothing.
func TestSplitCustodyMainnetBootsClean(t *testing.T) {
	c := mainnetBase()
	c.SolanaPayoutATA = "FloatATA"
	errs := c.validateSolanaCluster()
	if len(errs) != 0 {
		t.Fatalf("split custody on mainnet must boot, got: %v", errs)
	}
	if len(c.Warnings) != 0 {
		t.Fatalf("split custody produced warnings: %v", c.Warnings)
	}
	if c.PayoutATA() != "FloatATA" || !c.CustodySplit() {
		t.Fatalf("PayoutATA/%v CustodySplit/%v; want FloatATA/true", c.PayoutATA(), c.CustodySplit())
	}
}
