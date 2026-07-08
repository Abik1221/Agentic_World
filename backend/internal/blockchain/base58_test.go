package blockchain

import "testing"

func TestEncodeBase58(t *testing.T) {
	cases := []struct {
		in   []byte
		want string
	}{
		{[]byte{0}, "1"},         // single zero byte → one leading '1'
		{[]byte{0, 0, 1}, "112"}, // two leading zeros + value 1 ('2')
		{[]byte{57}, "z"},        // last alphabet char
		{[]byte{58}, "21"},       // rolls over
		{[]byte{0, 0}, "11"},     // all zeros
	}
	for _, c := range cases {
		if got := EncodeBase58(c.in); got != c.want {
			t.Errorf("EncodeBase58(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNewReference(t *testing.T) {
	ref, err := NewReference()
	if err != nil {
		t.Fatalf("NewReference: %v", err)
	}
	// 32 random bytes base58-encode to ~43-44 chars; must be non-empty and valid.
	if len(ref) < 32 || len(ref) > 48 {
		t.Fatalf("reference length %d out of expected range: %q", len(ref), ref)
	}
	for _, r := range ref {
		if !containsRune(base58Alphabet, r) {
			t.Fatalf("reference contains non-base58 rune %q in %q", r, ref)
		}
	}
	// Two references must differ (astronomically unlikely to collide).
	ref2, _ := NewReference()
	if ref == ref2 {
		t.Fatal("two references collided")
	}
}

func containsRune(s string, r rune) bool {
	for _, c := range s {
		if c == r {
			return true
		}
	}
	return false
}
