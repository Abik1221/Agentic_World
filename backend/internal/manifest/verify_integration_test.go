package manifest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/secretbox"
)

// TestVerify_EndToEndRealClient wires the verify service to the REAL
// SSRF-guarded agentclient (AllowPrivate, so it can reach the loopback
// httptest server) and a real secretbox, exercising the full M2 stack:
// service -> outbound HTTP -> health + handshake -> games cross-check -> activate.
func TestVerify_EndToEndRealClient(t *testing.T) {
	var sawBearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"status":"healthy","agent":"Atlas","version":"1.0.0"}`))
		case "/handshake":
			sawBearer = r.Header.Get("Authorization")
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"accepted":true,"sdkVersion":"1.0.0","supportedGames":["mafia","goofspiel"]}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	cipher, _ := secretbox.New("integration-key")
	sealed, _ := cipher.Seal([]byte("secret-bearer"))

	m := baseManifest()
	m.EndpointURL = srv.URL + "/play" // /health and /handshake are siblings
	repo := &fakeRepo{owned: true, manifest: m, found: true, token: sealed}

	probe := agentclient.New(agentclient.Config{AllowPrivate: true, Retries: 0})
	svc := New(repo, probe, cipher)

	rep, err := svc.Verify(context.Background(), "usr_1", "ag_1", "man_1")
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Verified {
		t.Fatalf("expected verified, got %+v", rep)
	}
	if !repo.activated {
		t.Fatal("expected activation")
	}
	if sawBearer != "Bearer secret-bearer" {
		t.Fatalf("endpoint did not receive decrypted bearer token, got %q", sawBearer)
	}
	if len(repo.attempts) != 1 || !repo.attempts[0].HandshakeOK {
		t.Fatalf("expected one successful recorded attempt, got %+v", repo.attempts)
	}
}
