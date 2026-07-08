package blockchain

import (
	"crypto/rand"
	"math/big"
)

// base58Alphabet is the Bitcoin/Solana base58 alphabet.
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// EncodeBase58 encodes bytes to a base58 string (Solana pubkey encoding).
func EncodeBase58(input []byte) string {
	// Count leading zero bytes → leading '1's.
	zeros := 0
	for zeros < len(input) && input[zeros] == 0 {
		zeros++
	}
	num := new(big.Int).SetBytes(input)
	base := big.NewInt(58)
	mod := new(big.Int)
	var out []byte
	for num.Sign() > 0 {
		num.DivMod(num, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, base58Alphabet[0])
	}
	// Reverse (we built it little-endian).
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// NewReference returns a fresh 32-byte value base58-encoded as a Solana pubkey,
// for use as a Solana Pay reference key. It is just a unique, unpredictable
// marker (never an account that must exist on-chain), so random bytes suffice.
func NewReference() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return EncodeBase58(b), nil
}
