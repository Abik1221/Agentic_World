// Package platformsign is the message-authenticity primitive for the cross-service
// platform bus. Config snapshots and domain events crossing Redis between the
// Super Admin and the game engine are signed with Ed25519, so a component that
// can merely reach Redis cannot forge them — and, crucially, neither can the
// consumer, because it holds only the producer's PUBLIC key.
//
// Two keypairs, one per direction:
//   - Config:  Admin holds the private key (signs); engine holds the public key.
//   - Events:  Engine holds the private key (signs); Admin holds the public key.
//
// Each service therefore holds only its own private key plus the peer's public
// key, so compromising one service cannot forge the other's messages. Config
// drives money/fees/rewards, so "only the Super Admin can ever produce valid
// config, even if the engine is popped" is the property we want.
//
// The package is pure and dependency-free so both the store and platformcfg
// layers can use it without a driver import. Keys are base64 (std encoding):
// a 32-byte Ed25519 seed for private keys, a 32-byte public key.
package platformsign

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
)

// Signer signs outbound messages with an Ed25519 private key. A nil *Signer is
// valid and means "signing disabled" (dev/local): Sign returns "".
type Signer struct{ priv ed25519.PrivateKey }

// Verifier verifies inbound messages against an Ed25519 public key. A nil
// *Verifier means "verification disabled" (dev/local): Verify always accepts.
type Verifier struct{ pub ed25519.PublicKey }

// NewSigner builds a Signer from a base64-encoded 32-byte seed. An empty seed
// returns a nil Signer (signing disabled) with no error, so local/dev runs work
// unconfigured while callers log loudly.
func NewSigner(seedB64 string) (*Signer, error) {
	if seedB64 == "" {
		return nil, nil
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil {
		return nil, err
	}
	if len(seed) != ed25519.SeedSize {
		return nil, errors.New("platformsign: private key seed must be 32 bytes")
	}
	return &Signer{priv: ed25519.NewKeyFromSeed(seed)}, nil
}

// NewVerifier builds a Verifier from a base64-encoded 32-byte public key. An
// empty key returns a nil Verifier (verification disabled) with no error.
func NewVerifier(pubB64 string) (*Verifier, error) {
	if pubB64 == "" {
		return nil, nil
	}
	pub, err := base64.StdEncoding.DecodeString(pubB64)
	if err != nil {
		return nil, err
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, errors.New("platformsign: public key must be 32 bytes")
	}
	return &Verifier{pub: ed25519.PublicKey(pub)}, nil
}

// Sign returns the base64 Ed25519 signature of data, or "" when signing is
// disabled (nil Signer).
func (s *Signer) Sign(data []byte) string {
	if s == nil {
		return ""
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(s.priv, data))
}

// Enabled reports whether this signer will actually sign.
func (s *Signer) Enabled() bool { return s != nil }

// Verify reports whether sig is a valid Ed25519 signature for data. A nil
// Verifier (disabled) accepts everything; a malformed/empty sig fails.
func (v *Verifier) Verify(data []byte, sig string) bool {
	if v == nil {
		return true
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return ed25519.Verify(v.pub, data, raw)
}

// Enabled reports whether this verifier will actually check signatures.
func (v *Verifier) Enabled() bool { return v != nil }

// GenerateKeypair returns a fresh (seedB64, publicB64) pair for provisioning. The
// seed goes to the signing service's private-key env; the public to the peer.
func GenerateKeypair() (seedB64, publicB64 string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(priv.Seed()),
		base64.StdEncoding.EncodeToString(pub), nil
}
