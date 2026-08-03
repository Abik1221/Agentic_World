package config

import (
	"strings"
	"testing"
)

// baseEnv sets the four variables Load() requires, so a test can assert a default.
func baseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	t.Setenv("REDIS_URL", "redis://localhost:6379/0")
	t.Setenv("JWT_SIGNING_KEY", "this-is-a-sufficiently-long-dev-signing-key!")
	t.Setenv("API_KEY_PEPPER", "pepper")
}

// The platform's take on a coin round trip is charged ONCE, on the way out.
//
// Free in / 10% out replaces the old symmetric 5%/5%. The direction matters more than
// the arithmetic: an entry fee bills a developer at the moment they fund an agent that
// has not yet won anything, which is the worst moment to charge and the one most likely
// to end the trial. Both remain operator-tunable over the config bus; these are the
// fallbacks a deployment gets with nothing set.
func TestCoinRoundTripIsChargedOnTheWayOutOnly(t *testing.T) {
	baseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DepositFeePct != 0 {
		t.Errorf("DepositFeePct default = %d, want 0 — funding an agent must be free", cfg.DepositFeePct)
	}
	if cfg.WithdrawSellFeePct != 10 {
		t.Errorf("WithdrawSellFeePct default = %d, want 10 — the single fee on the round trip",
			cfg.WithdrawSellFeePct)
	}
}

// Both stay operator-owned: an explicit env value wins, including an explicit 0 on the
// way out (a fee-free promotion) and an explicit fee on the way in.
func TestFeesRemainOperatorConfigurable(t *testing.T) {
	baseEnv(t)
	t.Setenv("DEPOSIT_FEE_PCT", "3")
	t.Setenv("WITHDRAW_SELL_FEE_PCT", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DepositFeePct != 3 {
		t.Errorf("DEPOSIT_FEE_PCT=3 ⇒ %d, want 3", cfg.DepositFeePct)
	}
	if cfg.WithdrawSellFeePct != 0 {
		t.Errorf("WITHDRAW_SELL_FEE_PCT=0 ⇒ %d, want 0 (an explicit zero is a real setting)",
			cfg.WithdrawSellFeePct)
	}
}

// The developer trace view reads the Lens on a different secret from the one the
// emitter writes with. Defaulting the read key to the ingest key keeps a single-key
// deployment working; an explicit read key must win, because on the Lens's own
// production compose the two are required to differ.
func TestLensQueryKeyDefaultsToIngestKeyButCanDiffer(t *testing.T) {
	baseEnv(t)
	t.Setenv("PYYOL_LENS_API_KEY", "ingest-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PyyolLensQueryAPIKey != "ingest-secret" {
		t.Errorf("query key = %q, want the ingest key as fallback", cfg.PyyolLensQueryAPIKey)
	}

	t.Setenv("PYYOL_LENS_QUERY_API_KEY", "read-secret")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PyyolLensQueryAPIKey != "read-secret" {
		t.Errorf("query key = %q, want the explicit read key", cfg.PyyolLensQueryAPIKey)
	}
	if cfg.PyyolLensAPIKey != "ingest-secret" {
		t.Errorf("ingest key = %q, must be unaffected by the read key", cfg.PyyolLensAPIKey)
	}
}

// A read endpoint with no read key produces a permanently unavailable trace view, and
// nothing in the deployment looks wrong. It has to be said at boot.
func TestQueryEndpointWithoutAKeyWarnsAtBoot(t *testing.T) {
	baseEnv(t)
	t.Setenv("PYYOL_LENS_QUERY_ENDPOINT", "http://pyyol-lens-query:8082")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	found := false
	for _, w := range cfg.Warnings {
		if strings.Contains(w, "PYYOL_LENS_QUERY_API_KEY") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no boot warning about the missing trace read key; warnings = %v", cfg.Warnings)
	}
}
