package auth

import "github.com/gofiber/fiber/v2"

func APIKey(expected string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if c.Get("X-Pyyol-Key") != expected {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{"error": "unauthorized"})
		}
		return c.Next()
	}
}
