package auth

import "github.com/gofiber/fiber/v2"

func APIKey(expected string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Fail closed: an empty expected key must reject everything, not accept
		// everything (an unset INGEST_API_KEY would otherwise open ingest to
		// spoofed telemetry / arbitrary org injection). A caller key must also be
		// non-empty and match.
		key := c.Get("X-Pyyol-Key")
		if expected == "" || key == "" || key != expected {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}
