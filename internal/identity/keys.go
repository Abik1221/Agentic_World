package identity

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"

	"github.com/agent-arena/arena/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

// API key shape: "sk_arena_<lookup>_<secret>".
//   - lookup: a public, unique segment used to find the key row (stored plaintext)
//   - secret: the part that is bcrypt-hashed (with a server pepper) and never stored
// Lookup keeps authentication O(1) (index on prefix) without scanning hashes.
var keyEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

const (
	lookupBytes = 8  // ~13 base32 chars
	secretBytes = 24 // ~39 base32 chars
)

type generatedKey struct {
	Raw    string // shown to the caller exactly once
	Prefix string // "sk_arena_<lookup>" — stored for lookup, unique among live keys
	Hash   string // bcrypt(secret+pepper) — stored
}

func generateKey(pepper string) (generatedKey, error) {
	lookup, err := randToken(lookupBytes)
	if err != nil {
		return generatedKey{}, err
	}
	secret, err := randToken(secretBytes)
	if err != nil {
		return generatedKey{}, err
	}
	prefix := platform.PrefixKey + "_" + lookup
	raw := prefix + "_" + secret
	hash, err := bcrypt.GenerateFromPassword([]byte(secret+pepper), bcrypt.DefaultCost)
	if err != nil {
		return generatedKey{}, err
	}
	return generatedKey{Raw: raw, Prefix: prefix, Hash: string(hash)}, nil
}

// splitKey parses a presented raw key into its lookup prefix and secret.
func splitKey(raw string) (prefix, secret string, err error) {
	rest, ok := strings.CutPrefix(raw, platform.PrefixKey+"_")
	if !ok {
		return "", "", errors.New("malformed api key")
	}
	lookup, secret, ok := strings.Cut(rest, "_")
	if !ok || lookup == "" || secret == "" {
		return "", "", errors.New("malformed api key")
	}
	return platform.PrefixKey + "_" + lookup, secret, nil
}

// verifySecret constant-time-compares a presented secret against the stored hash.
func verifySecret(hash, secret, pepper string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret+pepper)) == nil
}

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(keyEnc.EncodeToString(b)), nil
}
