package auth

import (
	"context"
	"testing"
)

type fakeOwner struct{ id string }

func (f fakeOwner) PrimaryAgentOf(context.Context, string) (string, error) { return f.id, nil }

func TestSittingAgentPrefersTheKey(t *testing.T) {
	id, err := SittingAgent(context.Background(), &Principal{
		Scope: ScopeAgent, AgentPublicID: "ag_key", UserPublicID: "usr_1",
	}, fakeOwner{id: "ag_other"})
	if err != nil || id != "ag_key" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestSittingAgentResolvesOwnerJWT(t *testing.T) {
	id, err := SittingAgent(context.Background(), &Principal{
		Scope: ScopeUser, UserPublicID: "usr_1",
	}, fakeOwner{id: "ag_mine"})
	if err != nil || id != "ag_mine" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

func TestSittingAgentRefusesOwnerWithNoAgent(t *testing.T) {
	_, err := SittingAgent(context.Background(), &Principal{
		Scope: ScopeUser, UserPublicID: "usr_1",
	}, fakeOwner{id: ""})
	if err == nil {
		t.Fatal("expected agent_required")
	}
}
