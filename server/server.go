package main

import (
	"github.com/gofiber/fiber/v2"
)

func main() {
	app := fiber.New(fiber.Config{
		Prefork: true,
	})

	app.Get("/*", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	app.Delete("/*", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	app.Post("/*", func(c *fiber.Ctx) error {
		body := string(c.Body())
		return c.SendString("[3,\"123\",\"hello\",{\"received\":" + body + "}]")
	})

	app.Listen(":8000")
}
