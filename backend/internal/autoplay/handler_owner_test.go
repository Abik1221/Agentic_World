package autoplay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-arena/arena/internal/auth"
)

func TestListOwnerReturnsOnlyCallerAgents(t *testing.T) {
	repo := NewMemRepo()
	ctx := context.Background()
	if err := repo.Set(ctx, Setting{AgentPublicID: "ag_on", OwnerPublicID: "usr_1", Enabled: true, Mode: ModeSandbox}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, Setting{AgentPublicID: "ag_off", OwnerPublicID: "usr_1", Enabled: false, Mode: ModeRanked}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, Setting{AgentPublicID: "ag_other", OwnerPublicID: "usr_2", Enabled: true, Mode: ModeSandbox}); err != nil {
		t.Fatal(err)
	}

	h := &Handler{repo: repo}
	req := httptest.NewRequest(http.MethodGet, "/v1/user/autoplay", nil)
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		Scope: auth.ScopeUser, UserPublicID: "usr_1",
	}))
	rec := httptest.NewRecorder()
	h.listOwner(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.Bytes())
	}
	var out struct {
		Agents []Setting `json:"agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Agents) != 2 {
		t.Fatalf("got %d agents, want 2 (the other owner's row must stay private): %+v", len(out.Agents), out.Agents)
	}
	byID := map[string]Setting{}
	for _, s := range out.Agents {
		byID[s.AgentPublicID] = s
	}
	if !byID["ag_on"].Enabled || byID["ag_off"].Enabled {
		t.Fatalf("enabled flags wrong: %+v", byID)
	}
	if _, ok := byID["ag_other"]; ok {
		t.Fatalf("leaked another owner's autoplay: %+v", out.Agents)
	}
}
