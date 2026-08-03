package auth

import (
	"net"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/limiter"
)

// ClientIP returns the caller's address, reading X-Forwarded-For from the RIGHT.
//
// Neither of the obvious options is correct here. Fiber's c.IP() returns the socket peer,
// which behind nginx is always nginx — so every caller in the world would share one
// bucket and any single client could exhaust it for everyone. Fiber's ProxyHeader mode
// returns the LEFT-most X-Forwarded-For entry, which is whatever the client sent, so an
// attacker gets a fresh bucket per request by varying a header.
//
// trustedHops counted from the right lands on the address our own proxy observed, which
// the client cannot forge. Falls back to the socket peer when there is no header — not
// spoofable, just coarse.
func ClientIP(c *fiber.Ctx, trustedHops int) string {
	if trustedHops < 1 {
		trustedHops = 1
	}
	if xff := c.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		n := trustedHops
		if n > len(parts) {
			n = len(parts)
		}
		if ip := strings.TrimSpace(parts[len(parts)-n]); ip != "" {
			return ip
		}
	}
	if host, _, err := net.SplitHostPort(c.Context().RemoteAddr().String()); err == nil {
		return host
	}
	return c.IP()
}

// RateLimit bounds requests per caller per minute.
//
// On these APIs the shared key is the primary control and this is the backstop behind it.
// It matters for two things the key cannot do on its own: it caps how fast an
// unauthenticated caller can guess the key, and it caps the damage a LEAKED key can do
// before someone notices — an unlimited valid key can drain the whole trace store, or
// bury real telemetry under forged spans, as fast as the network allows.
//
// Skips /health so liveness probes and the web topbar's status chips are never throttled
// into reporting a healthy service as down.
func RateLimit(perMinute, trustedHops int) fiber.Handler {
	return limiter.New(limiter.Config{
		Max:        perMinute,
		Expiration: time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return ClientIP(c, trustedHops)
		},
		Next: func(c *fiber.Ctx) bool {
			return strings.HasPrefix(c.Path(), "/health")
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{"error": "rate limited"})
		},
	})
}
