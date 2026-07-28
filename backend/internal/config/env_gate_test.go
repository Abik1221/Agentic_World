package config

import (
	"strings"
	"testing"
)

// ENV is the switch every other safety gate hangs off, so these tests are really
// about ALLOW_MINT and the SSRF guards rather than about string validation.

func TestUnknownEnvIsRefused(t *testing.T) {
	// "production" is the natural spelling and the wrong one: IsProd() matches "prod"
	// exactly, so this value would have left a live deployment in permissive mode.
	for _, env := range []string{"production", "PROD", "prd", "live", "", "stage"} {
		c := &Config{Env: env, Port: 8080, LogLevel: "info", CoinCents: 1, DefaultRounds: 1}
		err := c.validate()
		if err == nil {
			t.Fatalf("ENV=%q was accepted; it must refuse to boot", env)
		}
		if !strings.Contains(err.Error(), "ENV invalid") {
			t.Fatalf("ENV=%q failed for an unrelated reason: %v", env, err)
		}
	}
}

func TestKnownEnvsAreAccepted(t *testing.T) {
	for _, env := range []string{"local", "dev", "test", "staging", "prod"} {
		c := &Config{Env: env, Port: 8080, LogLevel: "info", CoinCents: 1, DefaultRounds: 1}
		if err := c.validate(); err != nil && strings.Contains(err.Error(), "ENV invalid") {
			t.Fatalf("ENV=%q should be valid: %v", env, err)
		}
	}
}

// The property that actually matters: in a prod-like environment the mint endpoint
// is off no matter what the operator set, and everywhere else IsProd() is false so
// the permissive defaults are a deliberate choice rather than an accident.
func TestProdLikeEnvsAreRecognised(t *testing.T) {
	for _, env := range []string{"prod", "staging"} {
		c := &Config{Env: env}
		if !c.IsProd() {
			t.Fatalf("ENV=%q is not treated as prod-like; ALLOW_MINT would default ON", env)
		}
	}
	for _, env := range []string{"local", "dev", "test"} {
		c := &Config{Env: env}
		if c.IsProd() {
			t.Fatalf("ENV=%q was treated as prod-like", env)
		}
	}
}
