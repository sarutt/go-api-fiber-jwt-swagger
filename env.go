package main

import (
	"os"
"github.com/gofiber/fiber/v2"
)

func getConfig(c *fiber.Ctx) error {
	// Example: Return a configuration value from environment variable
	secretKey := os.Getenv("SECRET_KEY")
	if secretKey == "" {
	  secretKey = "defaultSecret" // Default value if not specified
	}
  
	return c.JSON(fiber.Map{
	  "secret_key": secretKey,
	})
  }