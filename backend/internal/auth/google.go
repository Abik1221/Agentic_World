package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// GoogleVerifier validates Google Identity Services ID tokens (the `credential` a
// GIS "Sign in with Google" button returns). It verifies the RS256 signature against
// Google's rotating JWKS and checks iss/aud/exp — stdlib only, no third-party dep.
// Only the OAuth *Web client id* is needed (the token's `aud`); no client secret.

const googleCertsURL = "https://www.googleapis.com/oauth2/v3/certs"

var googleIssuers = map[string]bool{
	"accounts.google.com":         true,
	"https://accounts.google.com": true,
}

// GoogleClaims is the subset of a verified Google ID token we use.
type GoogleClaims struct {
	Sub           string
	Email         string
	EmailVerified bool
	Name          string
	// Nonce is the value the client bound this sign-in to. Empty when the client did not
	// send one — which Verify treats as a failure whenever nonce enforcement is on.
	Nonce string
}

type GoogleVerifier struct {
	clientID string
	// nonces enforces OIDC §3.1.3.7 replay protection. Nil leaves it off, which is only
	// correct for a deployment that has not been reconfigured yet — see Verify.
	nonces *NonceIssuer
	client   *http.Client
	mu       sync.Mutex
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
	ttl      time.Duration
}

func NewGoogleVerifier(clientID string) *GoogleVerifier {
	return &GoogleVerifier{
		clientID: clientID,
		client:   &http.Client{Timeout: 5 * time.Second},
		keys:     map[string]*rsa.PublicKey{},
		ttl:      time.Hour,
	}
}

// Enabled reports whether Google login is configured (a client id is set).
func (v *GoogleVerifier) Enabled() bool { return v.clientID != "" }

// RequireNonce turns on replay protection. Separate from the constructor so an existing
// deployment keeps working across the rollout: until the client is shipping nonces, calling
// this would reject every sign-in. Wire it once the frontend requests one.
func (v *GoogleVerifier) RequireNonce(n *NonceIssuer) { v.nonces = n }

// NonceRequired reports whether tokens must carry a validated nonce.
func (v *GoogleVerifier) NonceRequired() bool { return v.nonces != nil && v.nonces.Enabled() }

type googleJWK struct {
	Kid string `json:"kid"`
	N   string `json:"n"`
	E   string `json:"e"`
	Kty string `json:"kty"`
}

func (v *GoogleVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	if k := v.keys[kid]; k != nil && time.Since(v.fetched) < v.ttl {
		v.mu.Unlock()
		return k, nil
	}
	v.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, googleCertsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc struct {
		Keys []googleJWK `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return nil, err
	}
	keys := make(map[string]*rsa.PublicKey, len(doc.Keys))
	for _, j := range doc.Keys {
		if j.Kty != "RSA" {
			continue
		}
		if pk, err := rsaFromJWK(j.N, j.E); err == nil {
			keys[j.Kid] = pk
		}
	}
	v.mu.Lock()
	v.keys, v.fetched = keys, time.Now()
	k := v.keys[kid]
	v.mu.Unlock()
	if k == nil {
		return nil, errors.New("google: unknown signing key")
	}
	return k, nil
}

func rsaFromJWK(nB64, eB64 string) (*rsa.PublicKey, error) {
	nb, err := base64.RawURLEncoding.DecodeString(nB64)
	if err != nil {
		return nil, err
	}
	eb, err := base64.RawURLEncoding.DecodeString(eB64)
	if err != nil {
		return nil, err
	}
	e := 0
	for _, b := range eb {
		e = e<<8 | int(b)
	}
	return &rsa.PublicKey{N: new(big.Int).SetBytes(nb), E: e}, nil
}

// Verify checks the ID token's signature + iss/aud/exp and returns its claims.
func (v *GoogleVerifier) Verify(ctx context.Context, idToken string) (GoogleClaims, error) {
	if v.clientID == "" {
		return GoogleClaims{}, errors.New("google: login disabled")
	}
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return GoogleClaims{}, errors.New("google: malformed token")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return GoogleClaims{}, err
	}
	var hdr struct{ Alg, Kid string }
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return GoogleClaims{}, err
	}
	if hdr.Alg != "RS256" {
		return GoogleClaims{}, errors.New("google: unexpected alg")
	}
	pk, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return GoogleClaims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return GoogleClaims{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pk, crypto.SHA256, sum[:], sig); err != nil {
		return GoogleClaims{}, errors.New("google: bad signature")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return GoogleClaims{}, err
	}
	var c struct {
		Iss           string `json:"iss"`
		Aud           string `json:"aud"`
		Exp           int64  `json:"exp"`
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
		Name          string `json:"name"`
		Nonce         string `json:"nonce"`
		Iat           int64  `json:"iat"`
	}
	if err := json.Unmarshal(pb, &c); err != nil {
		return GoogleClaims{}, err
	}
	if !googleIssuers[c.Iss] {
		return GoogleClaims{}, errors.New("google: bad issuer")
	}
	if c.Aud != v.clientID {
		return GoogleClaims{}, errors.New("google: audience mismatch")
	}
	// A small skew allowance in BOTH directions. Google's clock and ours are not the same
	// clock, and a token rejected for being one second old is a sign-in failure a user can
	// do nothing about; without the iat bound, a token minted far in the future would be
	// accepted for as long as its exp allowed.
	const skew = 60 * time.Second
	now := time.Now()
	if now.After(time.Unix(c.Exp, 0).Add(skew)) {
		return GoogleClaims{}, errors.New("google: token expired")
	}
	if c.Iat > 0 && time.Unix(c.Iat, 0).After(now.Add(skew)) {
		return GoogleClaims{}, errors.New("google: token issued in the future")
	}
	if c.Sub == "" {
		return GoogleClaims{}, errors.New("google: missing subject")
	}
	// REPLAY PROTECTION. A token that is otherwise perfectly valid is still only good for
	// the sign-in it was minted for. Checked last: the cheap structural failures should not
	// be reported as nonce problems.
	if v.NonceRequired() {
		if c.Nonce == "" {
			return GoogleClaims{}, errors.New("google: token carries no nonce")
		}
		if err := v.nonces.Verify(c.Nonce); err != nil {
			return GoogleClaims{}, errors.New("google: " + err.Error())
		}
	}
	return GoogleClaims{
		Sub: c.Sub, Email: c.Email, EmailVerified: c.EmailVerified, Name: c.Name, Nonce: c.Nonce,
	}, nil
}
