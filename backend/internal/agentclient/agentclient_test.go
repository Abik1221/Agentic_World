package agentclient

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// devClient permits loopback so tests can hit httptest servers (127.0.0.1).
func devClient(cfg Config) *Client {
	cfg.AllowPrivate = true
	return New(cfg)
}

func TestBlockedIP(t *testing.T) {
	cases := []struct {
		ip      string
		blocked bool
	}{
		{"127.0.0.1", true},
		{"::1", true},
		{"10.0.0.5", true},
		{"172.16.9.9", true},
		{"192.168.1.1", true},
		{"169.254.169.254", true}, // cloud metadata
		{"fe80::1", true},
		{"fc00::1", true},
		{"0.0.0.0", true},
		{"100.64.0.1", true}, // CGNAT
		{"224.0.0.1", true},  // multicast
		{"8.8.8.8", false},
		{"1.1.1.1", false},
		{"93.184.216.34", false}, // example.com
		{"2606:2800:220:1::1", false},
	}
	for _, tc := range cases {
		ip := net.ParseIP(tc.ip)
		if ip == nil {
			t.Fatalf("bad test ip %q", tc.ip)
		}
		if got := blockedIP(ip); got != tc.blocked {
			t.Errorf("blockedIP(%s) = %v, want %v", tc.ip, got, tc.blocked)
		}
	}
}

func TestSiblingURL(t *testing.T) {
	cases := []struct{ in, name, want string }{
		{"https://a.example.com/play", "health", "https://a.example.com/health"},
		{"https://a.example.com/play", "handshake", "https://a.example.com/handshake"},
		{"https://a.example.com/agents/atlas/play", "health", "https://a.example.com/agents/atlas/health"},
		{"https://a.example.com", "health", "https://a.example.com/health"},
		{"https://a.example.com/play?x=1", "health", "https://a.example.com/health"},
	}
	for _, tc := range cases {
		got, err := siblingURL(tc.in, tc.name)
		if err != nil {
			t.Fatalf("siblingURL(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("siblingURL(%q,%q) = %q, want %q", tc.in, tc.name, got, tc.want)
		}
	}
	if _, err := siblingURL("://bad", "health"); err == nil {
		t.Error("expected error for invalid url")
	}
}

func TestHealth_Healthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"healthy","agent":"Atlas","version":"1.0.0"}`))
	}))
	defer srv.Close()

	res, err := devClient(Config{}).Health(context.Background(), Target{EndpointURL: srv.URL + "/play"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Body.Agent != "Atlas" || res.Status != 200 {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestHealth_Unhealthy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"status":"down"}`))
	}))
	defer srv.Close()

	// Retries 0 so the 500 is not retried during the test.
	res, _ := devClient(Config{Retries: 0}).Health(context.Background(), Target{EndpointURL: srv.URL + "/play"})
	if res.OK {
		t.Fatalf("expected not-OK on 500, got %+v", res)
	}
}

func TestHandshake_AcceptedWithBearer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("missing/wrong bearer: %q", got)
		}
		if r.Header.Get("X-Arena-Request-Id") == "" {
			t.Error("missing request id header")
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"accepted":true,"sdkVersion":"1.0.0","supportedGames":["mafia","goofspiel"]}`))
	}))
	defer srv.Close()

	res, err := devClient(Config{}).Handshake(context.Background(),
		Target{EndpointURL: srv.URL + "/play", Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || !res.Accepted || len(res.SupportedGames) != 2 {
		t.Fatalf("unexpected: %+v", res)
	}
}

func TestHandshake_ChallengeMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"accepted":true,"challenge":"not-the-challenge-we-sent"}`))
	}))
	defer srv.Close()

	res, err := devClient(Config{}).Handshake(context.Background(),
		Target{EndpointURL: srv.URL + "/play", Token: "test-token", AgentID: "ag_1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Fatalf("expected challenge mismatch to fail handshake, got %+v", res)
	}
	if res.Err == "" {
		t.Fatal("expected a challenge-mismatch error")
	}
}

func TestHandshake_Rejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"accepted":false}`))
	}))
	defer srv.Close()
	res, _ := devClient(Config{}).Handshake(context.Background(), Target{EndpointURL: srv.URL + "/play"})
	if res.OK {
		t.Fatalf("expected rejected handshake, got %+v", res)
	}
}

func TestSSRFGuardBlocksLoopback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"healthy"}`))
	}))
	defer srv.Close()

	// AllowPrivate defaults to false -> dialing httptest's 127.0.0.1 must be blocked.
	c := New(Config{Retries: 0})
	_, err := c.Health(context.Background(), Target{EndpointURL: srv.URL + "/play"})
	if err == nil {
		t.Fatal("expected SSRF guard to block loopback")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("expected a blocked-address error, got: %v", err)
	}
}

func TestOversizedBodyRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":"healthy","padding":"` + strings.Repeat("A", 1000) + `"}`))
	}))
	defer srv.Close()

	res, err := devClient(Config{MaxBodyBytes: 32, Retries: 0}).
		Health(context.Background(), Target{EndpointURL: srv.URL + "/play"})
	if err == nil {
		t.Fatalf("expected oversized-body error, got res %+v", res)
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRedirectBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_, err := devClient(Config{Retries: 0}).Health(context.Background(), Target{EndpointURL: srv.URL + "/play"})
	if err == nil {
		t.Fatal("expected redirect to be blocked")
	}
}

func TestPlay(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tk" {
			t.Errorf("play missing bearer: %q", got)
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["seat"] != float64(1) {
			t.Errorf("request body not delivered: %+v", in)
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"card":7}`))
	}))
	defer srv.Close()

	var out struct {
		Card int `json:"card"`
	}
	status, err := devClient(Config{}).Play(context.Background(),
		Target{EndpointURL: srv.URL + "/play", Token: "tk"},
		map[string]any{"seat": 1}, &out)
	if err != nil || status != 200 || out.Card != 7 {
		t.Fatalf("play failed: status=%d err=%v out=%+v", status, err, out)
	}
}

func TestPlay_Non200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
	}))
	defer srv.Close()
	if _, err := devClient(Config{Retries: 0}).Play(context.Background(),
		Target{EndpointURL: srv.URL + "/play"}, map[string]any{}, nil); err == nil {
		t.Fatal("expected error on non-200 play")
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	_, err := devClient(Config{Timeout: 20 * time.Millisecond, MaxTimeout: 20 * time.Millisecond, Retries: 0}).
		Health(context.Background(), Target{EndpointURL: srv.URL + "/play"})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}
