// Package httpx wires the HTTP server: router, middleware chain, the uniform
// response/error envelope, and graceful lifecycle. Handlers return domain results
// or an *APIError; httpx is the single place that renders the wire format.
package httpx

import "net/http"

// APIError is the canonical error returned to clients. It maps a stable machine
// code to an HTTP status and an optional details payload. See
// docs/architecture/api-surface.md for the full code catalogue.
type APIError struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

// WithDetails returns a copy of the error carrying structured details.
func (e *APIError) WithDetails(d map[string]any) *APIError {
	cp := *e
	cp.Details = d
	return &cp
}

// Constructors for the common cases. Domain modules define their own as needed.
func NewError(status int, code, msg string) *APIError {
	return &APIError{Status: status, Code: code, Message: msg}
}

var (
	ErrBadRequest    = NewError(http.StatusBadRequest, "invalid_request", "The request was invalid.")
	ErrUnauthorized  = NewError(http.StatusUnauthorized, "unauthenticated", "Authentication is required.")
	ErrForbidden     = NewError(http.StatusForbidden, "forbidden", "You do not have access to this resource.")
	ErrNotFound      = NewError(http.StatusNotFound, "not_found", "Resource not found.")
	ErrRateLimited   = NewError(http.StatusTooManyRequests, "rate_limited", "Too many requests.")
	ErrInternal      = NewError(http.StatusInternalServerError, "internal", "An unexpected error occurred.")
)
