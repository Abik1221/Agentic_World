package integration

import (
	"testing"

	"github.com/agent-arena/arena/internal/auth"
)

// e2ePublicKey must equal PLATFORM_ADMIN_PUBLIC_KEY in .github/workflows/backend-e2e.yml.
// If the two drift apart, every admin-guarded e2e call fails with a 403 that looks
// like a broken guard rather than a mismatched key — so pin it here.
const e2ePublicKey = "hEs2p9t8OVVv1nEXwfpd7gNJySwFBs/pZ4Sybn+BOwY="

func TestHarnessPlatformTokenIsAcceptedByTheRealVerifier(t *testing.T) {
	v, err := auth.NewPlatformVerifier(e2ePublicKey)
	if err != nil {
		t.Fatalf("public key does not parse: %v", err)
	}
	if v == nil {
		t.Fatal("verifier disabled — the key was empty")
	}
	p, err := v.Verify(platformToken())
	if err != nil {
		t.Fatalf("the harness token is not accepted by the server's own verifier: %v", err)
	}
	if p.Scope != auth.ScopePlatform {
		t.Fatalf("scope = %q, want %q", p.Scope, auth.ScopePlatform)
	}
	// This is what makes the token usable on /v1/admin/mint at all.
	if !auth.IsAdmin(p, nil) {
		t.Fatal("a platform principal must satisfy IsAdmin with no allowlist")
	}
}

// A developer's own credential must NOT reach mint — that is the whole reason the
// guard exists, and a regression here would silently let anyone credit themselves.
func TestDeveloperPrincipalIsNotAdmin(t *testing.T) {
	dev := &auth.Principal{Scope: auth.ScopeUser, UserPublicID: "usr_selfserve"}
	if auth.IsAdmin(dev, nil) {
		t.Fatal("a self-registered developer must never be admin")
	}
	if auth.IsAdmin(dev, map[string]bool{"usr_other": true}) {
		t.Fatal("a developer not on the allowlist must not be admin")
	}
}
