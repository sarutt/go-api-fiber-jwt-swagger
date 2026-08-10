package main

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
)

// claimWork godoc
// @Summary Take the next job at a stage
// @Description How a worker picks up work. Hands the oldest available episode at the stage to exactly one caller and holds it under an expiring lease, so two workers polling the same stage never process the same episode. Returns 204 when the stage has nothing free.
// @Tags pipeline
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param claim body ClaimRequest true "Stage to take work from"
// @Success 200 {object} Episode
// @Success 204 "Nothing available at this stage"
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/queue/claim [post]
func claimWork(c *fiber.Ctx) error {
	req := new(ClaimRequest)
	if err := c.BodyParser(req); err != nil {
		return badRequest(c, err.Error())
	}
	if req.Status == "" {
		return badRequest(c, "status is required")
	}
	if !isValidStatus(req.Status) {
		return badRequest(c, fmt.Sprintf("unknown status %q", req.Status))
	}

	// Fall back to the token identity so a single worker still gets a stable
	// name, but a fleet should send its own worker_id per process.
	workerID := valueOr(req.WorkerID, currentSubject(c))
	if workerID == "" {
		return badRequest(c, "worker_id is required")
	}

	episode, err := claimNextEpisode(req.Status, workerID)
	if err != nil {
		return serverError(c, err.Error())
	}
	if episode == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}
	return c.JSON(episode)
}

// releaseClaim godoc
// @Summary Give a claimed episode back
// @Description Called by a worker that cannot finish its job, so the episode returns to the queue immediately instead of waiting out the lease.
// @Tags pipeline
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param worker body WorkerRequest true "Worker releasing the claim"
// @Success 200 {object} Episode
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/release [post]
func releaseClaim(c *fiber.Ctx) error {
	episode, req, err := loadClaimedEpisode(c)
	if err != nil {
		return err
	}

	if holder := claimHolder(episode); holder != "" && holder != req.WorkerID {
		return conflict(c, fmt.Sprintf("episode is claimed by %s, not %s", holder, req.WorkerID))
	}

	clearClaim(episode)
	if err := db.Save(episode).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(episode)
}

// heartbeatClaim godoc
// @Summary Extend a claim on a long job
// @Description Pushes the lease out so work that takes longer than the lease — an animation render, say — is not handed to a second worker while the first is still on it.
// @Tags pipeline
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param worker body WorkerRequest true "Worker holding the claim"
// @Success 200 {object} Episode
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/heartbeat [post]
func heartbeatClaim(c *fiber.Ctx) error {
	episode, req, err := loadClaimedEpisode(c)
	if err != nil {
		return err
	}

	// Only a live claim can be extended. Once a lease has lapsed the episode
	// may already belong to someone else, so the worker must claim it again
	// rather than quietly resume.
	holder := claimHolder(episode)
	if holder == "" {
		return conflict(c, "claim has already expired: take the episode again through /pipeline/queue/claim")
	}
	if holder != req.WorkerID {
		return conflict(c, fmt.Sprintf("episode is claimed by %s, not %s", holder, req.WorkerID))
	}

	now := time.Now()
	episode.ClaimedAt = &now
	if err := db.Save(episode).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(episode)
}

// loadClaimedEpisode reads the episode and the worker identity shared by the
// release and heartbeat handlers.
func loadClaimedEpisode(c *fiber.Ctx) (*Episode, *WorkerRequest, error) {
	id, err := paramID(c)
	if err != nil {
		return nil, nil, badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return nil, nil, notFound(c, "episode not found")
	}

	req := new(WorkerRequest)
	if err := c.BodyParser(req); err != nil {
		return nil, nil, badRequest(c, err.Error())
	}
	req.WorkerID = valueOr(req.WorkerID, currentSubject(c))
	if req.WorkerID == "" {
		return nil, nil, badRequest(c, "worker_id is required")
	}
	return &episode, req, nil
}
