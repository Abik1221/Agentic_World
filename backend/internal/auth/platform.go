package auth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// PlatformVerifier verifies the Super Admin's service-to-service "Platform"
// token. It carries the same Ed25519 public key already used to authenticate the
// platform config bus (PLATFORM_ADMIN_PUBLIC_KEY), so the two services need no
// new shared secret. A nil *PlatformVerifier means platform auth is disabled
// (no key configured); it rejects every token rather than panicking.
type PlatformVerifier struct {
	pub ed25519.PublicKey
}

// NewPlatformVerifier builds a verifier from a base64 (std) 32-byte public key.
// An empty key returns (nil, nil): platform auth stays disabled and the existing
// Bearer credential paths are unaffected.
func NewPlatformVerifier(pubB64 string) (*PlatformVerifier, error) {
	if strings.TrimSpace(pubB64) == "" {
		return nil, nil
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return nil, err
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("auth: platform public key must be 32 bytes")
	}
	return &PlatformVerifier{pub: ed25519.PublicKey(pub)}, nil
}

// Enabled reports whether a platform key is configured.
func (v *PlatformVerifier) Enabled() bool { return v != nil }

type platformClaims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

// Verify checks a Platform token of the form base64url(payload).base64url(sig),
// where sig is an Ed25519 signature over the raw payload JSON bytes. On success
// it returns a ScopePlatform principal. A nil verifier (disabled) rejects all.
func (v *PlatformVerifier) Verify(token string) (*Principal, error) {
	if v == nil {
		return nil, errors.New("auth: platform authentication disabled")
	}
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return nil, errors.New("auth: malformed platform token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return nil, errors.New("auth: bad platform token payload")
	}
	rawSig, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return nil, errors.New("auth: bad platform token signature")
	}
	if !ed25519.Verify(v.pub, payload, rawSig) {
		return nil, errors.New("auth: platform token signature mismatch")
	}
	var c platformClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, errors.New("auth: bad platform token claims")
	}
	if c.Iss != "super-admin" {
		return nil, errors.New("auth: unexpected platform token issuer")
	}
	now := time.Now().Unix()
	if c.Exp != 0 && now >= c.Exp {
		return nil, errors.New("auth: platform token expired")
	}
	if c.Iat != 0 && now < c.Iat-60 {
		return nil, errors.New("auth: platform token not yet valid")
	}
	return &Principal{Scope: ScopePlatform, UserPublicID: c.Sub}, nil
}

// platformCredential extracts the token from an "Authorization: Platform <token>"
// header, or "" if the header is absent or uses a different scheme.
func platformCredential(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Platform "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}
