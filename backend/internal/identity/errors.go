package identity

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

// Domain errors are expressed as *httpx.APIError so handlers can return them
// directly and clients get the uniform envelope. httpx is a low-level shared
// package (it does not import identity), so there is no cycle.
var (
	ErrNotFound           = httpx.ErrNotFound
	ErrClaimExpired       = httpx.NewError(http.StatusGone, "claim_expired", "This claim token has expired. Start registration again.")
	ErrClaimNotVerified   = httpx.NewError(http.StatusAccepted, "claim_pending", "The claim tweet has not been found yet. Post it, then poll again.")
	ErrCaptchaFailed      = httpx.NewError(http.StatusForbidden, "captcha_failed", "Captcha verification failed.")
	ErrInvalidAPIKey      = httpx.NewError(http.StatusUnauthorized, "unauthenticated", "Invalid API key.")
	ErrForbiddenOwner     = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this agent.")
	ErrInvalidPubKey      = httpx.NewError(http.StatusBadRequest, "invalid_pubkey", "Signing key must be a base64-encoded Ed25519 public key.")
	ErrEmailTaken         = httpx.NewError(http.StatusConflict, "email_taken", "That email is already registered. Try signing in instead.")
	ErrInvalidCredentials = httpx.NewError(http.StatusUnauthorized, "invalid_credentials", "Incorrect email or password.")
	ErrInvalidMagicLink   = httpx.NewError(http.StatusUnauthorized, "invalid_magic_link", "This sign-in link is invalid, expired, or already used.")
)

func errInvalid(msg string) *httpx.APIError {
	return httpx.NewError(http.StatusBadRequest, "invalid_request", msg)
}
