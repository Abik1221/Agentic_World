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

// Key-management errors. Kept in their own block because their messages are too long
// for one line, and mixing multi-line entries into the aligned block above would make
// gofmt re-align every line in it.
var (
	// ErrRevokedAPIKey is returned ONLY when the presented secret verifies against a
	// revoked key — i.e. the caller genuinely held this credential and it was turned
	// off. That precondition is what keeps it from being an oracle: a guesser cannot
	// reach this branch without already knowing the secret, so naming the cause tells
	// an attacker nothing while telling the rightful owner exactly what happened (the
	// alternative was a bare "invalid API key" after a revoke somewhere else).
	//
	// The `key_revoked` code is also what the agent gateway forwards to the SDK, which
	// stops it from refreshing around a dead key and silently masking the problem.
	ErrRevokedAPIKey = httpx.NewError(http.StatusUnauthorized, "key_revoked",
		"This API key was revoked. Run `pyyol login` again, or issue a new key from the dashboard (Security → Agent API keys).")

	// ErrTooManyKeys guards MaxLiveKeysPerAgent. Reaching it means genuinely distinct
	// machines, since re-issuing for the same machine replaces rather than adds.
	ErrTooManyKeys = httpx.NewError(http.StatusConflict, "too_many_keys",
		"This agent already has the maximum number of active keys. Revoke one you no longer use from the dashboard (Security → Agent API keys).")
)

func errInvalid(msg string) *httpx.APIError {
	return httpx.NewError(http.StatusBadRequest, "invalid_request", msg)
}
