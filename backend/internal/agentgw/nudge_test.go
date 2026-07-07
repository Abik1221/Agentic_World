package agentgw

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

func TestVersionLess(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"1.0.0", "1.0.1", true},
		{"1.2.0", "1.10.0", true}, // numeric, not lexical
		{"1.0.0", "1.0.0", false},
		{"2.0.0", "1.9.9", false},
		{"1.0", "1.0.0", false},       // missing patch defaults to 0
		{"1.0.0-rc1", "1.0.0", false}, // suffix dropped; cores equal
		{"", "1.0.0", false},          // unparseable ⇒ fail open (not less)
		{"1.0.0", "garbage", false},   // unparseable ⇒ fail open
	}
	for _, c := range cases {
		if got := versionLess(c.a, c.b); got != c.want {
			t.Errorf("versionLess(%q,%q)=%v want %v", c.a, c.b, got, c.want)
		}
	}
}

// gatewayWith spins up a gateway with a custom SDKVersionInfo and returns its ws URL.
func gatewayWith(t *testing.T, info func(string) (string, string)) (string, func()) {
	t.Helper()
	gw := New(nil, Options{
		TurnTimeout:         time.Second,
		HeartbeatInterval:   100 * time.Millisecond,
		LivenessTimeout:     time.Second,
		AllowInsecureOrigin: true,
		SDKVersionInfo:      info,
	}, nil)
	srv := httptest.NewServer(gw.Handler())
	return "ws" + strings.TrimPrefix(srv.URL, "http"), srv.Close
}

// registerRaw dials, consumes hello, sends a register frame with the given
// sdk version+language, and returns the gateway's reply frame.
func registerRaw(t *testing.T, wsURL, sdkVersion, sdkLang string) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close(websocket.StatusNormalClosure, "")
	var hello Frame
	if err := wsjson.Read(ctx, ws, &hello); err != nil || hello.T != FrameHello {
		t.Fatalf("expected hello, got %+v err=%v", hello, err)
	}
	if err := wsjson.Write(ctx, ws, Frame{
		T: FrameRegister, AgentID: "ag_1", AgentName: "t",
		Games: []string{"goofspiel"}, SDKVersion: sdkVersion, SDKLanguage: sdkLang,
	}); err != nil {
		t.Fatalf("register write: %v", err)
	}
	var reply Frame
	if err := wsjson.Read(ctx, ws, &reply); err != nil {
		t.Fatalf("read reply: %v", err)
	}
	return reply
}

func TestUpgradeNudgeEchoedOnRegistered(t *testing.T) {
	wsURL, closeSrv := gatewayWith(t, func(lang string) (string, string) {
		if lang == "python" {
			return "1.2.0", "" // latest, no floor
		}
		return "", ""
	})
	defer closeSrv()

	reply := registerRaw(t, wsURL, "1.0.0", "python")
	if reply.T != FrameRegistered {
		t.Fatalf("expected registered, got %+v", reply)
	}
	if reply.LatestSDK != "1.2.0" {
		t.Errorf("latest_sdk = %q, want 1.2.0", reply.LatestSDK)
	}
}

func TestNoNudgeWhenUnconfigured(t *testing.T) {
	// SDKVersionInfo returns empties ⇒ no latest/min on the registered frame.
	wsURL, closeSrv := gatewayWith(t, func(string) (string, string) { return "", "" })
	defer closeSrv()
	reply := registerRaw(t, wsURL, "1.0.0", "python")
	if reply.T != FrameRegistered || reply.LatestSDK != "" || reply.MinSDK != "" {
		t.Fatalf("expected clean registered with no nudge, got %+v", reply)
	}
}

func TestTooOldSDKRefused(t *testing.T) {
	wsURL, closeSrv := gatewayWith(t, func(lang string) (string, string) {
		return "2.0.0", "2.0.0" // floor 2.0.0
	})
	defer closeSrv()

	reply := registerRaw(t, wsURL, "1.0.0", "python") // below floor
	if reply.T != FrameError || reply.Error != "sdk_too_old" {
		t.Fatalf("expected sdk_too_old error, got %+v", reply)
	}
	if !strings.Contains(reply.Reason, "2.0.0") {
		t.Errorf("reason should name the required version: %q", reply.Reason)
	}
}

func TestNewEnoughSDKAccepted(t *testing.T) {
	wsURL, closeSrv := gatewayWith(t, func(lang string) (string, string) {
		return "2.0.0", "1.0.0" // floor 1.0.0
	})
	defer closeSrv()
	reply := registerRaw(t, wsURL, "1.5.0", "python") // above floor
	if reply.T != FrameRegistered {
		t.Fatalf("expected registered, got %+v", reply)
	}
	if reply.MinSDK != "1.0.0" || reply.LatestSDK != "2.0.0" {
		t.Errorf("min/latest = %q/%q, want 1.0.0/2.0.0", reply.MinSDK, reply.LatestSDK)
	}
}
