package httpx

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/config"
	"github.com/agent-arena/arena/internal/platform"
)

// /metrics used to be world-readable, publishing the whole route table (every
// /v1/admin path) plus coins_staked_total and fraud_flags_total. These pin the
// three states so it cannot silently reopen.
func metricsReq(t *testing.T, env, token, authHeader string) *httptest.ResponseRecorder {
	t.Helper()
	h := NewRouter(Deps{
		Config:  &config.Config{Env: env, MetricsToken: token},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics: platform.NewMetrics(),
	})
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestMetricsClosedInProdWithoutToken(t *testing.T) {
	rec := metricsReq(t, "prod", "", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("prod without token: got %d, want 404", rec.Code)
	}
	// 404 and not 401: a 401 confirms the endpoint exists.
	if strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Fatal("prod without token leaked the exposition body")
	}
}

func TestMetricsRequiresBearerWhenTokenSet(t *testing.T) {
	const tok = "s3cret-metrics-token"

	if rec := metricsReq(t, "prod", tok, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("no header: got %d, want 404", rec.Code)
	}
	if rec := metricsReq(t, "prod", tok, "Bearer wrong-token"); rec.Code != http.StatusNotFound {
		t.Fatalf("wrong token: got %d, want 404", rec.Code)
	}
	// A prefix of the real token must not pass — guards against a length-only or
	// prefix comparison.
	if rec := metricsReq(t, "prod", tok, "Bearer "+tok[:5]); rec.Code != http.StatusNotFound {
		t.Fatalf("token prefix: got %d, want 404", rec.Code)
	}
	rec := metricsReq(t, "prod", tok, "Bearer "+tok)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct token: got %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "go_goroutines") {
		t.Fatal("correct token did not return the exposition")
	}
}

func TestMetricsOpenOutsideProdForLocalDebugging(t *testing.T) {
	rec := metricsReq(t, "dev", "", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("dev without token: got %d, want 200", rec.Code)
	}
}

// staging counts as prod — it holds real-shaped data.
func TestMetricsClosedInStaging(t *testing.T) {
	if rec := metricsReq(t, "staging", "", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("staging without token: got %d, want 404", rec.Code)
	}
}
