package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/agentclient"
	"github.com/agent-arena/arena/internal/httpx"
	"github.com/agent-arena/arena/internal/manifest"
)

// stubCert is a ranked certLooker: certified + hasURL is the hosted-away path.
type stubCert struct {
	certified bool
	hasURL    bool
	gameOK    bool
	gameFound bool
}

func (s stubCert) RequireCertified(context.Context, string) error {
	if !s.certified {
		return manifest.ErrNotCertified
	}
	return nil
}

func (s stubCert) PlayTarget(context.Context, string) (agentclient.Target, bool, error) {
	return agentclient.Target{}, s.hasURL, nil
}

func (s stubCert) SupportsGame(context.Context, string, string) (bool, bool, error) {
	return s.gameOK, s.gameFound, nil
}

func TestCheckEligibleConnectedNoCertSitsRanked(t *testing.T) {
	a := verifierAdapter{
		cert: stubCert{certified: false, hasURL: false},
		conn: func(string) bool { return true },
	}
	if err := a.CheckEligible(context.Background(), "agt_local"); err != nil {
		t.Fatalf("connected local SDK with no cert must sit ranked, got %v", err)
	}
}

func TestCheckEligibleNothingReachableRefuses(t *testing.T) {
	a := verifierAdapter{
		cert: stubCert{certified: false, hasURL: false},
		conn: func(string) bool { return false },
	}
	err := a.CheckEligible(context.Background(), "agt_away")
	if err == nil {
		t.Fatal("nothing reachable must refuse ranked sit")
	}
	var api *httpx.APIError
	if !errors.As(err, &api) || api.Code != "agent_not_playable" {
		t.Fatalf("code=%v, want agent_not_playable", err)
	}
	if strings.Contains(api.Message, "Verify your agent's endpoint") {
		t.Fatalf("ranked refuse reused the old certify-only copy: %q", api.Message)
	}
	if !strings.Contains(api.Message, "pyyol play") {
		t.Fatalf("ranked refuse must name the local CLI path, got %q", api.Message)
	}
}

func TestCheckEligibleHostedCertWithoutSocketSitsRanked(t *testing.T) {
	a := verifierAdapter{
		cert: stubCert{certified: true, hasURL: true},
		conn: func(string) bool { return false },
	}
	if err := a.CheckEligible(context.Background(), "agt_hosted"); err != nil {
		t.Fatalf("hosted verify without a local socket must sit ranked, got %v", err)
	}
}

func TestRankedEntryGateConnectedNoCertEnqueues(t *testing.T) {
	g := rankedEntryGate{
		cert: stubCert{certified: false, hasURL: false},
		conn: func(string) bool { return true },
		game: "goofspiel",
	}
	if err := g.RequireCertified(context.Background(), "agt_local"); err != nil {
		t.Fatalf("connected local SDK with no cert must enter the ranked queue, got %v", err)
	}
}

func TestRankedEntryGateNothingReachableRefuses(t *testing.T) {
	g := rankedEntryGate{
		cert: stubCert{certified: false, hasURL: false},
		conn: func(string) bool { return false },
	}
	err := g.RequireCertified(context.Background(), "agt_away")
	if err == nil {
		t.Fatal("nothing reachable must refuse ranked enqueue")
	}
	var api *httpx.APIError
	if !errors.As(err, &api) || api.Code != "agent_not_playable" {
		t.Fatalf("code=%v, want agent_not_playable", err)
	}
}

func TestRankedEntryGateHostedCertWithoutSocketEnqueues(t *testing.T) {
	g := rankedEntryGate{
		cert: stubCert{certified: true, hasURL: true, gameOK: true, gameFound: true},
		conn: func(string) bool { return false },
		game: "goofspiel",
	}
	if err := g.RequireCertified(context.Background(), "agt_hosted"); err != nil {
		t.Fatalf("hosted verify without a local socket must enter the ranked queue, got %v", err)
	}
}
