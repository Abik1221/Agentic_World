// Package openapi serves the API contract: the raw OpenAPI 3.0 spec at
// /openapi.yaml and an interactive Swagger UI at /docs. The spec is embedded in
// the binary, so it ships and versions with the code. Both routes are public.
package openapi

import (
	_ "embed"
	"net/http"

	"github.com/go-chi/chi/v5"
)

//go:embed openapi.yaml
var spec []byte

// Handler serves the spec and the Swagger UI page.
type Handler struct{}

func NewHandler() *Handler { return &Handler{} }

// Register mounts /openapi.yaml and /docs (both public, no auth).
func (h *Handler) Register(r chi.Router) {
	r.Get("/openapi.yaml", h.serveSpec)
	r.Get("/docs", h.serveUI)
}

func (h *Handler) serveSpec(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(spec)
}

func (h *Handler) serveUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(swaggerHTML))
}

// swaggerHTML renders Swagger UI (assets from the public CDN) pointed at the
// embedded spec. Self-contained apart from the CDN script/style.
const swaggerHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <title>Agent Arena API — Reference</title>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist@5/swagger-ui.css"/>
  <style>body { margin: 0; } .topbar { display: none; }</style>
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist@5/swagger-ui-bundle.js" crossorigin></script>
  <script>
    window.onload = function () {
      window.ui = SwaggerUIBundle({
        url: '/openapi.yaml',
        dom_id: '#swagger-ui',
        deepLinking: true,
        persistAuthorization: true,
        tryItOutEnabled: true,
      });
    };
  </script>
</body>
</html>`
