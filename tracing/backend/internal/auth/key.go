// Package auth is the shared-secret gate in front of the telemetry APIs.
package auth

import (
	"crypto/subtle"

	"github.com/gofiber/fiber/v2"
)

// Match reports whether a caller-supplied key equals the expected one, failing CLOSED.
//
// Two properties, both deliberate:
//
// An EMPTY expected key rejects everything rather than accepting everything. A gate that
// opens when unconfigured is worse than no gate, because it looks like a gate: an unset
// key in a new environment silently exposes whatever it was meant to protect, and nothing
// in the logs says so.
//
// The comparison is constant-time. A byte-wise `!=` returns as soon as it finds a
// mismatch, so response latency leaks how much of a guessed prefix was correct, which
// turns brute-forcing the key from infeasible into linear in its length.
func Match(expected, got string) bool {
	if expected == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

// APIKey gates a route group on the X-Pyyol-Key header.
func APIKey(expected string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if !Match(expected, c.Get("X-Pyyol-Key")) {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}
