package identity

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
	"unicode"
)

// slugify converts an agent name into a URL-safe, unique-ish slug:
// "Deep Bluff v3" -> "deep-bluff-v3-4k2p". A short random suffix avoids
// collisions; the DB's UNIQUE(slug) is the final guard.
func slugify(name string) string {
	var b strings.Builder
	lastDash := true // suppress leading dash
	for _, r := range strings.ToLower(name) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case !lastDash:
			b.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(b.String(), "-")
	if base == "" {
		base = "agent"
	}
	return base + "-" + randSuffix()
}

func randSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b))[:4]
}
