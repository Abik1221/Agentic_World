package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"
)

// REPLAY PROTECTION FOR GOOGLE ID TOKENS.
//
// The verifier already checked signature, issuer, audience and expiry — everything except
// that the token was minted for THIS sign-in attempt. OpenID Connect (§3.1.3.7) requires a
// nonce for exactly that reason: without one, a Google ID token is a bearer credential valid
// for its full ~1 hour, so anything that ever observes one — a logged request body, a
// browser extension, an XSS payload, a shared analytics pipeline — can replay it and get a
// session. Audience checking does not help, because a replayed token has the right audience.
//
// WHY IT IS SIGNED AND NOT STORED. A single-use nonce needs a store, and a store on the
// sign-in path is a new failure mode: if Redis is down, nobody can log in. A nonce that is an
// HMAC over (random value, expiry) is verifiable from the secret alone — no state, no
// dependency, no eviction policy — and bounds a replay to the five-minute window rather than
// the token's hour. That is the right trade for an authentication path that must not acquire
// new reasons to be unavailable.
//
// It is NOT single-use, and that is the honest limitation: within one window a captured
// (nonce, token) pair can be replayed. Closing that needs a store; the note below says so
// rather than implying more than the mechanism delivers.
//
// The secret is the JWT signing key. Reusing it is safe here because the payloads are
// domain-separated by a fixed prefix, so a nonce can never be mistaken for a session token
// and vice versa.

const (
	// nonceTTL bounds a replay. Long enough for a human to complete a Google account
	// chooser (including creating an account mid-flow), short enough that a captured pair
	// is worthless by the time it reaches an attacker.
	nonceTTL = 5 * time.Minute
	// noncePrefix domain-separates this MAC from every other use of the signing key.
	noncePrefix = "pyyol-google-nonce-v1"
	nonceBytes  = 16
)

// NonceIssuer mints and verifies sign-in nonces.
type NonceIssuer struct{ secret []byte }

// NewNonceIssuer builds one from the server signing key. An empty secret disables nonce
// enforcement, which is what keeps a deployment that has not been reconfigured working —
// see GoogleVerifier.Verify for how that is surfaced.
func NewNonceIssuer(secret string) *NonceIssuer { return &NonceIssuer{secret: []byte(secret)} }

// Enabled reports whether nonces can be issued and required.
func (n *NonceIssuer) Enabled() bool { return len(n.secret) > 0 }

// Issue returns an opaque nonce for the client to pass to Google Identity Services, which
// echoes it into the ID token's `nonce` claim.
//
// Shape: <base64url(random)>.<expiry unix>.<base64url(mac)> — the expiry travels in the clear
// so Verify can reject a stale nonce without a lookup, and it is inside the MAC so it cannot
// be extended.
func (n *NonceIssuer) Issue() (string, error) {
	if !n.Enabled() {
		return "", errors.New("nonce: not configured")
	}
	raw := make([]byte, nonceBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	body := base64.RawURLEncoding.EncodeToString(raw) + "." +
		strconv.FormatInt(time.Now().Add(nonceTTL).Unix(), 10)
	return body + "." + base64.RawURLEncoding.EncodeToString(n.mac(body)), nil
}

// Verify checks a nonce echoed back inside an ID token: correct MAC and not expired.
func (n *NonceIssuer) Verify(nonce string) error {
	if !n.Enabled() {
		return errors.New("nonce: not configured")
	}
	i := strings.LastIndexByte(nonce, '.')
	if i <= 0 {
		return errors.New("nonce: malformed")
	}
	body, sig := nonce[:i], nonce[i+1:]
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return errors.New("nonce: malformed signature")
	}
	// Constant-time: a byte-by-byte comparison here would leak the expected MAC one
	// request at a time.
	if !hmac.Equal(got, n.mac(body)) {
		return errors.New("nonce: bad signature")
	}
	j := strings.LastIndexByte(body, '.')
	if j <= 0 {
		return errors.New("nonce: malformed body")
	}
	exp, err := strconv.ParseInt(body[j+1:], 10, 64)
	if err != nil {
		return errors.New("nonce: malformed expiry")
	}
	// Checked AFTER the MAC, so an attacker cannot use timing on the expiry to probe.
	if time.Now().Unix() >= exp {
		return errors.New("nonce: expired")
	}
	return nil
}

func (n *NonceIssuer) mac(body string) []byte {
	m := hmac.New(sha256.New, n.secret)
	m.Write([]byte(noncePrefix))
	m.Write([]byte{0}) // separator: prefix and body cannot be confused for one another
	m.Write([]byte(body))
	return m.Sum(nil)
}
