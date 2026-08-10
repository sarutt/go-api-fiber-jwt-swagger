package main

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	jwtware "github.com/gofiber/jwt/v2"
)

// newPublicApp mirrors main.go's ordering: the console and favicon are
// registered before the JWT middleware, the pipeline API after it.
func newPublicApp(t *testing.T) *fiber.App {
	t.Helper()

	if err := setupDB("file::memory:"); err != nil {
		t.Fatalf("cannot set up test database: %v", err)
	}

	app := fiber.New()
	registerDashboard(app)
	app.Use(jwtware.New(jwtware.Config{
		SigningKey: []byte(testSecret),
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "Unauthorized"})
		},
	}))
	registerPipelineRoutes(app)
	return app
}

// The console has to be reachable without a token: it is the page a person
// signs in on, so putting it behind auth would lock everyone out.
func TestDashboardIsServedWithoutAToken(t *testing.T) {
	app := newPublicApp(t)

	res := call(t, app, http.MethodGet, "/admin", "", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("/admin returned %d without a token, want 200", res.StatusCode)
	}
	if contentType := res.Header.Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Errorf("/admin served %q, want HTML", contentType)
	}
}

func TestDashboardCarriesItsOwnMarkup(t *testing.T) {
	if !strings.Contains(dashboardHTML, "<title>") {
		t.Error("the embedded console is missing its markup")
	}
	// The console must not pull anything off the network: this service is
	// expected to run somewhere without outbound access.
	for _, remote := range []string{"http://", "https://", "//cdn", "//unpkg"} {
		if strings.Contains(dashboardHTML, remote) {
			t.Errorf("the console references a remote resource (%q); it must be self-contained", remote)
		}
	}
}

// The browser asks for a favicon on its own. Without a route it falls into
// the JWT middleware and answers 401, which shows up as a console error on
// an otherwise healthy page.
func TestFaviconIsServedWithoutAToken(t *testing.T) {
	app := newPublicApp(t)

	for _, path := range []string{"/favicon.ico", "/favicon.svg"} {
		res := call(t, app, http.MethodGet, path, "", nil)
		if res.StatusCode != http.StatusOK {
			t.Errorf("%s returned %d without a token, want 200", path, res.StatusCode)
		}
	}
}

// The console being public must not have opened up the API behind it.
func TestPipelineStaysProtectedAlongsideTheConsole(t *testing.T) {
	app := newPublicApp(t)

	res := call(t, app, http.MethodGet, "/pipeline/overview", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("/pipeline/overview returned %d without a token, want 401", res.StatusCode)
	}
}
