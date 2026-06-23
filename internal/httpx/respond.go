package httpx

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// JSON writes v as a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httpx: encode response failed", "error", err)
	}
}

// envelope is the wire shape for errors: {"error": {...}}.
type envelope struct {
	Error *APIError `json:"error"`
}

// Error renders any error as the uniform error envelope. Known *APIError values
// pass through with their status/code; anything else becomes a 500 with a stable
// "internal" code so internals never leak to clients.
func Error(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = ErrInternal
	}
	JSON(w, apiErr.Status, envelope{Error: apiErr})
}
