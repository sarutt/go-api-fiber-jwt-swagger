package main

import (
	"github.com/gofiber/fiber/v2"
)

func uploadFile(c *fiber.Ctx) error{
	file, err := c.FormFile("image")

	if err != nil{
		return c.Status(fiber.StatusBadRequest).SendString(err.Error())
	}

	err = c.SaveFile(file,"./uploads/"+file.Filename)

	if err != nil{
		return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
	}

	return c.SendString("File uploaded successfully: " + file.Filename)

}