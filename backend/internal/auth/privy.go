package auth

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// PrivyVerifier verifies a Privy access token — the front-door credential in the
// Beta wallet pipeline. Privy owns authentication (social / email / external +
// embedded Solana wallets) and issues a short-lived ES256 access token per app.
// We verify it with the app's Privy "verification key" (an ECDSA P-256 public key
// copied from the Privy dashboard), then exchange the proven Privy identity for
// our own dashboard JWT (see identity.UpsertFromPrivy). The Privy token is used
// ONLY at the login-exchange endpoint, never as a per-request credential — the
// rest of the system keeps using the existing user-scope JWT unchanged.
//
// A nil *PrivyVerifier means Privy auth is disabled (no key configured); like
// PlatformVerifier it rejects every token rather than panicking.
type PrivyVerifier struct {
	pub   *ecdsa.PublicKey
	appID string
}

// NewPrivyVerifier builds a verifier from the Privy app id and its PEM-encoded
// ES256 verification key. If either is empty it returns (nil, nil): Privy auth
// stays disabled and the existing credential paths are unaffected.
func NewPrivyVerifier(appID, verificationKeyPEM string) (*PrivyVerifier, error) {
	if strings.TrimSpace(appID) == "" || strings.TrimSpace(verificationKeyPEM) == "" {
		return nil, nil
	}
	pub, err := jwt.ParseECPublicKeyFromPEM([]byte(verificationKeyPEM))
	if err != nil {
		return nil, fmt.Errorf("auth: parse privy verification key: %w", err)
	}
	return &PrivyVerifier{pub: pub, appID: appID}, nil
}

// Enabled reports whether a Privy verification key is configured.
func (v *PrivyVerifier) Enabled() bool { return v != nil }

// PrivyIdentity is the cryptographically proven part of a Privy access token: the
// stable Privy user id (a DID, e.g. "did:privy:..."). Linked accounts (email,
// wallet, social handles) are NOT in the access token; they are supplied as
// display hints by the client at login and re-verified where they matter.
type PrivyIdentity struct {
	UserID string
}

// Verify checks a Privy access token's signature (ES256), issuer ("privy.io"),
// audience (our app id) and expiry, returning the proven Privy user id. A nil
// verifier (disabled) rejects all tokens.
func (v *PrivyVerifier) Verify(token string) (PrivyIdentity, error) {
	if v == nil {
		return PrivyIdentity{}, errors.New("auth: privy authentication disabled")
	}
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodECDSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return v.pub, nil
	}, jwt.WithIssuer("privy.io"), jwt.WithAudience(v.appID), jwt.WithExpirationRequired())
	if err != nil {
		return PrivyIdentity{}, err
	}
	if claims.Subject == "" {
		return PrivyIdentity{}, errors.New("auth: privy token missing subject")
	}
	return PrivyIdentity{UserID: claims.Subject}, nil
}
