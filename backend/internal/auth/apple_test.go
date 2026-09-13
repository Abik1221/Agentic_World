package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"testing"
)

func TestAppleTruthy(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{true, true},
		{false, false},
		{"true", true},
		{"TRUE", true},
		{"false", false},
		{"1", true},
		{float64(1), true},
		{float64(0), false},
		{nil, false},
	}
	for _, c := range cases {
		if got := appleTruthy(c.in); got != c.want {
			t.Fatalf("appleTruthy(%v)=%v want %v", c.in, got, c.want)
		}
	}
}

func TestAppleNonceMatches(t *testing.T) {
	raw := "test-nonce-value"
	sum := sha256.Sum256([]byte(raw))
	hexDigest := hex.EncodeToString(sum[:])
	b64 := base64.RawURLEncoding.EncodeToString(sum[:])

	if !appleNonceMatches(raw, raw) {
		t.Fatal("raw echo should match")
	}
	if !appleNonceMatches(raw, hexDigest) {
		t.Fatal("sha256 hex should match")
	}
	if !appleNonceMatches(raw, b64) {
		t.Fatal("sha256 base64url should match")
	}
	if appleNonceMatches(raw, "wrong") {
		t.Fatal("mismatch should fail")
	}
	if appleNonceMatches(raw, "") {
		t.Fatal("empty claim should fail")
	}
}

func TestParseApplePrivateKey(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	escaped := string(pemBytes)
	// Simulate GitHub secret paste with \n escapes.
	oneLine := ""
	for i, line := range splitLines(escaped) {
		if i > 0 {
			oneLine += `\n`
		}
		oneLine += line
	}
	got, err := parseApplePrivateKey(oneLine)
	if err != nil {
		t.Fatal(err)
	}
	if got.D.Cmp(key.D) != 0 {
		t.Fatal("parsed key does not match")
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
