package main

import (
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

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

// uploadAsset godoc
// @Summary Upload a produced file
// @Description How a generation agent hands over bytes rather than a URI it hosts itself. Creates the asset record and stores the file in one call. The storage path is derived from the episode and asset, never from the uploaded filename.
// @Tags pipeline-assets
// @Accept multipart/form-data
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param kind formData string true "Asset kind" Enums(SCRIPT,VOICEOVER,MUSIC,ANIMATION,MASTER_VIDEO,SHORTS_CUT,TIKTOK_CUT,THUMBNAIL)
// @Param platform formData string false "Platform this cut targets"
// @Param generated_by formData string false "Worker that produced it"
// @Param duration_seconds formData int false "Duration, for audio and video"
// @Param file formData file true "The file"
// @Success 201 {object} Asset
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 413 {object} ErrorResponse
// @Failure 415 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/assets/upload [post]
func uploadAsset(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}

	kind := AssetKind(strings.ToUpper(c.FormValue("kind")))
	if kind == "" {
		return badRequest(c, "kind is required")
	}

	header, err := c.FormFile("file")
	if err != nil {
		return badRequest(c, "a file is required")
	}
	if header.Size > maxAssetBytes() {
		return c.Status(fiber.StatusRequestEntityTooLarge).JSON(ErrorResponse{
			Error:   "Payload Too Large",
			Message: fmt.Sprintf("the file is %d bytes, the limit is %d", header.Size, maxAssetBytes()),
		})
	}

	contentType := header.Header.Get("Content-Type")
	ext, allowed := extensionFor(contentType)
	if !allowed {
		return c.Status(fiber.StatusUnsupportedMediaType).JSON(ErrorResponse{
			Error:   "Unsupported Media Type",
			Message: fmt.Sprintf("%q is not a format this pipeline stores", contentType),
		})
	}

	asset := Asset{
		EpisodeID:       episode.ID,
		Kind:            kind,
		Platform:        c.FormValue("platform"),
		ContentType:     contentType,
		GeneratedBy:     valueOr(c.FormValue("generated_by"), currentSubject(c)),
		DurationSeconds: formInt(c, "duration_seconds"),
	}
	// The row is created first so its ID can key the stored file, which keeps
	// storage paths unique without trusting anything the caller sent.
	if err := db.Create(&asset).Error; err != nil {
		return serverError(c, err.Error())
	}

	file, err := header.Open()
	if err != nil {
		db.Delete(&Asset{}, asset.ID)
		return badRequest(c, err.Error())
	}
	defer file.Close()

	key := assetKey(episode.ID, asset.ID, kind, ext)
	written, err := store.Put(key, file)
	if err != nil {
		// Without this the pipeline would hold a row pointing at nothing.
		db.Delete(&Asset{}, asset.ID)
		if errors.Is(err, ErrAssetTooLarge) {
			return c.Status(fiber.StatusRequestEntityTooLarge).JSON(ErrorResponse{
				Error:   "Payload Too Large",
				Message: err.Error(),
			})
		}
		return serverError(c, err.Error())
	}

	asset.StorageKey = key
	asset.SizeBytes = written
	asset.URI = LocalScheme + key
	if err := db.Save(&asset).Error; err != nil {
		store.Delete(key)
		db.Delete(&Asset{}, asset.ID)
		return serverError(c, err.Error())
	}

	return c.Status(fiber.StatusCreated).JSON(asset)
}

// getAssetContent godoc
// @Summary Download an asset
// @Description Serves a file the pipeline stores. Assets registered as a URI the agent hosts elsewhere have nothing to serve and return 404.
// @Tags pipeline-assets
// @Produce octet-stream
// @Security ApiKeyAuth
// @Param id path int true "Asset ID"
// @Success 200 {file} binary
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/assets/{id}/content [get]
func getAssetContent(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var asset Asset
	if err := db.First(&asset, id).Error; err != nil {
		return notFound(c, "asset not found")
	}
	if asset.StorageKey == "" {
		return notFound(c, "this asset is hosted elsewhere: see its uri")
	}

	file, err := store.Get(asset.StorageKey)
	if err != nil {
		return notFound(c, "the stored file is missing")
	}

	c.Set(fiber.HeaderContentType, valueOr(asset.ContentType, fiber.MIMEOctetStream))
	return c.SendStream(file)
}

// deleteAsset godoc
// @Summary Delete an asset
// @Description Removes the record and, when the pipeline stored the file, the file itself.
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

	var asset Asset
	if err := db.First(&asset, id).Error; err != nil {
		return notFound(c, "asset not found")
	}

	if err := db.Delete(&Asset{}, id).Error; err != nil {
		return serverError(c, err.Error())
	}
	// Deleting the row without the file would leak storage quietly.
	if asset.StorageKey != "" {
		if err := store.Delete(asset.StorageKey); err != nil {
			log.Printf("asset %d row deleted but its file remains: %v", asset.ID, err)
		}
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// formInt reads an optional numeric form field.
func formInt(c *fiber.Ctx, field string) int {
	value, err := strconv.Atoi(c.FormValue(field))
	if err != nil {
		return 0
	}
	return value
}
