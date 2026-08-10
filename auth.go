package main

import (
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v4"
)

// Roles carried in the JWT. Production agents get RoleAgent and may drive
// episodes between stages; only a RoleReviewer identity may record a decision
// at one of the two human gates, so an agent cannot approve its own work.
const (
	RoleReviewer = "reviewer"
	RoleAgent    = "agent"
)

// Account is a login. This is still the boilerplate's in-source account list
// rather than a user table — it carries a role so the gates can be enforced,
// but it must be replaced with real credential storage before production.
type Account struct {
	Email    string
	Password string
	Role     string
}

var accounts = []Account{
	{Email: "user@example.com", Password: "password123", Role: RoleReviewer},
	{Email: "agent@example.com", Password: "agent123", Role: RoleAgent},
}

// findAccount looks up an account by email and password.
func findAccount(email, password string) (Account, bool) {
	for _, account := range accounts {
		if account.Email == email && account.Password == password {
			return account, true
		}
	}
	return Account{}, false
}

// claims pulls the JWT claims the auth middleware stored on the request.
func claims(c *fiber.Ctx) jwt.MapClaims {
	token, ok := c.Locals("user").(*jwt.Token)
	if !ok || token == nil {
		return nil
	}
	mapClaims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil
	}
	return mapClaims
}

// claimString reads a string claim, returning "" when it is absent.
func claimString(c *fiber.Ctx, key string) string {
	value, ok := claims(c)[key].(string)
	if !ok {
		return ""
	}
	return value
}

// currentRole is the role of the caller.
func currentRole(c *fiber.Ctx) string {
	return claimString(c, "role")
}

// currentSubject is the identity of the caller, used to attribute gate
// decisions and stage changes to whoever actually made them.
func currentSubject(c *fiber.Ctx) string {
	return claimString(c, "sub")
}

// requireRole refuses a request whose token does not carry the given role.
func requireRole(role string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if currentRole(c) != role {
			return c.Status(fiber.StatusForbidden).JSON(ErrorResponse{
				Error:   "Forbidden",
				Message: "this endpoint requires the " + role + " role",
			})
		}
		return c.Next()
	}
}
