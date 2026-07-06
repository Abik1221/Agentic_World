package manifest

import (
	"context"
	"errors"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/secretbox"
)

// --- fakes -----------------------------------------------------------------

type fakeRepo struct {
	owned       bool
	manifest    Manifest
	found       bool
	active      Manifest
	activeFound bool
	token       []byte
	attempts    []VerificationAttempt
	activated   bool
}

func (f *fakeRepo) AgentOwned(_ context.Context, _, _ string) (bool, error) { return f.owned, nil }
func (f *fakeRepo) InsertManifest(_ context.Context, _ Manifest, _ string, _ []byte) error {
	return nil
}
func (f *fakeRepo) ActiveManifest(_ context.Context, _ string) (Manifest, bool, error) {
	return f.active, f.activeFound, nil
}
func (f *fakeRepo) ActiveAgentIDs(_ context.Context) ([]string, error) {
	if f.activeFound {
		return []string{f.active.AgentPublicID}, nil
	}
	return nil, nil
}
func (f *fakeRepo) LatestManifest(_ context.Context, _ string) (Manifest, bool, error) {
	return f.manifest, f.found, nil
}
func (f *fakeRepo) ListVersions(_ context.Context, _ string) ([]Manifest, error) { return nil, nil }
func (f *fakeRepo) GetManifest(_ context.Context, _, _ string) (Manifest, bool, error) {
	return f.manifest, f.found, nil
}
func (f *fakeRepo) SetEndpointToken(_ context.Context, _, _ string, sealed []byte) error {
	f.token = sealed
	return nil
}
func (f *fakeRepo) EndpointToken(_ context.Context, _ string) ([]byte, bool, error) {
	return f.token, len(f.token) > 0, nil
}
func (f *fakeRepo) RecordVerification(_ context.Context, a VerificationAttempt) error {
	f.attempts = append(f.attempts, a)
	return nil
}
func (f *fakeRepo) MarkVerifiedAndActivate(_ context.Context, _, _ string) error {
	f.activated = true
	return nil
}

type fakeProbe struct {
	health    agentclient.HealthResult
	healthErr error
	hs        agentclient.HandshakeResult
	hsErr     error
	gotToken  string
}

func (p *fakeProbe) Health(_ context.Context, _ agentclient.Target) (agentclient.HealthResult, error) {
	return p.health, p.healthErr
}
func (p *fakeProbe) Handshake(_ context.Context, t agentclient.Target) (agentclient.HandshakeResult, error) {
	p.gotToken = t.Token
	return p.hs, p.hsErr
}

func baseManifest() Manifest {
	return Manifest{
		PublicID:      "man_1",
		AgentPublicID: "ag_1",
		EndpointURL:   "https://agent.example.com/play",
		AuthType:      "bearer-token",
		Games:         []string{"mafia", "goofspiel"},
		Status:        StatusValidated,
	}
}

func healthy() agentclient.HealthResult {
	return agentclient.HealthResult{OK: true, Status: 200, LatencyMs: 12}
}

// --- tests -----------------------------------------------------------------

func TestVerify_HappyPath(t *testing.T) {
	cipher, _ := secretbox.New("key")
	sealed, _ := cipher.Seal([]byte("tok"))
	repo := &fakeRepo{owned: true, manifest: baseManifest(), found: true, token: sealed}
	probe := &fakeProbe{
		health: healthy(),
		hs:     agentclient.HandshakeResult{OK: true, Accepted: true, SDKVersion: "1.0.0", SupportedGames: []string{"mafia", "goofspiel", "monopoly"}},
	}
	svc := New(repo, probe, cipher)

	rep, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Verified || !rep.HealthOK || !rep.HandshakeOK || !rep.GamesCovered {
		t.Fatalf("expected full verification, got %+v", rep)
	}
	if !repo.activated {
		t.Fatal("expected manifest to be activated")
	}
	if probe.gotToken != "tok" {
		t.Fatalf("expected decrypted token 'tok', got %q", probe.gotToken)
	}
	if len(repo.attempts) != 1 {
		t.Fatalf("expected 1 recorded attempt, got %d", len(repo.attempts))
	}
}

func TestVerify_HealthFails(t *testing.T) {
	cipher, _ := secretbox.New("key")
	sealed, _ := cipher.Seal([]byte("tok"))
	repo := &fakeRepo{owned: true, manifest: baseManifest(), found: true, token: sealed}
	probe := &fakeProbe{health: agentclient.HealthResult{OK: false, Status: 500, Err: "down"}}
	svc := New(repo, probe, cipher)

	rep, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified || rep.HandshakeOK {
		t.Fatalf("expected failure without handshake, got %+v", rep)
	}
	if repo.activated {
		t.Fatal("must not activate on failed health")
	}
	if len(repo.attempts) != 1 || repo.attempts[0].HealthOK {
		t.Fatalf("expected one recorded failed attempt, got %+v", repo.attempts)
	}
}

func TestVerify_GamesNotCovered(t *testing.T) {
	cipher, _ := secretbox.New("key")
	sealed, _ := cipher.Seal([]byte("tok"))
	repo := &fakeRepo{owned: true, manifest: baseManifest(), found: true, token: sealed}
	probe := &fakeProbe{
		health: healthy(),
		hs:     agentclient.HandshakeResult{OK: true, Accepted: true, SupportedGames: []string{"mafia"}}, // missing goofspiel
	}
	svc := New(repo, probe, cipher)

	rep, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Verified || rep.GamesCovered {
		t.Fatalf("expected games-not-covered failure, got %+v", rep)
	}
	if repo.activated {
		t.Fatal("must not activate when games are not covered")
	}
}

func TestVerify_RequiresSecretForBearer(t *testing.T) {
	cipher, _ := secretbox.New("key")
	repo := &fakeRepo{owned: true, manifest: baseManifest(), found: true} // no token
	svc := New(repo, &fakeProbe{health: healthy()}, cipher)

	_, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if !errors.Is(err, ErrEndpointSecretRequired) {
		t.Fatalf("expected ErrEndpointSecretRequired, got %v", err)
	}
}

func TestVerify_NotOwned(t *testing.T) {
	cipher, _ := secretbox.New("key")
	repo := &fakeRepo{owned: false, manifest: baseManifest(), found: true}
	svc := New(repo, &fakeProbe{}, cipher)
	if _, err := svc.Verify(context.Background(), "usr_x", "ag_1", "man_1"); !errors.Is(err, ErrForbiddenOwner) {
		t.Fatalf("expected ErrForbiddenOwner, got %v", err)
	}
}

func TestVerify_Unavailable(t *testing.T) {
	repo := &fakeRepo{owned: true, manifest: baseManifest(), found: true}
	svc := New(repo, nil, nil) // no probe -> disabled
	if _, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1"); !errors.Is(err, ErrVerificationUnavailable) {
		t.Fatalf("expected ErrVerificationUnavailable, got %v", err)
	}
}

func TestSetEndpointSecret_SealsToken(t *testing.T) {
	cipher, _ := secretbox.New("key")
	repo := &fakeRepo{owned: true, manifest: baseManifest(), found: true}
	svc := New(repo, &fakeProbe{}, cipher)

	if err := svc.SetEndpointSecret(context.Background(), "usr_1", "ag_1", "man_1", "my-token"); err != nil {
		t.Fatal(err)
	}
	if len(repo.token) == 0 {
		t.Fatal("token was not stored")
	}
	// Stored value must be ciphertext, not the plaintext.
	if string(repo.token) == "my-token" {
		t.Fatal("token stored in plaintext")
	}
	plain, err := cipher.Open(repo.token)
	if err != nil || string(plain) != "my-token" {
		t.Fatalf("stored token did not decrypt to original: %v %q", err, plain)
	}
}

func TestPublicActive(t *testing.T) {
	pub := baseManifest()
	pub.Visibility = "public"
	pub.Status = StatusVerified

	// Public + verified -> returned.
	repo := &fakeRepo{active: pub, activeFound: true}
	svc := New(repo, nil, nil)
	if _, err := svc.PublicActive(context.Background(), "ag_1"); err != nil {
		t.Fatalf("expected public manifest, got %v", err)
	}

	// Private -> hidden (404).
	priv := pub
	priv.Visibility = "private"
	svc2 := New(&fakeRepo{active: priv, activeFound: true}, nil, nil)
	if _, err := svc2.PublicActive(context.Background(), "ag_1"); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("expected private manifest hidden, got %v", err)
	}

	// None active -> 404.
	svc3 := New(&fakeRepo{activeFound: false}, nil, nil)
	if _, err := svc3.PublicActive(context.Background(), "ag_1"); !errors.Is(err, ErrNoManifest) {
		t.Fatalf("expected 404 when no active manifest, got %v", err)
	}
}

func TestPublicViewOmitsSensitive(t *testing.T) {
	m := baseManifest()
	m.EndpointURL = "https://secret.example.com/play"
	m.ContactEmail = "dev@example.com"
	m.Model = &ModelBlock{Provider: "OpenAI", Model: "GPT-5.5"}
	v := publicView(m)
	if _, ok := v["endpoint"]; ok {
		t.Fatal("public view must not expose endpoint")
	}
	if _, ok := v["contact"]; ok {
		t.Fatal("public view must not expose contact email")
	}
	model, ok := v["model"].(map[string]any)
	if !ok || model["developer_declared"] != true {
		t.Fatalf("model must be tagged developer_declared: %v", v["model"])
	}
}

func TestMissingGames(t *testing.T) {
	got := missingGames([]string{"a", "b", "c"}, []string{"b"})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Fatalf("unexpected: %v", got)
	}
	if len(missingGames([]string{"a"}, []string{"a", "x"})) != 0 {
		t.Fatal("expected all covered")
	}
}
