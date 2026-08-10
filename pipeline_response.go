package main

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
)

// badRequest, notFound and conflict return the standard error body used by
// every pipeline endpoint, matching the shape the JWT middleware already
// returns for auth failures.
func badRequest(c *fiber.Ctx, message string) error {
	return c.Status(fiber.StatusBadRequest).JSON(ErrorResponse{Error: "Bad Request", Message: message})
}

func notFound(c *fiber.Ctx, message string) error {
	return c.Status(fiber.StatusNotFound).JSON(ErrorResponse{Error: "Not Found", Message: message})
}

// conflict is for a request that is well formed but not legal right now, such
// as a state transition the pipeline does not allow from the current stage.
func conflict(c *fiber.Ctx, message string) error {
	return c.Status(fiber.StatusConflict).JSON(ErrorResponse{Error: "Conflict", Message: message})
}

func serverError(c *fiber.Ctx, message string) error {
	return c.Status(fiber.StatusInternalServerError).JSON(ErrorResponse{Error: "Internal Server Error", Message: message})
}

// paramID reads a numeric :id route parameter.
func paramID(c *fiber.Ctx) (uint, error) {
	id, err := strconv.ParseUint(c.Params("id"), 10, 64)
	if err != nil {
		return 0, err
	}
	return uint(id), nil
}
