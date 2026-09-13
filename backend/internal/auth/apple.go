package auth

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// AppleVerifier completes Sign in with Apple for a Services ID (web): it mints the
// short-lived client-secret JWT from the .p8 key, exchanges the authorization code,
// then verifies the returned id_token against Apple's JWKS (iss/aud/exp/sub/nonce).
//
// The .p8 private key and generated client secret never leave this process — the
// browser only ever holds the public Services ID (client_id), matching how GitHub
// keeps its client secret server-side.

const (
	appleTokenURL = "https://appleid.apple.com/auth/token"
	appleCertsURL = "https://appleid.apple.com/auth/keys"
	appleIssuer   = "https://appleid.apple.com"
	// Apple allows client-secret JWTs up to 6 months; we mint fresh ones for ~20 minutes
	// so a leaked .env copy ages out quickly and we never need a rotation cron.
	appleClientSecretTTL = 20 * time.Minute
)

// AppleClaims is the subset of a verified Apple identity we use.
type AppleClaims struct {
	Sub             string
	Email           string
	EmailVerified   bool
	IsPrivateEmail  bool // Hide My Email relay — still a real deliverable address when verified
	Nonce           string
}

type AppleVerifier struct {
	clientID   string // Services ID (e. to. com.pyyol.web)
	teamID     string
	keyID      string
	privateKey *ecdsa.PrivateKey
	client     *http.Client

	mu          sync.Mutex
	keys        map[string]*rsa.PublicKey
	fetched     time.Time
	ttl         time.Duration
	cachedSecret string
	secretExp    time.Time
}

// NewAppleVerifier builds a verifier from Apple Developer Console values.
// Any empty half disables Sign in with Apple (Enabled() == false). privateKeyPEM
// is the contents of the .p8 AuthKey file (BEGIN PRIVATE KEY); literal "\n"
// sequences are expanded so GitHub Actions secrets paste cleanly.
func NewAppleVerifier(clientID, teamID, keyID, privateKeyPEM string) (*AppleVerifier, error) {
	clientID = strings.TrimSpace(clientID)
	teamID = strings.TrimSpace(teamID)
	keyID = strings.TrimSpace(keyID)
	privateKeyPEM = strings.TrimSpace(privateKeyPEM)
	if clientID == "" && teamID == "" && keyID == "" && privateKeyPEM == "" {
		return &AppleVerifier{}, nil
	}
	if clientID == "" || teamID == "" || keyID == "" || privateKeyPEM == "" {
		return nil, errors.New("apple: APPLE_CLIENT_ID, APPLE_TEAM_ID, APPLE_KEY_ID, and APPLE_PRIVATE_KEY must all be set together")
	}
	key, err := parseApplePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, fmt.Errorf("apple: parse private key: %w", err)
	}
	return &AppleVerifier{
		clientID:   clientID,
		teamID:     teamID,
		keyID:      keyID,
		privateKey: key,
		client:     &http.Client{Timeout: 8 * time.Second},
		keys:       map[string]*rsa.PublicKey{},
		ttl:        time.Hour,
	}, nil
}

// Enabled reports whether Sign in with Apple is fully configured.
func (v *AppleVerifier) Enabled() bool {
	return v != nil && v.clientID != "" && v.privateKey != nil
}

// ClientID returns the Services ID (safe to expose to the browser as NEXT_PUBLIC_*).
func (v *AppleVerifier) ClientID() string {
	if v == nil {
		return ""
	}
	return v.clientID
}

func parseApplePrivateKey(pemOrEscaped string) (*ecdsa.PrivateKey, error) {
	s := strings.ReplaceAll(pemOrEscaped, `\n`, "\n")
	block, _ := pem.Decode([]byte(s))
	if block == nil {
		return nil, errors.New("no PEM block found")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("expected ECDSA private key (Apple .p8)")
	}
	return ec, nil
}

// clientSecret mints (and briefly caches) the ES256 JWT Apple requires as client_secret.
func (v *AppleVerifier) clientSecret() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.cachedSecret != "" && time.Now().Before(v.secretExp.Add(-2*time.Minute)) {
		return v.cachedSecret, nil
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{
		"iss": v.teamID,
		"iat": now.Unix(),
		"exp": now.Add(appleClientSecretTTL).Unix(),
		"aud": appleIssuer,
		"sub": v.clientID,
	})
	tok.Header["kid"] = v.keyID
	signed, err := tok.SignedString(v.privateKey)
	if err != nil {
		return "", err
	}
	v.cachedSecret = signed
	v.secretExp = now.Add(appleClientSecretTTL)
	return signed, nil
}

// Exchange turns an authorization code (+ optional PKCE verifier and expected nonce)
// into verified Apple claims. redirectURI must match the value used to obtain the code.
func (v *AppleVerifier) Exchange(ctx context.Context, code, redirectURI, codeVerifier, expectedNonce string) (AppleClaims, error) {
	if !v.Enabled() {
		return AppleClaims{}, errors.New("apple: login disabled")
	}
	if strings.TrimSpace(code) == "" {
		return AppleClaims{}, errors.New("apple: missing code")
	}

	idToken, err := v.exchangeCode(ctx, code, redirectURI, codeVerifier)
	if err != nil {
		return AppleClaims{}, err
	}
	return v.verifyIDToken(ctx, idToken, expectedNonce)
}

func (v *AppleVerifier) exchangeCode(ctx context.Context, code, redirectURI, codeVerifier string) (string, error) {
	secret, err := v.clientSecret()
	if err != nil {
		return "", fmt.Errorf("apple: client secret: %w", err)
	}
	form := url.Values{}
	form.Set("client_id", v.clientID)
	form.Set("client_secret", secret)
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	if redirectURI != "" {
		form.Set("redirect_uri", redirectURI)
	}
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, appleTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := v.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var out struct {
		IDToken          string `json:"id_token"`
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("apple: bad token response: %w", err)
	}
	if out.Error != "" {
		if out.ErrorDescription != "" {
			return "", fmt.Errorf("apple: %s (%s)", out.Error, out.ErrorDescription)
		}
		return "", fmt.Errorf("apple: %s", out.Error)
	}
	if out.IDToken == "" {
		return "", errors.New("apple: no id_token returned")
	}
	return out.IDToken, nil
}

type appleJWK struct {
	Kid string `json:"kid"`
	Kty string `json:"kty"`
	N   string `json:"n"`
	E   string `json:"e"`
	Alg string `json:"alg"`
}

func (v *AppleVerifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	if k := v.keys[kid]; k != nil && time.Since(v.fetched) < v.ttl {
		v.mu.Unlock()
		return k, nil
	}
	v.mu.Unlock()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, appleCertsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var doc struct {
		Keys []appleJWK `json:"keys"`
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
		return nil, errors.New("apple: unknown signing key")
	}
	return k, nil
}

func (v *AppleVerifier) verifyIDToken(ctx context.Context, idToken, expectedNonce string) (AppleClaims, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return AppleClaims{}, errors.New("apple: malformed token")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return AppleClaims{}, err
	}
	var hdr struct{ Alg, Kid string }
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return AppleClaims{}, err
	}
	if hdr.Alg != "RS256" {
		return AppleClaims{}, errors.New("apple: unexpected alg")
	}
	pk, err := v.key(ctx, hdr.Kid)
	if err != nil {
		return AppleClaims{}, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return AppleClaims{}, err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pk, crypto.SHA256, sum[:], sig); err != nil {
		return AppleClaims{}, errors.New("apple: bad signature")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return AppleClaims{}, err
	}
	// Apple sometimes encodes email_verified / is_private_email as strings.
	var c struct {
		Iss            string `json:"iss"`
		Aud            string `json:"aud"`
		Exp            int64  `json:"exp"`
		Iat            int64  `json:"iat"`
		Sub            string `json:"sub"`
		Email          string `json:"email"`
		EmailVerified  any    `json:"email_verified"`
		IsPrivateEmail any    `json:"is_private_email"`
		Nonce          string `json:"nonce"`
	}
	if err := json.Unmarshal(pb, &c); err != nil {
		return AppleClaims{}, err
	}
	if c.Iss != appleIssuer {
		return AppleClaims{}, errors.New("apple: bad issuer")
	}
	if c.Aud != v.clientID {
		return AppleClaims{}, errors.New("apple: audience mismatch")
	}
	const skew = 60 * time.Second
	now := time.Now()
	if now.After(time.Unix(c.Exp, 0).Add(skew)) {
		return AppleClaims{}, errors.New("apple: token expired")
	}
	if c.Iat > 0 && time.Unix(c.Iat, 0).After(now.Add(skew)) {
		return AppleClaims{}, errors.New("apple: token issued in the future")
	}
	if c.Sub == "" {
		return AppleClaims{}, errors.New("apple: missing subject")
	}
	if expectedNonce != "" {
		if !appleNonceMatches(expectedNonce, c.Nonce) {
			return AppleClaims{}, errors.New("apple: nonce mismatch")
		}
	}
	return AppleClaims{
		Sub:            c.Sub,
		Email:          c.Email,
		EmailVerified:  appleTruthy(c.EmailVerified),
		IsPrivateEmail: appleTruthy(c.IsPrivateEmail),
		Nonce:          c.Nonce,
	}, nil
}

// appleNonceMatches accepts the raw nonce (REST echo) or Apple JS-style SHA-256
// digests (hex or base64url) of that nonce.
func appleNonceMatches(expected, claim string) bool {
	if claim == "" {
		return false
	}
	if claim == expected {
		return true
	}
	sum := sha256.Sum256([]byte(expected))
	if claim == hex.EncodeToString(sum[:]) {
		return true
	}
	if claim == base64.RawURLEncoding.EncodeToString(sum[:]) {
		return true
	}
	return false
}

func appleTruthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return strings.EqualFold(t, "true") || t == "1"
	case float64:
		return t != 0
	default:
		return false
	}
}
