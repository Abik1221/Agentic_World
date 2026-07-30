package integration

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"time"
)

// e2ePlatformSeed derives the Ed25519 key the harness signs Platform tokens with.
// The matching public key is set as PLATFORM_ADMIN_PUBLIC_KEY on the e2e server:
//
//	hEs2p9t8OVVv1nEXwfpd7gNJySwFBs/pZ4Sybn+BOwY=
//
// Hard-coding a seed in a test is safe here and nowhere else: it authenticates
// ONLY against a server that was booted with this exact public key, which is the
// throwaway e2e instance. A real deployment carries the Super Admin's key, so this
// token is rejected outright.
//
// This exists because /v1/admin/mint is admin-guarded. Minting coins is the test
// stand-in for a settled top-up, and letting any self-registered developer call it
// would let them credit themselves — the guard is the point, so the harness has to
// authenticate properly rather than have the guard weakened for tests.
const e2ePlatformSeed = "pyyol-e2e-platform-token-seed!!!" // 32 bytes

// platformToken mints a short-lived Platform token accepted by RequirePlatformOrAdmin.
// Claims mirror what the Super Admin issues: iss/sub/iat/exp, with exp inside the
// server's max-age cap.
func platformToken() string {
	priv := ed25519.NewKeyFromSeed([]byte(e2ePlatformSeed))
	now := time.Now().Unix()
	payload, _ := json.Marshal(map[string]any{
		"iss": "super-admin",
		"sub": "e2e-harness",
		"iat": now,
		"exp": now + 300,
	})
	sig := ed25519.Sign(priv, payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig)
}
