// Package middleware provides the composable HTTP middleware chain: request IDs,
// structured logging, panic recovery, CORS, and RED metrics. Auth and rate-limit
// middleware are added in later stages and slot into the same chain.
package middleware

import "net/http"

// statusWriter wraps http.ResponseWriter to capture the status code and byte
// count for logging/metrics. Unwrap keeps it compatible with http.ResponseController
// so later features (SSE flushing, hijacking) work transparently.
type statusWriter struct {
	http.ResponseWriter
	status  int
	written int
	wrote   bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status = code
		w.wrote = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += n
	return n, err
}

func (w *statusWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// Unwrap exposes the underlying writer for http.NewResponseController.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
