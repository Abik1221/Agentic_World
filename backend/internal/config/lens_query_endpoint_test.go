package config

import (
	"strings"
	"testing"
)

// A stack configured the documented way must be able to READ its own traces.
//
// PYYOL_LENS_QUERY_ENDPOINT had no fallback while the query KEY did, so setting only the
// ingest endpoint + key — which is what every deployment doc asks for — shipped telemetry
// and disabled the read path. The developer trace view then reported "traces are not
// enabled in this environment" on a deployment whose trace store was up and receiving,
// and nothing in the configuration looked wrong.
func TestLensQueryEndpointDerivedFromIngest(t *testing.T) {
	baseEnv(t)
	t.Setenv("PYYOL_LENS_ENDPOINT", "http://pyyol-lens-ingest:8081")
	t.Setenv("PYYOL_LENS_API_KEY", "one-secret")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PyyolLensQueryEndpoint != "http://pyyol-lens-ingest:8082" {
		t.Fatalf("query endpoint = %q, want the ingest host on the query port", cfg.PyyolLensQueryEndpoint)
	}
	// Derived, never silent: an operator has to be able to see where reads went.
	if !hasWarning(cfg.Warnings, "derived") {
		t.Fatalf("derivation must be warned about, got warnings %v", cfg.Warnings)
	}
}

// An explicit setting always wins — the derivation is a fallback, not an override.
func TestLensQueryEndpointExplicitWins(t *testing.T) {
	baseEnv(t)
	t.Setenv("PYYOL_LENS_ENDPOINT", "http://pyyol-lens-ingest:8081")
	t.Setenv("PYYOL_LENS_QUERY_ENDPOINT", "https://tel-query.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PyyolLensQueryEndpoint != "https://tel-query.example.com" {
		t.Fatalf("query endpoint = %q, want the explicit value", cfg.PyyolLensQueryEndpoint)
	}
}

// An ingest endpoint on a non-standard port is NOT guessed at. Pointing reads at the
// wrong service would make the arena report "unreachable" about a host that answers
// fine — a worse outcome than the feature staying off and saying so.
func TestLensQueryEndpointNotGuessedFromUnknownPort(t *testing.T) {
	baseEnv(t)
	t.Setenv("PYYOL_LENS_ENDPOINT", "https://telemetry.example.com")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PyyolLensQueryEndpoint != "" {
		t.Fatalf("query endpoint = %q, want empty rather than a guess", cfg.PyyolLensQueryEndpoint)
	}
	if !hasWarning(cfg.Warnings, "could not be derived") {
		t.Fatalf("an undecidable derivation must say so, got warnings %v", cfg.Warnings)
	}
}

// No Lens at all stays quiet: a deployment that never wanted telemetry should not be
// warned about telemetry it is not using.
func TestNoLensConfiguredProducesNoTraceWarning(t *testing.T) {
	baseEnv(t)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PyyolLensQueryEndpoint != "" {
		t.Fatalf("query endpoint = %q, want empty", cfg.PyyolLensQueryEndpoint)
	}
	if hasWarning(cfg.Warnings, "PYYOL_LENS_QUERY_ENDPOINT") {
		t.Fatalf("unconfigured telemetry must not warn, got %v", cfg.Warnings)
	}
}

func hasWarning(warnings []string, needle string) bool {
	for _, w := range warnings {
		if strings.Contains(w, needle) {
			return true
		}
	}
	return false
}
