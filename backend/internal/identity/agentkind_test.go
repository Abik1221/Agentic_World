package identity

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/platform"
	"github.com/go-chi/chi/v5"
)

// The agent KIND is the platform's answer to "whose result is this", and every public
// surface is filtered on it. These tests pin the three properties that make it trustworthy:
//
//  1. an ordinary sign-up cannot choose it,
//  2. an admin caller can, and only for kinds that are safe to mint,
//  3. it is decided at CREATION, because relabelling later moves no matches.
//
// The bug they exist to prevent is not hypothetical. gamelab created its benchmark agents
// through the public sign-up, so they were `external`, so a platform benchmark run landed
// on the public developer leaderboard, rated, while /harness stayed empty.

// kindCapturingRepo records the CreateAccountInput the service builds. CreateAccount and
// SetSignupCountry (best-effort on successful HTTP signup) are implemented; the embedded
// nil Repo makes any OTHER call panic, so a test cannot pass by accidentally exercising a
// different path.
type kindCapturingRepo struct {
	Repo
	got CreateAccountInput
}

func (r *kindCapturingRepo) CreateAccount(_ context.Context, in CreateAccountInput) (Agent, User, error) {
	r.got = in
	return Agent{PublicID: in.AgentPublicID, Name: in.AgentName},
		User{PublicID: in.UserPublicID}, nil
}

func (r *kindCapturingRepo) SetSignupCountry(_ context.Context, _, _ string) error {
	return nil
}

func newKindTestService(repo Repo) *Service {
	return New(repo, nil, nil, auth.NewJWT(kindTestSigningKey, time.Hour),
		platform.NewClock(), "test-pepper-at-least-32-bytes-long-ok!!", time.Hour)
}

const kindTestSigningKey = "test-signing-key-at-least-32-bytes-long!!" // #nosec G101 -- test-only

// newKindTestHandler builds the handler the way production does, so the rate-limit
// passthroughs and every other default are the real ones rather than a test's zero values.
func newKindTestHandler(repo Repo) *Handler {
	authn := auth.NewAuthenticator(nil, auth.NewJWT(kindTestSigningKey, time.Hour), nil, slog.Default())
	return NewHandler(newKindTestService(repo), authn, nil, nil, nil, true, false, false)
}

func TestSignUpAlwaysCreatesAnExternalAgent(t *testing.T) {
	repo := &kindCapturingRepo{}
	svc := newKindTestService(repo)

	if _, err := svc.SignUp(context.Background(), "dev@example.com", "a-strong-lab-passphrase-2026",
		"dev-agent", "a developer's agent"); err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if repo.got.Kind != KindExternal {
		t.Fatalf("the PUBLIC sign-up must create %q agents, got %q — a public path that can "+
			"produce any other kind lets a developer decide which board rates them",
			KindExternal, repo.got.Kind)
	}
	// The guardrails a developer gets must not quietly become the harness ones.
	if repo.got.Limits.MaxConcurrentMatches != DefaultLimits().MaxConcurrentMatches {
		t.Fatalf("a developer agent must be created with the default throughput limit %d, got %d",
			DefaultLimits().MaxConcurrentMatches, repo.got.Limits.MaxConcurrentMatches)
	}
}

func TestSignUpDoesNotMintAnInitialKey(t *testing.T) {
	repo := &kindCapturingRepo{}
	svc := newKindTestService(repo)

	res, err := svc.SignUp(context.Background(), "dev@example.com", "a-strong-lab-passphrase-2026",
		"dev-agent", "")
	if err != nil {
		t.Fatalf("SignUp: %v", err)
	}
	if res.APIKey != "" || repo.got.KeyPrefix != "" || repo.got.KeyHash != "" {
		t.Fatalf("developer signup must not mint an unused initial key: api_key=%q prefix=%q",
			res.APIKey, repo.got.KeyPrefix)
	}
	if res.DashboardToken == "" || res.AgentID == "" {
		t.Fatal("signup must still return a dashboard session and agent")
	}
}

func TestSignUpAsStillMintsAPlayKey(t *testing.T) {
	repo := &kindCapturingRepo{}
	svc := newKindTestService(repo)

	res, err := svc.SignUpAs(context.Background(), "harness@pyyol.test", "a-strong-lab-passphrase-2026",
		"harness-agent", "platform benchmark seat", KindHarness)
	if err != nil {
		t.Fatalf("SignUpAs: %v", err)
	}
	if res.APIKey == "" || repo.got.KeyPrefix == "" || repo.got.KeyHash == "" {
		t.Fatal("admin-created harness seats still need a play key")
	}
}

func TestSignUpAsCreatesTheRequestedKind(t *testing.T) {
	repo := &kindCapturingRepo{}
	svc := newKindTestService(repo)

	if _, err := svc.SignUpAs(context.Background(), "harness@pyyol.test", "a-strong-lab-passphrase-2026",
		"harness-agent", "platform benchmark seat", KindHarness); err != nil {
		t.Fatalf("SignUpAs: %v", err)
	}
	if repo.got.Kind != KindHarness {
		t.Fatalf("SignUpAs(%q) must reach the repo as %q, got %q — if the kind is dropped here "+
			"the agent is created external and the run lands on the developer board",
			KindHarness, KindHarness, repo.got.Kind)
	}
}

// The kind must be refused for anything the platform is not prepared to mint. House is the
// case that matters: its certification exemption comes from an explicit boot-time id list,
// never from this column, so an API-minted house agent would be house-shaped to every query
// while holding no exemption.
func TestSignUpAsRefusesKindsThatMustNotBeMinted(t *testing.T) {
	for _, kind := range []string{KindHouse, "", "External", "admin", "harness "} {
		t.Run("kind="+kind, func(t *testing.T) {
			repo := &kindCapturingRepo{}
			svc := newKindTestService(repo)
			if _, err := svc.SignUpAs(context.Background(), "x@pyyol.test",
				"a-strong-lab-passphrase-2026", "some-agent", "", kind); err == nil {
				t.Fatalf("SignUpAs(%q) must be refused", kind)
			}
			if repo.got.AgentPublicID != "" {
				t.Fatal("a refused kind must not reach CreateAccount at all")
			}
		})
	}
}

// A harness agent differs from a developer's in exactly ONE guardrail. Pinned as a pair,
// because the risk runs both ways: a harness agent stuck at the developer throughput limit
// cannot complete a batch (this is what capped `-matches 14` at 4), and a harness agent
// handed loosened MONEY limits would stop playing under the constraints the benchmark
// claims to measure under.
func TestHarnessLimitsDifferOnlyInThroughput(t *testing.T) {
	def, har := DefaultLimits(), HarnessLimits()

	if har.MaxConcurrentMatches <= def.MaxConcurrentMatches {
		t.Fatalf("harness throughput %d must exceed the developer default %d, or a batch "+
			"stalls the moment one match has not finished finalizing",
			har.MaxConcurrentMatches, def.MaxConcurrentMatches)
	}
	// Enough headroom for a whole run's worth of slots that have not been given back —
	// a stalled match holds its slot until the liveness worker forfeits it.
	if har.MaxConcurrentMatches < 50 {
		t.Fatalf("harness throughput %d is below the 30–50 matches per pairing a rankable "+
			"interval needs; leaked slots would exhaust it mid-run", har.MaxConcurrentMatches)
	}
	// Everything else identical, field by field, so a future edit to DefaultLimits cannot
	// silently give the harness different economics.
	har.MaxConcurrentMatches = def.MaxConcurrentMatches
	if har != def {
		t.Fatalf("harness limits must differ from the developer defaults ONLY in throughput:\n"+
			" default = %+v\n harness = %+v", def, har)
	}
	if err := HarnessLimits().Validate(); err != nil {
		t.Fatalf("harness limits must be storable: %v", err)
	}
	if LimitsForKind(KindHarness) != HarnessLimits() || LimitsForKind(KindExternal) != DefaultLimits() {
		t.Fatal("LimitsForKind must route each kind to its own guardrails")
	}
}

// The route, not the service, is where authority is decided — so the route is what gets
// tested for it. An unauthenticated or non-admin caller must not reach the handler at all.
func TestAdminCreateAgentRouteIsAdminOnly(t *testing.T) {
	repo := &kindCapturingRepo{}
	h := newKindTestHandler(repo)
	h.SetAdmins(nil) // no allowlist: only a valid Platform token may pass
	r := chi.NewRouter()
	h.Register(r)

	body := strings.NewReader(`{"email":"h@pyyol.test","password":"a-strong-lab-passphrase-2026",` +
		`"agent_name":"harness-agent","kind":"harness"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/agents", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("POST /v1/admin/agents with no credential must be refused, got HTTP %d", rec.Code)
	}
	if repo.got.AgentPublicID != "" {
		t.Fatal("an unauthorized create must not reach CreateAccount")
	}
}

// The public sign-up must not gain a kind field by accident. httpx.DecodeJSON disallows
// unknown fields, so posting one is REFUSED outright rather than silently dropped — which is
// the stronger of the two behaviours, and the one worth pinning: a caller who tries gets a
// 400 and no account, instead of a 201 and an agent whose kind they have to go and check.
func TestPublicSignupRefusesAKindField(t *testing.T) {
	repo := &kindCapturingRepo{}
	h := newKindTestHandler(repo)
	r := chi.NewRouter()
	h.Register(r)

	body := strings.NewReader(`{"email":"sneaky@example.com","password":"a-strong-lab-passphrase-2026",` +
		`"agent_name":"sneaky-agent","kind":"harness"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/signup", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a kind posted to the PUBLIC sign-up must be refused, got HTTP %d: %s",
			rec.Code, rec.Body.String())
	}
	if repo.got.AgentPublicID != "" {
		t.Fatal("a sign-up carrying a kind must not create an account at all")
	}

	// And the ordinary body still works, so the guard above is about the extra field and
	// not about the endpoint having been broken.
	repo.got = CreateAccountInput{}
	ok := strings.NewReader(`{"email":"dev@example.com","password":"a-strong-lab-passphrase-2026",` +
		`"agent_name":"dev-agent"}`)
	req = httptest.NewRequest(http.MethodPost, "/v1/auth/signup", ok)
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("an ordinary public signup must still succeed, got HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if repo.got.Kind != KindExternal {
		t.Fatalf("the public sign-up must create %q agents, got %q", KindExternal, repo.got.Kind)
	}
}
