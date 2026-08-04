package identity

import (
	"strings"
	"testing"
)

// A label is the REPLACE key for a machine's credential (migration 0071), so
// normalization is a correctness property, not cosmetics: two spellings of one
// machine's name must collapse to one slot, or `pyyol login` starts accumulating keys
// instead of replacing its own.
func TestNormalizeKeyLabel_CollapsesSpellings(t *testing.T) {
	same := []string{"Macbook Pro", "macbook pro", "  MACBOOK   pro  ", "Macbook\tPro\n"}
	want, err := NormalizeKeyLabel(same[0])
	if err != nil {
		t.Fatalf("NormalizeKeyLabel(%q): %v", same[0], err)
	}
	if want != "macbook pro" {
		t.Fatalf("got %q, want %q", want, "macbook pro")
	}
	for _, in := range same[1:] {
		got, err := NormalizeKeyLabel(in)
		if err != nil {
			t.Fatalf("NormalizeKeyLabel(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("NormalizeKeyLabel(%q) = %q, want %q — one machine would occupy two key slots", in, got, want)
		}
	}
}

// Control characters arrive from hostnames and shell interpolation, not from a
// person. They are stripped rather than rejected: failing a login over an invisible
// byte is the worse outcome.
func TestNormalizeKeyLabel_StripsControlChars(t *testing.T) {
	got, err := NormalizeKeyLabel("ci\x00-run\x1fner\x7f")
	if err != nil {
		t.Fatalf("NormalizeKeyLabel: %v", err)
	}
	if got != "ci-runner" {
		t.Fatalf("got %q, want %q", got, "ci-runner")
	}
}

func TestNormalizeKeyLabel_Rejected(t *testing.T) {
	for _, bad := range []string{"", "   ", "\t\n", "\x00\x01", strings.Repeat("a", maxKeyLabelLen+1)} {
		if got, err := NormalizeKeyLabel(bad); err == nil {
			t.Errorf("NormalizeKeyLabel(%q) = %q, want an error", bad, got)
		}
	}
	// The boundary itself is allowed — an off-by-one here silently truncates or
	// rejects a legitimate 40-char hostname.
	atLimit := strings.Repeat("a", maxKeyLabelLen)
	if _, err := NormalizeKeyLabel(atLimit); err != nil {
		t.Errorf("NormalizeKeyLabel(%d chars) = %v, want no error", maxKeyLabelLen, err)
	}
}

// The length cap counts RUNES, not bytes: a 40-character label of multi-byte
// characters is a valid machine name and must not be rejected for being 80 bytes.
func TestNormalizeKeyLabel_CountsRunesNotBytes(t *testing.T) {
	if _, err := NormalizeKeyLabel(strings.Repeat("é", maxKeyLabelLen)); err != nil {
		t.Errorf("NormalizeKeyLabel(40 multi-byte runes) = %v, want no error", err)
	}
	if _, err := NormalizeKeyLabel(strings.Repeat("é", maxKeyLabelLen+1)); err == nil {
		t.Error("NormalizeKeyLabel(41 multi-byte runes) = nil error, want rejection")
	}
}

// An unlabelled issuer must land in a slot of its OWN KIND. If every unlabelled
// caller shared one label, an old CLI could evict a container's key — the exact
// failure labels were introduced to end. And if the fallback varied per request,
// unlabelled clients would accumulate keys until they hit the cap.
func TestFallbackKeyLabel_StableAndSeparated(t *testing.T) {
	cli := fallbackKeyLabel("pyyol/0.9.1 (python 3.12)")
	browser := fallbackKeyLabel("Mozilla/5.0 (Macintosh) AppleWebKit/537.36 Chrome/141")
	other := fallbackKeyLabel("curl/8.4.0")

	if cli == browser || cli == other || browser == other {
		t.Fatalf("fallback labels collide: cli=%q browser=%q other=%q", cli, browser, other)
	}
	for _, ua := range []string{"pyyol/0.9.1 (python 3.12)", "pyyol/1.2.0 (node 22)"} {
		if got := fallbackKeyLabel(ua); got != cli {
			t.Errorf("fallbackKeyLabel(%q) = %q, want the stable %q", ua, got, cli)
		}
	}
	// Whatever the fallback is, it must survive the same normalization real labels go
	// through — otherwise issuing would fail for a caller that sent no label at all.
	for _, l := range []string{cli, browser, other} {
		norm, err := NormalizeKeyLabel(l)
		if err != nil {
			t.Errorf("fallback label %q is not a valid label: %v", l, err)
		}
		if norm != l {
			t.Errorf("fallback label %q normalizes to %q — store it in normalized form", l, norm)
		}
	}
}
