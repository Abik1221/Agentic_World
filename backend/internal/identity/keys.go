package identity

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
	"unicode"

	"github.com/agent-arena/arena/internal/platform"
	"golang.org/x/crypto/bcrypt"
)

// API key shape: "sk_arena_<lookup>_<secret>".
//   - lookup: a public, unique segment used to find the key row (stored plaintext)
//   - secret: the part that is bcrypt-hashed (with a server pepper) and never stored
//
// Lookup keeps authentication O(1) (index on prefix) without scanning hashes.
var keyEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

const (
	lookupBytes = 8  // ~13 base32 chars
	secretBytes = 24 // ~39 base32 chars
)

type generatedKey struct {
	Raw    string // shown to the caller exactly once
	Prefix string // "sk_arena_<lookup>" — stored for lookup, unique among live keys
	Hash   string // bcrypt(pepperedDigest(secret,pepper)) — stored
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
	// Pre-hash the secret through HMAC (like passwords, see password.go) before
	// bcrypt: the ~39-char secret plus a long server pepper would otherwise exceed
	// bcrypt's 72-byte input cap and fail EVERY key generation. The digest is a
	// fixed, bounded length regardless of pepper length.
	hash, err := bcrypt.GenerateFromPassword(pepperedDigest(secret, pepper), bcrypt.DefaultCost)
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
// It tries the current HMAC-digest scheme first, then falls back to the legacy
// raw secret+pepper scheme so keys minted before the digest change still verify.
func verifySecret(hash, secret, pepper string) bool {
	if bcrypt.CompareHashAndPassword([]byte(hash), pepperedDigest(secret, pepper)) == nil {
		return true
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(secret+pepper)) == nil
}

// MaxLiveKeysPerAgent caps how many keys an agent can have live at once. Labels
// make re-issuing for the same machine a REPLACE, so hitting this means genuinely
// distinct machines, and a developer with twenty live agent credentials has lost
// track of them. The cap is enforced in the issuing transaction, not here, so two
// concurrent issues cannot both squeeze past it.
const MaxLiveKeysPerAgent = 20

const maxKeyLabelLen = 40

// NormalizeKeyLabel cleans a caller-supplied device name into what gets stored, or
// reports why it cannot. Labels are identity in the key list — the owner revokes
// "ci-runner", not "sk_arena_4kd2…" — and they are also the REPLACE key, so
// "Macbook " and "macbook" must not become two slots for one machine: the label is
// lowercased and its whitespace collapsed.
//
// Control characters are stripped rather than rejected: they arrive from hostnames
// and shell interpolation, not from a person, and failing a login over an invisible
// byte would be the more confusing outcome.
func NormalizeKeyLabel(raw string) (string, error) {
	var b strings.Builder
	lastSpace := false
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if b.Len() > 0 && !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		case unicode.IsControl(r):
			// dropped
		default:
			b.WriteRune(unicode.ToLower(r))
			lastSpace = false
		}
	}
	label := strings.TrimSpace(b.String())
	if label == "" {
		return "", errInvalid("label is required — name the machine or deployment that will hold this key")
	}
	if len([]rune(label)) > maxKeyLabelLen {
		return "", errInvalid("label must be at most 40 characters")
	}
	return label, nil
}

func randToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToLower(keyEnc.EncodeToString(b)), nil
}
