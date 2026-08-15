package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// harness.go — running gamelab as the PLATFORM's benchmark rather than as a simulated
// developer.
//
// # What -harness changes, and what it deliberately does not
//
// It changes ONE thing: the account-creation call. With -harness the agents are created
// through POST /v1/admin/agents with kind='harness' instead of the public POST
// /v1/auth/signup, which creates kind='external'.
//
// Everything after that is byte-for-byte the developer flow — the same manifest submission,
// the same platform-side endpoint verification, the same agent-scope key, the same wallet
// funding through dev checkout and allocate, the same lobby and group queue, the same
// completion binding through the gateway, the same scaffold fingerprint. That is not
// laziness; it is the requirement. A benchmark that ran on its own private code path would
// be measuring the private code path, and the number it produced would not be comparable to
// anything a developer's agent does.
//
// # Why the kind cannot be applied afterwards
//
// The public sinks filter on an allowlist of kind='external' (32 sites). An agent that plays
// while still `external` has ALREADY had those matches attributed to the developer board and
// its ratings written; an UPDATE afterwards relabels the agent and moves none of it. So the
// kind has to be true at INSERT, which is why this is a create-time argument on an
// admin-guarded route rather than a repair step.

// HarnessMode makes onboarding create kind='harness' agents through the admin route.
var HarnessMode = false

// PlatformToken is the Super Admin credential the admin create route is authorized with.
// Minted per process from the seed below, short-lived, and never logged.
var PlatformToken = ""

// labPlatformSeed derives the Ed25519 key gamelab signs its Platform token with. The
// matching public key is what a lab server is booted with:
//
//	PLATFORM_ADMIN_PUBLIC_KEY=hEs2p9t8OVVv1nEXwfpd7gNJySwFBs/pZ4Sybn+BOwY=
//
// Hard-coding a seed is safe HERE and nowhere else, for exactly the reason it is safe in
// tests/integration/platform_token.go, which uses this same value: the token it produces
// authenticates ONLY against a server that was booted with this specific public key. That is
// the throwaway lab stack. A real deployment carries the Super Admin's own key, against
// which this token verifies as a forgery and is refused — which is the correct outcome,
// because gamelab has no business creating agents in production.
//
// PYYOL_PLATFORM_SEED overrides it, so an operator running gamelab against a stack with a
// different key supplies their own without a code change and without it landing in shell
// history as a flag.
const labPlatformSeed = "pyyol-e2e-platform-token-seed!!!" // #nosec G101 -- lab-only; see above

// mintPlatformToken produces the Super Admin token accepted by RequirePlatformOrAdmin.
//
// The claims mirror what the Super Admin issues (iss/sub/iat/exp) and the lifetime is short
// on purpose: it is used for a handful of account creations at the very start of a run and
// is worthless minutes later, so a token captured from the process environment mid-run buys
// nothing.
func mintPlatformToken() (string, error) {
	seed := envOr("PYYOL_PLATFORM_SEED", labPlatformSeed)
	if len(seed) != ed25519.SeedSize {
		return "", fmt.Errorf("PYYOL_PLATFORM_SEED must be exactly %d bytes, got %d",
			ed25519.SeedSize, len(seed))
	}
	priv := ed25519.NewKeyFromSeed([]byte(seed))
	now := time.Now().Unix()
	payload, err := json.Marshal(map[string]any{
		"iss": "super-admin",
		"sub": "gamelab-harness",
		"iat": now,
		"exp": now + 600,
	})
	if err != nil {
		return "", err
	}
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// createHarnessAccount creates one benchmark account through the admin route.
//
// Returns the same fields signup does, because the caller's onboarding continues on the
// ordinary developer path from here and must not need to know which route created it.
func (a *api) createHarnessAccount(platformToken string, body map[string]any, out any) error {
	body["kind"] = "harness"
	code, excerpt, err := a.doWithAuth(http.MethodPost, "/v1/admin/agents",
		"Platform "+platformToken, body, out)
	if err != nil {
		return fmt.Errorf("create harness agent: %w", err)
	}
	switch code {
	case http.StatusCreated, http.StatusOK:
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		// Worth its own message. The failure mode this replaces was SILENT — agents were
		// created as `external` and the run looked fine until the wrong leaderboard moved —
		// so a harness run that cannot authenticate must stop here rather than fall back to
		// the public signup and reproduce the exact bug -harness exists to fix.
		return fmt.Errorf("create harness agent: HTTP %d — the server did not accept this "+
			"Platform token. Its PLATFORM_ADMIN_PUBLIC_KEY must match PYYOL_PLATFORM_SEED. "+
			"NOT falling back to public signup: that would create external agents and put "+
			"this run on the developer leaderboard. — %s", code, excerpt)
	default:
		return fmt.Errorf("create harness agent: HTTP %d — %s", code, excerpt)
	}
}

// harnessEnabledFromEnv reports whether the process was told to run as the platform harness
// by environment rather than by flag, so a docker exec can set it alongside the provider
// keys it already passes that way.
func harnessEnabledFromEnv() bool { return os.Getenv("PYYOL_HARNESS") == "true" }
