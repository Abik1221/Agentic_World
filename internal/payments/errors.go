package payments

import (
	"net/http"

	"github.com/agent-arena/arena/internal/httpx"
)

var (
	ErrBadSignature     = httpx.NewError(http.StatusBadRequest, "bad_signature", "Webhook signature verification failed.")
	ErrSignatureExpired = httpx.NewError(http.StatusBadRequest, "signature_expired", "Webhook timestamp is outside the tolerance window.")
	ErrUnknownPack      = httpx.NewError(http.StatusBadRequest, "unknown_pack", "Unknown coin pack.")
	ErrForbiddenAgent   = httpx.NewError(http.StatusForbidden, "forbidden", "You do not own this agent.")
	ErrNotConfigured    = httpx.NewError(http.StatusServiceUnavailable, "payments_unconfigured", "Payments are not configured.")
)
