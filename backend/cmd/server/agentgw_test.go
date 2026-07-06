package main

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
)

type fakeResolver struct {
	token string
	found bool
	err   error
}

func (f fakeResolver) PlayTarget(context.Context, string) (agentclient.Target, bool, error) {
	return agentclient.Target{Token: f.token}, f.found, f.err
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
