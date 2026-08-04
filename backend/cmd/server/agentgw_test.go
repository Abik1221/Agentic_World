package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/auth"
	"github.com/agent-arena/arena/internal/identity"
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
// revoked makes that same key report identity.ErrRevokedAPIKey, which is what the
// real service returns when the presented secret verifies against a revoked row.
type fakeKeys struct {
	key     string
	agent   string
	revoked bool
}

func (f fakeKeys) ResolveAgentKey(_ context.Context, raw string) (*auth.Principal, error) {
	if raw == f.key {
		if f.revoked {
			return nil, identity.ErrRevokedAPIKey
		}
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

// AuthFailureReason turns a rejection into something the developer can act on. Its
// value depends entirely on being SPECIFIC where that is safe and SILENT where it is
// not, so both halves are asserted here.
func TestAuthFailureReason(t *testing.T) {
	a := socketAuthenticator{
		resolver: fakeResolver{found: false},
		keys:     fakeKeys{key: "sk_arena_good", agent: "ag_1"},
		log:      slog.Default(),
	}
	ctx := context.Background()

	// A key the caller provably held, now revoked. The code must be exactly
	// key_revoked: both SDKs branch on it to stop refreshing around a dead credential
	// (which otherwise keeps the agent playing on a JWT and never reports the problem).
	revoking := socketAuthenticator{
		resolver: fakeResolver{found: false},
		keys:     fakeKeys{key: "sk_arena_good", agent: "ag_1", revoked: true},
		log:      slog.Default(),
	}
	code, reason := revoking.AuthFailureReason(ctx, "sk_arena_good", "ag_1")
	if code != "key_revoked" {
		t.Fatalf("revoked key: code = %q, want %q", code, "key_revoked")
	}
	if reason == "" {
		t.Fatal("revoked key: empty reason — the developer learns nothing")
	}

	// A valid key aimed at the wrong agent: naming the mismatch is safe (the caller
	// holds the key) and is the difference between a five-second fix and a support ask.
	code, reason = a.AuthFailureReason(ctx, "sk_arena_good", "ag_other")
	if code != "agent_mismatch" || reason == "" {
		t.Fatalf("wrong agent: code = %q reason = %q", code, reason)
	}

	// An UNKNOWN key must stay opaque. Anything specific here is an oracle: it would
	// confirm which half of (agent_id, token) was wrong to someone holding neither.
	if code, reason = a.AuthFailureReason(ctx, "sk_arena_bad", "ag_1"); code != "" || reason != "" {
		t.Fatalf("unknown key leaked a diagnosis: code = %q reason = %q", code, reason)
	}

	// No key resolver (endpoint-secret-only deployment) must not panic or invent one.
	bare := socketAuthenticator{resolver: fakeResolver{found: false}, log: slog.Default()}
	if code, reason = bare.AuthFailureReason(ctx, "whatever", "ag_1"); code != "" || reason != "" {
		t.Fatalf("no resolver: code = %q reason = %q", code, reason)
	}
}
