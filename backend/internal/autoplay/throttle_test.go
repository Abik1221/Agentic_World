package autoplay

import (
	"testing"
	"time"
)

func TestSandboxThrottle_CountsWithinWindowAndExpires(t *testing.T) {
	base := time.Unix(1_000_000, 0)
	now := base
	th := NewSandboxThrottle(5 * time.Minute)
	th.now = func() time.Time { return now }

	if th.ActiveCount("a") != 0 {
		t.Fatal("no starts yet → 0")
	}
	th.Record("a")
	th.Record("a")
	if got := th.ActiveCount("a"); got != 2 {
		t.Fatalf("two recent starts → 2, got %d", got)
	}
	// Advance past the window: both starts age out.
	now = base.Add(6 * time.Minute)
	if got := th.ActiveCount("a"); got != 0 {
		t.Fatalf("starts older than the window should expire, got %d", got)
	}
	// A fresh start counts again; other agents are independent.
	th.Record("a")
	if th.ActiveCount("a") != 1 || th.ActiveCount("b") != 0 {
		t.Fatalf("per-agent tracking wrong: a=%d b=%d", th.ActiveCount("a"), th.ActiveCount("b"))
	}
}
