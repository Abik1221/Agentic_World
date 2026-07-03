// Package secretbox provides authenticated symmetric encryption (AES-256-GCM)
// for small secrets stored at rest — currently the developer-supplied bearer
// token the platform presents when calling an agent's endpoint.
//
// The key is derived from an operator-configured secret via SHA-256, so any
// sufficiently strong secret string yields a valid 32-byte AES-256 key. Each
// Seal uses a fresh random nonce prepended to the ciphertext; Open authenticates
// before decrypting, so tampering is detected. It is a pure leaf (stdlib crypto
// only, no I/O).
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
)

// ErrInvalidCiphertext is returned when a ciphertext is malformed or fails
// authentication (wrong key or tampered bytes).
var ErrInvalidCiphertext = errors.New("secretbox: invalid or tampered ciphertext")

// Cipher seals and opens secrets with a single derived AES-256-GCM key.
type Cipher struct {
	aead cipher.AEAD
}

// New derives an AES-256-GCM cipher from secret. secret must be non-empty; a
// short/weak secret still works but the operator is responsible for entropy.
func New(secret string) (*Cipher, error) {
	if secret == "" {
		return nil, errors.New("secretbox: secret must not be empty")
	}
	key := sha256.Sum256([]byte(secret)) // 32 bytes -> AES-256
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("secretbox: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secretbox: new gcm: %w", err)
	}
	return &Cipher{aead: aead}, nil
}

// Seal encrypts plaintext, returning nonce||ciphertext. Safe to store as-is.
func (c *Cipher) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, c.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("secretbox: nonce: %w", err)
	}
	// Seal appends the ciphertext to nonce, so the result is nonce||ct||tag.
	return c.aead.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal. It returns ErrInvalidCiphertext on any authentication or
// length failure (no oracle distinguishing the two).
func (c *Cipher) Open(sealed []byte) ([]byte, error) {
	ns := c.aead.NonceSize()
	if len(sealed) < ns {
		return nil, ErrInvalidCiphertext
	}
	nonce, ct := sealed[:ns], sealed[ns:]
	pt, err := c.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrInvalidCiphertext
	}
	return pt, nil
}
