package main

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
)

// getEpisodes godoc
// @Summary List episodes
// @Description List every episode, newest first. Filter by pipeline stage with ?status=
// @Tags pipeline-episodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param status query string false "Filter by pipeline status" Enums(IDEA_BACKLOG,SCRIPT_DRAFT,SCRIPT_APPROVED,VO_GENERATED,MUSIC_GENERATED,ANIMATION_RENDERED,ASSEMBLED,QA_REVIEW,PLATFORM_ADAPTED,SCHEDULED,PUBLISHED,ANALYZED,CANCELLED)
// @Success 200 {array} Episode
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/episodes [get]
func getEpisodes(c *fiber.Ctx) error {
	query := db.Order("id desc")

	if status := c.Query("status"); status != "" {
		if !isValidStatus(EpisodeStatus(status)) {
			return badRequest(c, fmt.Sprintf("unknown status %q", status))
		}
		query = query.Where("status = ?", status)
	}

	var episodes []Episode
	if err := query.Find(&episodes).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(episodes)
}

// getEpisode godoc
// @Summary Get one episode
// @Tags pipeline-episodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Success 200 {object} Episode
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/episodes/{id} [get]
func getEpisode(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}
	return c.JSON(episode)
}

// createEpisode godoc
// @Summary Create an episode
// @Description Creates an episode in IDEA_BACKLOG. Status from the request body is ignored — every episode enters the pipeline at the first stage.
// @Tags pipeline-episodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param episode body Episode true "Episode"
// @Success 201 {object} Episode
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/episodes [post]
func createEpisode(c *fiber.Ctx) error {
	episode := new(Episode)
	if err := c.BodyParser(episode); err != nil {
		return badRequest(c, err.Error())
	}
	if episode.Title == "" {
		return badRequest(c, "title is required")
	}

	// Every episode enters at the head of the pipeline regardless of what the
	// caller sent, so nothing can be created part-way past a gate.
	episode.ID = 0
	episode.Status = StatusIdeaBacklog
	episode.MadeForKids = true
	episode.AIDisclosure = true
	if episode.Code == "" {
		var count int64
		db.Model(&Episode{}).Count(&count)
		episode.Code = fmt.Sprintf("PPH-%03d", count+1)
	}

	if err := db.Create(episode).Error; err != nil {
		return badRequest(c, err.Error())
	}

	recordEvent(episode.ID, "", StatusIdeaBacklog, valueOr(currentSubject(c), "api"), "episode created")
	return c.Status(fiber.StatusCreated).JSON(episode)
}

// updateEpisode godoc
// @Summary Update episode content
// @Description Updates the editorial fields of an episode. Status is not editable here — use the transition endpoint.
// @Tags pipeline-episodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param episode body Episode true "Episode fields to update"
// @Success 200 {object} Episode
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/episodes/{id} [put]
func updateEpisode(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}

	incoming := new(Episode)
	if err := c.BodyParser(incoming); err != nil {
		return badRequest(c, err.Error())
	}

	// Status changes go through the state machine, never through a plain
	// update, so an agent cannot skip a stage or a gate by writing the field.
	episode.Title = valueOr(incoming.Title, episode.Title)
	episode.Synopsis = valueOr(incoming.Synopsis, episode.Synopsis)
	episode.ScriptText = valueOr(incoming.ScriptText, episode.ScriptText)
	episode.SongTitle = valueOr(incoming.SongTitle, episode.SongTitle)
	episode.SongLyrics = valueOr(incoming.SongLyrics, episode.SongLyrics)
	episode.YouTubeVideoID = valueOr(incoming.YouTubeVideoID, episode.YouTubeVideoID)
	episode.TikTokVideoID = valueOr(incoming.TikTokVideoID, episode.TikTokVideoID)
	if incoming.CurriculumTopicID != nil {
		episode.CurriculumTopicID = incoming.CurriculumTopicID
	}
	if incoming.DurationSeconds > 0 {
		episode.DurationSeconds = incoming.DurationSeconds
	}
	if incoming.ScheduledAt != nil {
		episode.ScheduledAt = incoming.ScheduledAt
	}

	if err := db.Save(&episode).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(episode)
}

// deleteEpisode godoc
// @Summary Delete an episode
// @Tags pipeline-episodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Success 204 "Deleted"
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/episodes/{id} [delete]
func deleteEpisode(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	result := db.Delete(&Episode{}, id)
	if result.Error != nil {
		return serverError(c, result.Error.Error())
	}
	if result.RowsAffected == 0 {
		return notFound(c, "episode not found")
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// transitionEpisode godoc
// @Summary Move an episode to the next stage
// @Description Used by production agents. Refuses any move the state machine does not allow, and refuses the two gate transitions, which require a human approval instead.
// @Tags pipeline-episodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param transition body TransitionRequest true "Target stage"
// @Success 200 {object} Episode
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/transition [post]
func transitionEpisode(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}

	req := new(TransitionRequest)
	if err := c.BodyParser(req); err != nil {
		return badRequest(c, err.Error())
	}
	if req.ToStatus == "" {
		return badRequest(c, "to_status is required")
	}

	// A live claim means one worker owns this episode; nobody else may move
	// it. Without this the claim would be advisory and a second worker could
	// still finish the job it was not given.
	if holder := claimHolder(&episode); holder != "" && holder != req.WorkerID {
		return conflict(c, fmt.Sprintf("episode is claimed by %s until the lease expires", holder))
	}

	if err := validateTransition(episode.Status, req.ToStatus, false); err != nil {
		return conflict(c, err.Error())
	}

	from := episode.Status
	episode.Status = req.ToStatus
	if req.ToStatus == StatusPublished && episode.PublishedAt == nil {
		now := time.Now()
		episode.PublishedAt = &now
	}
	// The claim belonged to the stage just finished, so the next stage starts
	// unclaimed and its own worker can take it. Attempts count the current
	// stage, so they reset with it.
	clearClaim(&episode)
	episode.Attempts = 0
	episode.LastError = ""

	if err := db.Save(&episode).Error; err != nil {
		return serverError(c, err.Error())
	}

	// Prefer the actor the agent names, fall back to its authenticated
	// identity, and only then to the stage's expected owner.
	actor := valueOr(req.Actor, valueOr(currentSubject(c), stageOwners[from]))
	recordEvent(episode.ID, from, req.ToStatus, actor, req.Note)
	alertOnTransition(&episode, from)
	return c.JSON(episode)
}

// getEpisodeEvents godoc
// @Summary Get the stage history of an episode
// @Tags pipeline-episodes
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Success 200 {array} EpisodeEvent
// @Router /pipeline/episodes/{id}/events [get]
func getEpisodeEvents(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var events []EpisodeEvent
	if err := db.Where("episode_id = ?", id).Order("id asc").Find(&events).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(events)
}

// getPipelineQueue godoc
// @Summary Look at the work queue for a stage
// @Description Read-only view of everything waiting at a stage, including which worker holds each episode. To actually take work use POST /pipeline/queue/claim, which hands one episode to one worker.
// @Tags pipeline
// @Produce json
// @Security ApiKeyAuth
// @Param status query string true "Pipeline stage to poll"
// @Success 200 {array} Episode
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/queue [get]
func getPipelineQueue(c *fiber.Ctx) error {
	status := c.Query("status")
	if status == "" {
		return badRequest(c, "status is required")
	}
	if !isValidStatus(EpisodeStatus(status)) {
		return badRequest(c, fmt.Sprintf("unknown status %q", status))
	}

	var episodes []Episode
	if err := db.Where("status = ?", status).Order("id asc").Find(&episodes).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(episodes)
}

// getPipelineStages godoc
// @Summary Describe the pipeline
// @Description Returns every stage with its owning agent, allowed next stages and any human gate. Lets an agent discover where it fits without hardcoding the flow.
// @Tags pipeline
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {array} StageInfo
// @Router /pipeline/stages [get]
func getPipelineStages(c *fiber.Ctx) error {
	return c.JSON(pipelineStages())
}

// recordEvent appends to the episode audit trail. A failure to write history
// must not fail the transition that already succeeded, so it only logs.
func recordEvent(episodeID uint, from, to EpisodeStatus, actor, note string) {
	event := EpisodeEvent{
		EpisodeID:  episodeID,
		FromStatus: from,
		ToStatus:   to,
		Actor:      valueOr(actor, "unknown"),
		Note:       note,
	}
	db.Create(&event)
}

// valueOr returns v when it is set, otherwise the fallback.
func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
