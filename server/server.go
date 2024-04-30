package main

import (
	"time"

	"github.com/gofiber/fiber/v2"
)

func main() {
	app := fiber.New()

	app.Get("/", func(c *fiber.Ctx) error {
		time.Sleep(1000)
		return c.SendString("Hello, Worlds!")
	})

	app.Listen(":5000")
}
