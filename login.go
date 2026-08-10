package main

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/golang-jwt/jwt/v4"
)

func login(secretKey string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		type LoginRequest struct {
			Email    string `json:"email"`
			Password string `json:"password"`
		}

		var request LoginRequest
		if err := c.BodyParser(&request); err != nil {
			return err
		}

		// Check credentials - In real world, you should check against a database
		account, ok := findAccount(request.Email, request.Password)
		if !ok {
			return fiber.ErrUnauthorized
		}

		// Create token
		token := jwt.New(jwt.SigningMethodHS256)

		// Set claims. sub and role are what the pipeline authorises against:
		// gate decisions are attributed to sub and restricted to reviewers.
		claims := token.Claims.(jwt.MapClaims)
		claims["sub"] = account.Email
		claims["role"] = account.Role
		claims["name"] = account.Email
		claims["exp"] = time.Now().Add(time.Hour * 72).Unix()

		// Generate encoded token
		t, err := token.SignedString([]byte(secretKey))
		if err != nil {
			return c.SendStatus(fiber.StatusInternalServerError)
		}

		return c.JSON(fiber.Map{"token": t, "role": account.Role})
	}
}
