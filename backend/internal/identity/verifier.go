package identity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// ClaimVerifier confirms that the human owner publicly posted the claim token
// (e.g. a tweet) and returns their stable X identity. Implementations are
// injected so the platform never couples to a specific social provider.
//
//   - Returns (xUserID, xHandle, nil) once the post is found.
//   - Returns ErrClaimNotVerified while no matching post exists yet (agents poll).
type ClaimVerifier interface {
	VerifyClaim(ctx context.Context, token string) (xUserID, xHandle string, err error)
}

// DevClaimVerifier auto-verifies offline (ENV=local or no X credentials). It
// derives a deterministic, unique X identity from the token so the full
// onboarding flow works end-to-end without external calls. NEVER used in prod.
type DevClaimVerifier struct{}

func (DevClaimVerifier) VerifyClaim(_ context.Context, token string) (string, string, error) {
	sum := sha256.Sum256([]byte(token))
	short := hex.EncodeToString(sum[:6])
	return "xdev_" + short, "dev_" + short[:6], nil
}

// xClaimVerifier is the production verifier that searches the user's recent posts
// for the claim token via the X API. It is intentionally a thin seam: the exact
// search endpoint/tier is wired when X credentials are provisioned. Until then
// main selects DevClaimVerifier. Keeping the interface here means swapping in the
// real implementation touches no call sites.
//
// type xClaimVerifier struct { bearer string; http *http.Client }
// func (v xClaimVerifier) VerifyClaim(ctx, token) (string, string, error) { … }
