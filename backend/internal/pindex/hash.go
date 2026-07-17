package pindex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Hash is a stable content hash of the inputs, stored with each history row so a
// later recompute can prove it reproduced the same P-Index from the same inputs.
// Deterministic: struct field order is fixed and time.Time marshals canonically.
func (in DeveloperInputs) Hash() string {
	b, err := json.Marshal(in)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
