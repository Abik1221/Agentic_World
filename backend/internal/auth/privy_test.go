package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const testPrivyApp = "app_test_123"

// newPrivyTestKey returns a fresh P-256 keypair with the public half PEM-encoded
// the way the Privy dashboard hands operators their verification key.
func newPrivyTestKey(t *testing.T) (*ecdsa.PrivateKey, string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatalf("marshal pub: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
	return priv, string(pemBytes)
}

func signPrivyToken(t *testing.T, priv *ecdsa.PrivateKey, claims jwt.RegisteredClaims) string {
	t.Helper()
	tok, err := jwt.NewWithClaims(jwt.SigningMethodES256, claims).SignedString(priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

func validClaims() jwt.RegisteredClaims {
	now := time.Now()
	return jwt.RegisteredClaims{
		Issuer:    "privy.io",
		Audience:  jwt.ClaimStrings{testPrivyApp},
		Subject:   "did:privy:abc123",
		IssuedAt:  jwt.NewNumericDate(now),
		ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
	}
}

func TestPrivyVerifier_Disabled(t *testing.T) {
	v, err := NewPrivyVerifier("", "")
	if err != nil || v != nil {
		t.Fatalf("empty config should disable: v=%v err=%v", v, err)
	}
	if v.Enabled() {
		t.Fatal("nil verifier must report disabled")
	}
	if _, err := v.Verify("anything"); err == nil {
		t.Fatal("disabled verifier must reject all tokens")
	}
}

func TestPrivyVerifier_HappyPath(t *testing.T) {
	priv, pubPEM := newPrivyTestKey(t)
	v, err := NewPrivyVerifier(testPrivyApp, pubPEM)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}
	id, err := v.Verify(signPrivyToken(t, priv, validClaims()))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id.UserID != "did:privy:abc123" {
		t.Fatalf("subject = %q, want did:privy:abc123", id.UserID)
	}
}

func TestPrivyVerifier_Rejects(t *testing.T) {
	priv, pubPEM := newPrivyTestKey(t)
	other, _ := newPrivyTestKey(t)
	v, err := NewPrivyVerifier(testPrivyApp, pubPEM)
	if err != nil {
		t.Fatalf("new verifier: %v", err)
	}

	cases := map[string]jwt.RegisteredClaims{
		"wrong_audience": func() jwt.RegisteredClaims { c := validClaims(); c.Audience = jwt.ClaimStrings{"other_app"}; return c }(),
		"wrong_issuer":   func() jwt.RegisteredClaims { c := validClaims(); c.Issuer = "evil.io"; return c }(),
		"expired": func() jwt.RegisteredClaims {
			c := validClaims()
			c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour))
			return c
		}(),
		"no_subject": func() jwt.RegisteredClaims { c := validClaims(); c.Subject = ""; return c }(),
	}
	for name, claims := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := v.Verify(signPrivyToken(t, priv, claims)); err == nil {
				t.Fatalf("%s: expected rejection", name)
			}
		})
	}

	t.Run("wrong_signing_key", func(t *testing.T) {
		if _, err := v.Verify(signPrivyToken(t, other, validClaims())); err == nil {
			t.Fatal("token signed by a different key must be rejected")
		}
	})

	t.Run("hs256_downgrade", func(t *testing.T) {
		tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, validClaims()).SignedString([]byte("secret"))
		if err != nil {
			t.Fatalf("sign hs256: %v", err)
		}
		if _, err := v.Verify(tok); err == nil {
			t.Fatal("HS256 token must be rejected (algorithm confusion)")
		}
	})
}
