// Command wallet-secret-encrypt seals the Solana hot-wallet private key at rest so
// it isn't stored as a plaintext env var (W3). It prints base64(secretbox
// ciphertext) — set that as SOLANA_HOT_WALLET_SECRET_ENC, and the same master key
// as SOLANA_HOT_WALLET_ENC_KEY, and the server decrypts it in memory at boot.
//
//	SOLANA_HOT_WALLET_ENC_KEY=<master> go run ./cmd/wallet-secret-encrypt <base58-secret>
//	# or pipe the secret on stdin:
//	SOLANA_HOT_WALLET_ENC_KEY=<master> echo -n <base58-secret> | go run ./cmd/wallet-secret-encrypt
//
// Keep the master key out of shell history / logs (use a file or a secret store).
package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/agent-arena/arena/internal/secretbox"
)

func main() {
	master := os.Getenv("SOLANA_HOT_WALLET_ENC_KEY")
	if strings.TrimSpace(master) == "" {
		fmt.Fprintln(os.Stderr, "error: SOLANA_HOT_WALLET_ENC_KEY (the master key) must be set")
		os.Exit(2)
	}
	var secret string
	if len(os.Args) > 1 {
		secret = strings.TrimSpace(os.Args[1])
	} else {
		b, _ := io.ReadAll(os.Stdin)
		secret = strings.TrimSpace(string(b))
	}
	if secret == "" {
		fmt.Fprintln(os.Stderr, "error: provide the base58 hot-wallet secret as an argument or on stdin")
		os.Exit(2)
	}
	c, err := secretbox.New(master)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	sealed, err := c.Seal([]byte(secret))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	fmt.Println(base64.StdEncoding.EncodeToString(sealed))
}
