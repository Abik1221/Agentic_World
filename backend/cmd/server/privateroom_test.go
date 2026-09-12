package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/agent-arena/arena/internal/httpx"
)

func TestPrivateRoomPlayableAcceptsLocalOrHosted(t *testing.T) {
	cases := []struct {
		name                           string
		connected, certified, autoplay bool
		want                           bool
	}{
		{"cli socket, no cert", true, false, false, true},
		{"certified connected-ranked / hosted verified", false, true, false, true},
		{"autoplay on, no url", false, false, true, true},
		{"nothing reachable", false, false, false, false},
		{"socket and cert", true, true, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := privateRoomPlayable(tc.connected, tc.certified, tc.autoplay)
			if got != tc.want {
				t.Fatalf("privateRoomPlayable(%v,%v,%v)=%v, want %v",
					tc.connected, tc.certified, tc.autoplay, got, tc.want)
			}
		})
	}
}

func TestPrivateRoomRefusalIsNotTheRankedEndpointCopy(t *testing.T) {
	var api *httpx.APIError
	if !errors.As(errPrivateRoomNotPlayable(), &api) {
		t.Fatal("expected an APIError")
	}
	if api.Code != "agent_not_playable" {
		t.Fatalf("code=%q, want agent_not_playable", api.Code)
	}
	if !strings.Contains(api.Message, "pyyol play") || !strings.Contains(api.Message, "Private rooms") {
		t.Fatalf("room refusal must name the local CLI path, got %q", api.Message)
	}
	if strings.Contains(api.Message, "Verify your agent's endpoint") {
		t.Fatalf("room refusal reused the ranked certify-endpoint copy: %q", api.Message)
	}
}
