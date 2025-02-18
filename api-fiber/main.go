package main

import (
	"os"
	"log"
	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"

	"github.com/gofiber/jwt/v2"

	"github.com/gofiber/fiber/v2/middleware/cors"
	
	"github.com/gofiber/swagger"
	_"gitlhub.com/sarutt/apifiber/docs"
)

// @title Book API
// @description This is a sample server for a book API.
// @version 1.0
// @host localhost:8080
// @BasePath /
// @schemes http
// @securityDefinitions.apikey ApiKeyAuth
// @in header
// @name Authorization
func main() {

app := fiber.New()

// Apply CORS middleware
app.Use(cors.New(cors.Config{
    AllowOrigins: "*", // ใส่ URL ของ frontend คุณ
    AllowMethods: "GET,POST,HEAD,PUT,DELETE,PATCH",
    AllowHeaders: "Origin, Content-Type, Accept, Authorization",
}))

app.Get("/swagger/*", swagger.HandlerDefault) // default swagger

err := godotenv.Load()
if err != nil {
    log.Fatal("Error loading .env file")
}

// ดึงค่า secretKey จากไฟล์ .env
secretKey := os.Getenv("SECRET_KEY")

if secretKey == "" {
    log.Fatal("SECRET_KEY is not set in .env file")
}

// Login route
app.Post("/login", login(secretKey))

// JWT Middleware
app.Use(jwtware.New(jwtware.Config{
    SigningKey: []byte(secretKey),
	ErrorHandler: func(c *fiber.Ctx, err error) error {
        if err != nil {
            return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
                "error": "Unauthorized",
                "message": err.Error(),
            })
        }
        return c.Next()
    },
}))
  
  app.Get("/books", getBooks)
  app.Get("/books/:id",getBook)
  app.Post("/books",createBook)
  app.Put("/books/:id", updateBook)
  app.Delete("/books/:id", deleteBook)

app.Post("/upload",uploadFile)

app.Get("/config",getConfig)

  app.Listen(":8080")

}

