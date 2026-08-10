package main

import (
	_ "embed"

	"github.com/gofiber/fiber/v2"
)

// dashboardHTML is the operator console. It is embedded rather than read from
// disk so the binary is the whole deployment, matching how the rest of this
// service ships.
//
//go:embed webui/index.html
var dashboardHTML string

// favicon is served so the browser's automatic request for it does not fall
// through to the JWT middleware and come back as a 401 in the console. Note
// that every other unmatched path still answers 401 rather than 404, because
// the JWT middleware is registered app-wide — which at least does not reveal
// which paths exist.
const favicon = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
	`<circle cx="16" cy="16" r="14" fill="#ff6b35"/>` +
	`<circle cx="11" cy="14" r="2.6" fill="#16202a"/>` +
	`<circle cx="21" cy="14" r="2.6" fill="#16202a"/>` +
	`</svg>`

// registerDashboard mounts the operator console. It must be called before the
// JWT middleware: the page itself is public, because it is what a person uses
// to sign in. Every API call it then makes carries the token and is checked
// exactly like any other client's.
func registerDashboard(app *fiber.App) {
	app.Get("/admin", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, fiber.MIMETextHTMLCharsetUTF8)
		return c.SendString(dashboardHTML)
	})

	app.Get("/favicon.svg", serveFavicon)
	app.Get("/favicon.ico", serveFavicon)
}

func serveFavicon(c *fiber.Ctx) error {
	c.Set(fiber.HeaderContentType, "image/svg+xml")
	c.Set(fiber.HeaderCacheControl, "public, max-age=86400")
	return c.SendString(favicon)
}
