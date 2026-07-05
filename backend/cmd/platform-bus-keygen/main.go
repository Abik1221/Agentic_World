// Command platform-bus-keygen prints two fresh Ed25519 keypairs for the
// cross-service platform bus: one for the config plane (Admin signs) and one for
// the event plane (engine signs). Distribute the values as labelled below — each
// service holds only its own private key plus the peer's public key.
//
//	go run ./cmd/platform-bus-keygen
package main

import (
	"fmt"
	"os"

	"github.com/agent-arena/arena/internal/platformsign"
)

func main() {
	adminSeed, adminPub, err := platformsign.GenerateKeypair()
	if err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	engineSeed, enginePub, err := platformsign.GenerateKeypair()
	if err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}

	fmt.Print(`# Platform bus signing keys — set the SAME values in both services.
# Config plane: the Super Admin signs config; the engine verifies it.
# Event plane:  the engine signs events; the Super Admin verifies them.

## Super Admin (admin backend) env:
PLATFORM_ADMIN_PRIVATE_KEY=` + adminSeed + `
PLATFORM_ENGINE_PUBLIC_KEY=` + enginePub + `

## Main Backend (game engine) env:
PLATFORM_ENGINE_PRIVATE_KEY=` + engineSeed + `
PLATFORM_ADMIN_PUBLIC_KEY=` + adminPub + `
`)
}
