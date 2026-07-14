// Package totp implements RFC 6238 time-based one-time passwords (the standard
// "authenticator app" 6-digit codes) using only the standard library — no external
// dependency and no third-party service. It is the free, self-hosted second factor
// for step-up auth on money movement (withdrawals + withdrawal-wallet changes).
//
// Defaults match what Google Authenticator / Authy / 1Password expect: HMAC-SHA1,
// 6 digits, a 30-second period. Secrets are base32 (RFC 4648, no padding).
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	period = 30 // seconds per code
	digits = 6
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// GenerateSecret returns a fresh 20-byte (160-bit) secret, base32-encoded — the
// value embedded in the enrollment QR and stored (encrypted) for the user.
func GenerateSecret() (string, error) {
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return b32.EncodeToString(raw), nil
}

// hotp computes the RFC 4226 HMAC-SHA1 one-time password for a counter.
func hotp(secret []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	mac := hmac.New(sha1.New, secret)
	mac.Write(buf[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	bin := (uint32(sum[offset])&0x7f)<<24 |
		uint32(sum[offset+1])<<16 |
		uint32(sum[offset+2])<<8 |
		uint32(sum[offset+3])
	return fmt.Sprintf("%0*d", digits, bin%1_000_000)
}

// Code returns the TOTP code for secretB32 at time t.
func Code(secretB32 string, t time.Time) (string, error) {
	secret, err := decode(secretB32)
	if err != nil {
		return "", err
	}
	return hotp(secret, uint64(t.Unix()/period)), nil
}

// Validate reports whether code is valid for secretB32 at time t, tolerating +/-
// `skew` periods of clock drift (skew=1 ⇒ accepts the previous, current, and next
// 30s window). The comparison is constant-time. A malformed secret or non-6-digit
// code is rejected.
func Validate(secretB32, code string, t time.Time, skew int) bool {
	code = strings.TrimSpace(code)
	if len(code) != digits {
		return false
	}
	secret, err := decode(secretB32)
	if err != nil {
		return false
	}
	if skew < 0 {
		skew = 0
	}
	counter := t.Unix() / period
	for w := -skew; w <= skew; w++ {
		want := hotp(secret, uint64(counter+int64(w)))
		if subtle.ConstantTimeCompare([]byte(want), []byte(code)) == 1 {
			return true
		}
	}
	return false
}

// URI builds the otpauth:// provisioning URI the client renders as a QR code.
func URI(secretB32, issuer, account string) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", secretB32)
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprintf("%d", digits))
	q.Set("period", fmt.Sprintf("%d", period))
	return "otpauth://totp/" + label + "?" + q.Encode()
}

func decode(secretB32 string) ([]byte, error) {
	s := strings.ToUpper(strings.TrimSpace(secretB32))
	b, err := b32.DecodeString(s)
	if err != nil || len(b) == 0 {
		return nil, fmt.Errorf("totp: invalid secret")
	}
	return b, nil
}
