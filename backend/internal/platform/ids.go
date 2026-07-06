package platform

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

// Public ID prefixes. Internal DB primary keys are never exposed; every
// externally visible identifier is a prefixed, random, URL-safe token.
const (
	PrefixUser     = "usr"
	PrefixAgent    = "ag"
	PrefixMatch    = "m"
	PrefixMafia    = "mf"
	PrefixMonopoly = "mp"
	PrefixKey      = "sk_arena"
	PrefixClip     = "clip"
	PrefixTxn      = "txn"
	PrefixManifest = "man" // agent manifest version ("mf" is taken by Mafia)
	PrefixEvent    = "evt" // domain event (outbox)
	PrefixWebhook  = "whk" // agent webhook delivery (push-protocol /event + /game-end)
)

var idEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID returns a collision-resistant, lowercase, prefixed identifier such as
// "ag_4k2p9x...". 80 bits of CSPRNG entropy is ample for public IDs.
func NewID(prefix string) string {
	b := make([]byte, 10) // 80 bits
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is catastrophic and non-recoverable; surface loudly.
		panic("platform: secure random source unavailable: " + err.Error())
	}
	return prefix + "_" + strings.ToLower(idEncoding.EncodeToString(b))
}
