package platform

import (
	"strings"
	"testing"
)

func TestNewID(t *testing.T) {
	id := NewID(PrefixAgent)
	if !strings.HasPrefix(id, "ag_") {
		t.Fatalf("id %q missing prefix", id)
	}
	if id != strings.ToLower(id) {
		t.Fatalf("id %q should be lowercase", id)
	}
	if strings.Contains(id, "=") {
		t.Fatalf("id %q should be unpadded base32", id)
	}
}

func TestNewID_Unique(t *testing.T) {
	const n = 10_000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id := NewID(PrefixMatch)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}
