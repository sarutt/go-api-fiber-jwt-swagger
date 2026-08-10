package main

import (
	"github.com/gofiber/fiber/v2"
)

// getEpisodeAssets godoc
// @Summary List the files produced for an episode
// @Tags pipeline-assets
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param kind query string false "Filter by asset kind" Enums(SCRIPT,VOICEOVER,MUSIC,ANIMATION,MASTER_VIDEO,SHORTS_CUT,TIKTOK_CUT,THUMBNAIL)
// @Success 200 {array} Asset
// @Router /pipeline/episodes/{id}/assets [get]
func getEpisodeAssets(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	query := db.Where("episode_id = ?", id)
	if kind := c.Query("kind"); kind != "" {
		query = query.Where("kind = ?", kind)
	}

	var assets []Asset
	if err := query.Order("id asc").Find(&assets).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(assets)
}

// createAsset godoc
// @Summary Register a produced file against an episode
// @Description How a generation agent reports its output. The episode ID comes from the path, so an agent cannot file an asset against a different episode.
// @Tags pipeline-assets
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param asset body Asset true "Asset"
// @Success 201 {object} Asset
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/assets [post]
func createAsset(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}

	asset := new(Asset)
	if err := c.BodyParser(asset); err != nil {
		return badRequest(c, err.Error())
	}
	if asset.Kind == "" {
		return badRequest(c, "kind is required")
	}
	if asset.URI == "" {
		return badRequest(c, "uri is required")
	}

	asset.ID = 0
	asset.EpisodeID = episode.ID

	if err := db.Create(asset).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.Status(fiber.StatusCreated).JSON(asset)
}

// deleteAsset godoc
// @Summary Delete an asset
// @Tags pipeline-assets
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Asset ID"
// @Success 204 "Deleted"
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/assets/{id} [delete]
func deleteAsset(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	result := db.Delete(&Asset{}, id)
	if result.Error != nil {
		return serverError(c, result.Error.Error())
	}
	if result.RowsAffected == 0 {
		return notFound(c, "asset not found")
	}
	return c.SendStatus(fiber.StatusNoContent)
}
