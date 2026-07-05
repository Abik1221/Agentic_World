package httpx

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const maxBodyBytes = 1 << 20 // 1 MiB — generous for our small JSON requests

// DecodeJSON strictly decodes a JSON request body into dst: it caps the body
// size, rejects unknown fields, and rejects trailing garbage. On any problem it
// returns a 400 *APIError suitable to hand straight to Error.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			return NewError(http.StatusRequestEntityTooLarge, "payload_too_large", "Request body is too large.")
		}
		return NewError(http.StatusBadRequest, "invalid_request", "Request body is not valid JSON or contains unknown fields.")
	}
	// Ensure there is exactly one JSON value.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return NewError(http.StatusBadRequest, "invalid_request", "Request body must contain a single JSON object.")
	}
	return nil
}
