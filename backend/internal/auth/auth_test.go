package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequireScope(t *testing.T) {
	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	tests := []struct {
		name      string
		principal *Principal
		require   Scope
		wantCode  int
		wantBody  string // substring expected in body for the denied cases
	}{
		{"no principal", nil, ScopeUser, http.StatusUnauthorized, "unauthenticated"},
		{"agent on user route", &Principal{Scope: ScopeAgent}, ScopeUser, http.StatusForbidden, "agent_cannot_modify_limits"},
		{"user on user route", &Principal{Scope: ScopeUser, UserPublicID: "usr_1"}, ScopeUser, http.StatusOK, "ok"},
		{"agent on agent route", &Principal{Scope: ScopeAgent, AgentPublicID: "ag_1"}, ScopeAgent, http.StatusOK, "ok"},
		{"user on agent route", &Principal{Scope: ScopeUser}, ScopeAgent, http.StatusForbidden, "forbidden_scope"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := RequireScope(tt.require)(final)
			req := httptest.NewRequest(http.MethodPost, "/x", nil)
			if tt.principal != nil {
				req = req.WithContext(context.WithValue(req.Context(), principalKey, tt.principal))
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != tt.wantCode {
				t.Fatalf("code = %d, want %d", rr.Code, tt.wantCode)
			}
			if tt.wantBody != "" && !strings.Contains(rr.Body.String(), tt.wantBody) {
				t.Fatalf("body %q does not contain %q", rr.Body.String(), tt.wantBody)
			}
		})
	}
}
