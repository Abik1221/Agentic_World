package platform

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// On a normal machine the trust store is present, so the check must stay SILENT.
//
// A boot-time warning that fires on healthy deployments is worse than no warning: it trains
// operators to scroll past the one message that matters.
func TestTLSTrustIsQuietWhenTheStoreIsPresent(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	if ok := CheckTLSTrust(log); !ok {
		t.Skip("no system trust store in this environment; nothing to assert")
	}
	if strings.Contains(buf.String(), "TLS TRUST STORE") {
		t.Fatalf("check logged a warning despite a working trust store: %s", buf.String())
	}
}

// NOTE on what is NOT tested here.
//
// The failure branches cannot be exercised in-process: Go reads the system trust store once and
// there is no supported way to empty it for a test. A first attempt asserted that the message
// contained "impact" and "fix" by checking a constant declared in the test itself, which is
// circular — it would have passed with the real message deleted.
//
// Both failure paths were verified LIVE instead, by running the server on debian:bookworm-slim
// (the image that started this whole investigation):
//
//	ERROR "EMPTY TLS TRUST STORE: outbound HTTPS cannot be verified"
//	  impact="agent endpoint verification and every https integration will fail"
//	  fix="install ca-certificates in the runtime image"
//
// and confirmed silent on an image that ships the bundle.
