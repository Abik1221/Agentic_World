package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/health"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// fakeChecker lets us drive readiness outcomes deterministically.
type fakeChecker struct {
	pingErr error
	applied bool
	appErr  error
}

func (f fakeChecker) Ping(context.Context) error                      { return f.pingErr }
func (f fakeChecker) MigrationsApplied(context.Context) (bool, error) { return f.applied, f.appErr }

func newServer(c health.Checker) http.Handler {
	clk := platform.FixedClock{T: time.Unix(1_700_000_000, 0).UTC()}
	h := health.New(c, clk, "test-1.0")
	r := chi.NewRouter()
	h.Register(r)
	return r
}

func TestLiveAlwaysOK(t *testing.T) {
	srv := newServer(fakeChecker{})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", rr.Code)
	}
}

func TestReady(t *testing.T) {
	tests := []struct {
		name  string
		check fakeChecker
		want  int
	}{
		{"ready", fakeChecker{applied: true}, http.StatusOK},
		{"deps down", fakeChecker{pingErr: errors.New("pg down")}, http.StatusServiceUnavailable},
		{"not migrated", fakeChecker{applied: false}, http.StatusServiceUnavailable},
		{"migrate check error", fakeChecker{appErr: errors.New("boom")}, http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(tt.check)
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if rr.Code != tt.want {
				t.Fatalf("/readyz = %d, want %d (body=%s)", rr.Code, tt.want, rr.Body.String())
			}
		})
	}
}

func TestPingReportsVersion(t *testing.T) {
	srv := newServer(fakeChecker{applied: true})
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/ping", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("/v1/ping = %d, want 200", rr.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["message"] != "pong" || body["version"] != "test-1.0" {
		t.Fatalf("unexpected ping body: %v", body)
	}
}
