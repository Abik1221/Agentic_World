package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/auth"
)

type fakeResolver struct {
	token string
	found bool
	err   error
}

func (f fakeResolver) PlayTarget(context.Context, string) (agentclient.Target, bool, error) {
	return agentclient.Target{Token: f.token}, f.found, f.err
}

// fakeKeys resolves exactly one (key -> agent) pair, like the identity service.
type fakeKeys struct {
	key   string
	agent string
}

func (f fakeKeys) ResolveAgentKey(_ context.Context, raw string) (*auth.Principal, error) {
	if raw == f.key {
		return &auth.Principal{Scope: auth.ScopeAgent, AgentPublicID: f.agent}, nil
	}
	return nil, errors.New("unknown key")
}

func TestSocketAuthenticatorAgentKey(t *testing.T) {
	a := socketAuthenticator{
		resolver: fakeResolver{found: false},
		keys:     fakeKeys{key: "sk_arena_good", agent: "ag_1"},
		log:      slog.Default(),
	}
	// A valid agent key resolving to the claimed agent is accepted.
	if id, ok := a.Authenticate(context.Background(), "sk_arena_good", "ag_1"); !ok || id != "ag_1" {
		t.Fatalf("valid agent key rejected: id=%q ok=%v", id, ok)
	}
	// A valid key but for a DIFFERENT agent id is rejected.
	if _, ok := a.Authenticate(context.Background(), "sk_arena_good", "ag_other"); ok {
		t.Fatal("agent key accepted for the wrong agent id")
	}
	// An unknown key with no endpoint-secret fallback is rejected.
	if _, ok := a.Authenticate(context.Background(), "sk_arena_bad", "ag_1"); ok {
		t.Fatal("unknown key accepted")
	}
}

func TestSocketAuthenticator(t *testing.T) {
	log := slog.Default()
	cases := []struct {
		name           string
		resolver       fakeResolver
		token, agentID string
		wantOK         bool
	}{
		{"valid secret", fakeResolver{token: "s3cr3t", found: true}, "s3cr3t", "ag_1", true},
		{"wrong secret", fakeResolver{token: "s3cr3t", found: true}, "nope", "ag_1", false},
		{"no manifest", fakeResolver{found: false}, "s3cr3t", "ag_1", false},
		{"empty token", fakeResolver{token: "s3cr3t", found: true}, "", "ag_1", false},
		{"empty agent id", fakeResolver{token: "s3cr3t", found: true}, "s3cr3t", "", false},
		{"resolver error", fakeResolver{err: errors.New("db down")}, "s3cr3t", "ag_1", false},
		{"no stored secret", fakeResolver{token: "", found: true}, "s3cr3t", "ag_1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := socketAuthenticator{resolver: tc.resolver, log: log}
			id, ok := a.Authenticate(context.Background(), tc.token, tc.agentID)
			if ok != tc.wantOK {
				t.Fatalf("Authenticate ok=%v want %v", ok, tc.wantOK)
			}
			if ok && id != tc.agentID {
				t.Fatalf("resolved id=%q want %q", id, tc.agentID)
			}
		})
	}
}
