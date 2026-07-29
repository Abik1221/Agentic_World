package config

import "testing"

// The four env vars that gate deposits, asserted as a unit.
//
// A missing one does not error — it silently leaves DepositsEnabled() false, so
// /v1/deposits answers 503 and "Buy coins" cannot work. There is no log line saying
// why, which is exactly how a deploy looks healthy while nobody can fund an account.
func TestDepositGateNeedsAllFour(t *testing.T) {
	full := func() *Config {
		return &Config{
			SolanaRPCURL:        "https://api.mainnet-beta.solana.com",
			SolanaUSDCMint:      "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v",
			SolanaPlatformOwner: "OwnerPubkey111",
			SolanaPlatformATA:   "AtaPubkey222",
		}
	}
	if !full().DepositsEnabled() {
		t.Fatal("all four set but deposits are still off")
	}
	for _, blank := range []func(*Config){
		func(c *Config) { c.SolanaRPCURL = "" },
		func(c *Config) { c.SolanaUSDCMint = "" },
		func(c *Config) { c.SolanaPlatformOwner = "" },
		func(c *Config) { c.SolanaPlatformATA = "" },
	} {
		c := full()
		blank(c)
		if c.DepositsEnabled() {
			t.Fatal("deposits enabled with one of the four missing")
		}
	}
}
