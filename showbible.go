package main

import (
	"github.com/gofiber/fiber/v2"
)

// The show bible: the fixed cast, sets and curriculum backlog that every
// episode is generated against. Keeping these in one place is what stops the
// characters drifting in appearance and voice across the series.

// getCharacters godoc
// @Summary List the cast
// @Tags showbible
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {array} Character
// @Router /pipeline/characters [get]
func getCharacters(c *fiber.Ctx) error {
	var characters []Character
	if err := db.Order("id asc").Find(&characters).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(characters)
}

// getCharacter godoc
// @Summary Get one character
// @Tags showbible
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Character ID"
// @Success 200 {object} Character
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/characters/{id} [get]
func getCharacter(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var character Character
	if err := db.First(&character, id).Error; err != nil {
		return notFound(c, "character not found")
	}
	return c.JSON(character)
}

// createCharacter godoc
// @Summary Add a character to the cast
// @Tags showbible
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param character body Character true "Character"
// @Success 201 {object} Character
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/characters [post]
func createCharacter(c *fiber.Ctx) error {
	character := new(Character)
	if err := c.BodyParser(character); err != nil {
		return badRequest(c, err.Error())
	}
	if character.Name == "" {
		return badRequest(c, "name is required")
	}

	character.ID = 0
	if err := db.Create(character).Error; err != nil {
		return badRequest(c, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(character)
}

// updateCharacter godoc
// @Summary Update a character
// @Tags showbible
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Character ID"
// @Param character body Character true "Character fields to update"
// @Success 200 {object} Character
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/characters/{id} [put]
func updateCharacter(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var character Character
	if err := db.First(&character, id).Error; err != nil {
		return notFound(c, "character not found")
	}

	incoming := new(Character)
	if err := c.BodyParser(incoming); err != nil {
		return badRequest(c, err.Error())
	}

	character.Name = valueOr(incoming.Name, character.Name)
	character.Species = valueOr(incoming.Species, character.Species)
	character.Role = valueOr(incoming.Role, character.Role)
	character.ColorHex = valueOr(incoming.ColorHex, character.ColorHex)
	character.SignatureItem = valueOr(incoming.SignatureItem, character.SignatureItem)
	character.Teaches = valueOr(incoming.Teaches, character.Teaches)
	character.VoiceProfileID = valueOr(incoming.VoiceProfileID, character.VoiceProfileID)
	character.ReferenceSheetURL = valueOr(incoming.ReferenceSheetURL, character.ReferenceSheetURL)

	if err := db.Save(&character).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(character)
}

// getLocations godoc
// @Summary List the recurring sets
// @Tags showbible
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {array} Location
// @Router /pipeline/locations [get]
func getLocations(c *fiber.Ctx) error {
	var locations []Location
	if err := db.Order("id asc").Find(&locations).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(locations)
}

// createLocation godoc
// @Summary Add a set
// @Tags showbible
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param location body Location true "Location"
// @Success 201 {object} Location
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/locations [post]
func createLocation(c *fiber.Ctx) error {
	location := new(Location)
	if err := c.BodyParser(location); err != nil {
		return badRequest(c, err.Error())
	}
	if location.Name == "" {
		return badRequest(c, "name is required")
	}

	location.ID = 0
	if err := db.Create(location).Error; err != nil {
		return badRequest(c, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(location)
}

// getCurriculumTopics godoc
// @Summary List curriculum topics
// @Tags showbible
// @Produce json
// @Security ApiKeyAuth
// @Param theme query string false "Filter by theme" Enums(COUNTING,LETTERS,EMOTIONS,KINDNESS)
// @Param used query bool false "Filter by whether a topic has been used"
// @Success 200 {array} CurriculumTopic
// @Router /pipeline/curriculum-topics [get]
func getCurriculumTopics(c *fiber.Ctx) error {
	query := db.Order("difficulty_level asc, week_in_cycle asc")

	if theme := c.Query("theme"); theme != "" {
		query = query.Where("theme = ?", theme)
	}
	if used := c.Query("used"); used != "" {
		query = query.Where("used = ?", used == "true")
	}

	var topics []CurriculumTopic
	if err := query.Find(&topics).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(topics)
}

// getNextCurriculumTopic godoc
// @Summary Get the next unused curriculum topic
// @Description What the content idea agent calls to find the next teaching subject due in the rotation. Returns the lowest difficulty unused topic, following the four week cycle order.
// @Tags showbible
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} CurriculumTopic
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/curriculum-topics/next [get]
func getNextCurriculumTopic(c *fiber.Ctx) error {
	var topic CurriculumTopic
	err := db.Where("used = ?", false).
		Order("difficulty_level asc, week_in_cycle asc").
		First(&topic).Error
	if err != nil {
		return notFound(c, "no unused curriculum topics left: add more to the backlog")
	}
	return c.JSON(topic)
}

// createCurriculumTopic godoc
// @Summary Add a curriculum topic to the backlog
// @Tags showbible
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param topic body CurriculumTopic true "Curriculum topic"
// @Success 201 {object} CurriculumTopic
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/curriculum-topics [post]
func createCurriculumTopic(c *fiber.Ctx) error {
	topic := new(CurriculumTopic)
	if err := c.BodyParser(topic); err != nil {
		return badRequest(c, err.Error())
	}
	if topic.Title == "" {
		return badRequest(c, "title is required")
	}

	topic.ID = 0
	if err := db.Create(topic).Error; err != nil {
		return badRequest(c, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(topic)
}

// markCurriculumTopicUsed godoc
// @Summary Mark a curriculum topic as used
// @Description Called once an episode has been created from the topic, so the rotation moves on.
// @Tags showbible
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Curriculum topic ID"
// @Success 200 {object} CurriculumTopic
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/curriculum-topics/{id}/used [post]
func markCurriculumTopicUsed(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var topic CurriculumTopic
	if err := db.First(&topic, id).Error; err != nil {
		return notFound(c, "curriculum topic not found")
	}

	topic.Used = true
	if err := db.Save(&topic).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(topic)
}
