package monopoly

import (
	"crypto/sha256"
	"encoding/hex"
)

// Commit returns the public commitment to a match seed: sha256(seed), hex-encoded.
// It is published in the match_created event before any dice are rolled; the seed
// is revealed only at settlement, letting anyone verify the dice rolls and card
// decks were fixed in advance and not adapted to any player's choices.
func Commit(seed []byte) string {
	sum := sha256.Sum256(seed)
	return hex.EncodeToString(sum[:])
}

// VerifyCommit reports whether seed matches a previously published commit. The
// compared values are public (a hash and its preimage commitment), so a plain
// comparison is sufficient.
func VerifyCommit(seed []byte, commit string) bool {
	return Commit(seed) == commit
}
