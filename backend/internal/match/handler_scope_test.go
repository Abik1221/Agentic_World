package match_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/match"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// Rooms used to require an agent API key. A signed-in dashboard therefore posted
// a user JWT, the scope guard answered 401 "Authentication is required.", and
// the owner of the funded sitting agent looked logged out. These tests go
// through Register() so dropping the owner admission from the route table fails
// here, not only in a helper.

type stubKeys struct{ ownerID, agentID string }

func (s stubKeys) ResolveAgentKey(_ context.Context, _ string) (*auth.Principal, error) {
	return &auth.Principal{Scope: auth.ScopeAgent, UserPublicID: s.ownerID, AgentPublicID: s.agentID}, nil
}

type stubOwners struct{ agent string }

func (s stubOwners) PrimaryAgentOf(context.Context, string) (string, error) {
	return s.agent, nil
}

const testSigningKey = "test-signing-key-for-room-scope"

func roomRouter(t *testing.T, owners matchPrimary) (chi.Router, string, string) {
	t.Helper()
	jwt := auth.NewJWT(testSigningKey, time.Hour)
	authn := auth.NewAuthenticator(
		stubKeys{ownerID: "usr_a", agentID: "ag_a"},
		jwt,
		nil,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	h := match.NewHandler(newSvc(), authn)
	if owners != nil {
		h.SetPrimaryAgentLookup(owners)
	}
	r := chi.NewRouter()
	h.Register(r)
	userToken, err := jwt.Issue("usr_a")
	if err != nil {
		t.Fatalf("issue user token: %v", err)
	}
	return r, userToken, platform.PrefixKey + "_live_room"
}

// matchPrimary is the lookup the handler accepts. Declared here so the test
// can pass nil or a stub without importing the unexported interface.
type matchPrimary interface {
	PrimaryAgentOf(ctx context.Context, ownerPublicID string) (string, error)
}

func postJSON(t *testing.T, r chi.Router, path, credential, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if credential != "" {
		req.Header.Set("Authorization", "Bearer "+credential)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestRoomCreateAdmitsADashboardJWTAndSitsTheOwnersAgent(t *testing.T) {
	r, userToken, _ := roomRouter(t, stubOwners{agent: "ag_owned"})
	rec := postJSON(t, r, "/v1/room/create", userToken, `{"bid":50}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("dashboard JWT create = %d %s, want 201. A signed-in owner whose agent holds the stake must be able to open a room.", rec.Code, rec.Body.String())
	}
	var out struct {
		RoomID  string `json:"room_id"`
		MatchID string `json:"match_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RoomID == "" || out.MatchID == "" {
		t.Fatalf("create returned no room id: %s", rec.Body.String())
	}
}

func TestRoomCreateStillAdmitsAnAgentKey(t *testing.T) {
	r, _, agentKey := roomRouter(t, stubOwners{agent: "ag_owned"})
	rec := postJSON(t, r, "/v1/room/create", agentKey, `{"bid":50}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("agent key create = %d %s, want 201", rec.Code, rec.Body.String())
	}
}

func TestRoomCreateRejectsAnonymousWithAuthenticationRequired(t *testing.T) {
	r, _, _ := roomRouter(t, stubOwners{agent: "ag_owned"})
	rec := postJSON(t, r, "/v1/room/create", "", `{"bid":50}`)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous create = %d, want 401", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("Authentication is required.")) {
		t.Fatalf("anonymous body = %s, want the unauthenticated banner the dashboard was showing by mistake", rec.Body.String())
	}
}

func TestLobbyJoinAdmitsADashboardJWT(t *testing.T) {
	r, userToken, agentKey := roomRouter(t, stubOwners{agent: "ag_b"})
	created := postJSON(t, r, "/v1/room/create", agentKey, `{"bid":50}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("setup create = %d %s", created.Code, created.Body.String())
	}
	var out struct {
		MatchID string `json:"match_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// A different owner sits via JWT — same-owner is refused, so the stub agent
	// must not be the creator. The agent key created as ag_a / usr_a; JWT is
	// usr_a too. Issue a second user token? The router JWT is usr_a. Joining
	// as the same owner must be ErrSameOwner (409), which still proves the
	// guard admitted the JWT instead of answering 401.
	rec := postJSON(t, r, "/v1/lobby/join", userToken, `{"match_id":"`+out.MatchID+`"}`)
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("dashboard JWT join = %d %s — the owner was turned away as if they were logged out", rec.Code, rec.Body.String())
	}
}

func TestReadyStillRejectsADashboardJWT(t *testing.T) {
	r, userToken, _ := roomRouter(t, stubOwners{agent: "ag_owned"})
	req := httptest.NewRequest(http.MethodPost, "/v1/match/mt_x/ready", nil)
	req.Header.Set("Authorization", "Bearer "+userToken)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dashboard JWT ready = %d, want 403. Playing stays agent-scoped.", rec.Code)
	}
}
