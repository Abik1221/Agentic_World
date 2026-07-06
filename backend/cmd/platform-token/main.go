// Command platform-token mints a short-lived Ed25519 "Platform" service token —
// the same credential the Super Admin backend sends as `Authorization: Platform
// <token>` to reach the arena's /v1/admin/* routes. Dev/ops helper for exercising
// the admin surface without standing up the full admin backend.
//
//	PLATFORM_ADMIN_PRIVATE_KEY=<seed> go run ./cmd/platform-token [sub] [ttl]
//
// sub defaults to "super-admin-cli"; ttl (e.g. 5m) defaults to 5 minutes. The
// private key is the base64 32-byte seed printed by cmd/platform-bus-keygen.
package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

type claims struct {
	Iss string `json:"iss"`
	Sub string `json:"sub"`
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
}

func main() {
	seedB64 := os.Getenv("PLATFORM_ADMIN_PRIVATE_KEY")
	if seedB64 == "" {
		fmt.Fprintln(os.Stderr, "platform-token: set PLATFORM_ADMIN_PRIVATE_KEY (base64 32-byte seed)")
		os.Exit(1)
	}
	seed, err := base64.StdEncoding.DecodeString(seedB64)
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Fprintln(os.Stderr, "platform-token: PLATFORM_ADMIN_PRIVATE_KEY must be a base64 32-byte seed:", err)
		os.Exit(1)
	}
	sub := "super-admin-cli"
	if len(os.Args) > 1 {
		sub = os.Args[1]
	}
	ttl := 5 * time.Minute
	if len(os.Args) > 2 {
		if d, err := time.ParseDuration(os.Args[2]); err == nil {
			ttl = d
		}
	}

	priv := ed25519.NewKeyFromSeed(seed)
	now := time.Now()
	payload, err := json.Marshal(claims{Iss: "super-admin", Sub: sub, Iat: now.Unix(), Exp: now.Add(ttl).Unix()})
	if err != nil {
		fmt.Fprintln(os.Stderr, "platform-token: marshal:", err)
		os.Exit(1)
	}
	sig := ed25519.Sign(priv, payload)
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig)
	fmt.Println(token)
}
