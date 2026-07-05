package identity

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/mail"
	"regexp"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// Password hashing for email + password sign-in.
//
// A password is never bcrypt-ed directly. It is first run through
// HMAC-SHA256 keyed by the server pepper, then base64-encoded, then bcrypt-ed:
//
//   bcrypt( base64( HMAC-SHA256(pepper, password) ) )
//
// This does two things:
//   - folds the secret pepper into the hash (so a stolen DB alone is not enough), and
//   - collapses arbitrary-length input to a fixed 44-char ASCII digest, so we never
//     hit bcrypt's 72-byte truncation limit and long passwords stay fully significant.
//
// The per-hash bcrypt salt still makes every stored hash unique, so two owners
// with the same password never share a hash.

const (
	minPasswordLen = 8
	maxPasswordLen = 200 // upper bound so a huge body can't be used to burn CPU
	maxEmailLen    = 254 // RFC 5321 practical maximum
)

// agentNameRe mirrors the client-side rule (Frontend/app/register): 3–32 chars,
// letters/digits/underscore/hyphen, case-insensitive.
var agentNameRe = regexp.MustCompile(`^[A-Za-z0-9_-]{3,32}$`)

// hashPassword returns a bcrypt hash suitable for storage in users.password_hash.
func hashPassword(password, pepper string) (string, error) {
	h, err := bcrypt.GenerateFromPassword(pepperedDigest(password, pepper), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(h), nil
}

// verifyPassword constant-time-checks a candidate password against a stored hash.
func verifyPassword(hash, password, pepper string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), pepperedDigest(password, pepper)) == nil
}

func pepperedDigest(password, pepper string) []byte {
	mac := hmac.New(sha256.New, []byte(pepper))
	mac.Write([]byte(password))
	// base64 keeps the value ASCII-safe (bcrypt treats input as a C string and
	// stops at the first NUL byte; a raw digest could contain one).
	return []byte(base64.StdEncoding.EncodeToString(mac.Sum(nil)))
}

// dummy is a valid bcrypt hash generated once, used to keep login timing roughly
// constant when an email does not exist (so we don't leak which emails are
// registered via response time).
var (
	dummyOnce sync.Once
	dummyHash []byte
)

// equalizeTiming runs a throwaway bcrypt comparison so the "unknown email" path
// costs about the same as a real password check.
func equalizeTiming(password, pepper string) {
	dummyOnce.Do(func() {
		if h, err := bcrypt.GenerateFromPassword(pepperedDigest("timing-equalizer", pepper), bcrypt.DefaultCost); err == nil {
			dummyHash = h
		}
	})
	if dummyHash != nil {
		_ = bcrypt.CompareHashAndPassword(dummyHash, pepperedDigest(password, pepper))
	}
}

// normalizeEmail trims, validates, and lowercases an email. ok is false when the
// input is not a single bare address.
func normalizeEmail(raw string) (email string, ok bool) {
	e := strings.TrimSpace(raw)
	if e == "" || len(e) > maxEmailLen {
		return "", false
	}
	addr, err := mail.ParseAddress(e)
	if err != nil || addr.Name != "" || !strings.EqualFold(addr.Address, e) {
		return "", false
	}
	// Require a dotted domain (mail.ParseAddress accepts dot-less atoms like
	// "user@localhost", which we don't want for public sign-up).
	at := strings.LastIndex(addr.Address, "@")
	if at <= 0 {
		return "", false
	}
	domain := addr.Address[at+1:]
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") {
		return "", false
	}
	return strings.ToLower(addr.Address), true
}

// validatePassword enforces the minimum-strength rules for a new password.
func validatePassword(pw string) error {
	switch {
	case len(pw) < minPasswordLen:
		return errInvalid("password must be at least 8 characters")
	case len(pw) > maxPasswordLen:
		return errInvalid("password is too long")
	}
	return nil
}

// validAgentName reports whether name matches the public agent-name rule.
func validAgentName(name string) bool {
	return agentNameRe.MatchString(name)
}
