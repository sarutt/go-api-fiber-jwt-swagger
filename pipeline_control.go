package main

import (
	"fmt"
	"time"

	"github.com/gofiber/fiber/v2"
)

// stuckAfter is how long an episode may sit in one stage before the overview
// flags it. A single operator cannot watch a queue all day, so the system has
// to volunteer what has gone quiet.
const stuckAfter = 24 * time.Hour

// pauseState returns the control row for a scope, creating nothing.
func pauseState(scope string) (PipelineControl, bool) {
	var control PipelineControl
	if err := db.Where("scope = ?", scope).First(&control).Error; err != nil {
		return PipelineControl{}, false
	}
	return control, control.Paused
}

// productionPaused reports whether work may be taken at a stage right now,
// checking the global switch first and then that stage's own.
func productionPaused(status EpisodeStatus) (bool, string) {
	if control, paused := pauseState(ControlScopeAll); paused {
		return true, control.Reason
	}
	if control, paused := pauseState(string(status)); paused {
		return true, control.Reason
	}
	return false, ""
}

// setPause writes a control row for a scope.
func setPause(scope string, paused bool, reason, actor string) (PipelineControl, error) {
	var control PipelineControl
	if err := db.Where("scope = ?", scope).First(&control).Error; err != nil {
		control = PipelineControl{Scope: scope}
	}

	control.Paused = paused
	control.Reason = reason
	control.PausedBy = actor
	if paused {
		now := time.Now()
		control.PausedAt = &now
	} else {
		control.PausedAt = nil
	}

	if err := db.Save(&control).Error; err != nil {
		return PipelineControl{}, err
	}
	return control, nil
}

// pausePipeline godoc
// @Summary Stop production
// @Description The operator's stop switch. Workers can no longer take work, so the pipeline drains rather than halting mid-job: whatever is already claimed finishes, nothing new starts. Pass a stage as the scope to pause one step — pausing SCHEDULED holds uploads while the rest keeps producing.
// @Tags pipeline-control
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param pause body PauseRequest true "What to pause and why"
// @Success 200 {object} PipelineControl
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /pipeline/control/pause [post]
func pausePipeline(c *fiber.Ctx) error {
	req := new(PauseRequest)
	if err := c.BodyParser(req); err != nil {
		return badRequest(c, err.Error())
	}

	scope := valueOr(req.Scope, ControlScopeAll)
	if scope != ControlScopeAll && !isValidStatus(EpisodeStatus(scope)) {
		return badRequest(c, fmt.Sprintf("scope must be %s or a pipeline stage, got %q", ControlScopeAll, scope))
	}

	control, err := setPause(scope, true, req.Reason, currentSubject(c))
	if err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(control)
}

// resumePipeline godoc
// @Summary Start production again
// @Description Lifts a pause. Workers begin taking work at their next poll; nothing needs restarting.
// @Tags pipeline-control
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param pause body PauseRequest true "What to resume"
// @Success 200 {object} PipelineControl
// @Failure 400 {object} ErrorResponse
// @Failure 403 {object} ErrorResponse
// @Router /pipeline/control/resume [post]
func resumePipeline(c *fiber.Ctx) error {
	req := new(PauseRequest)
	if err := c.BodyParser(req); err != nil {
		return badRequest(c, err.Error())
	}

	scope := valueOr(req.Scope, ControlScopeAll)
	if scope != ControlScopeAll && !isValidStatus(EpisodeStatus(scope)) {
		return badRequest(c, fmt.Sprintf("scope must be %s or a pipeline stage, got %q", ControlScopeAll, scope))
	}

	control, err := setPause(scope, false, "", currentSubject(c))
	if err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(control)
}

// getControl godoc
// @Summary Show what is paused
// @Tags pipeline-control
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {array} PipelineControl
// @Router /pipeline/control [get]
func getControl(c *fiber.Ctx) error {
	var controls []PipelineControl
	if err := db.Order("scope asc").Find(&controls).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(controls)
}

// getOverview godoc
// @Summary The whole operation on one screen
// @Description Everything one operator needs to see at a glance: whether production is running, how much sits at each stage, what is waiting on a human, what has failed, and what has gone quiet.
// @Tags pipeline-control
// @Produce json
// @Security ApiKeyAuth
// @Success 200 {object} PipelineOverview
// @Router /pipeline/overview [get]
func getOverview(c *fiber.Ctx) error {
	overview := PipelineOverview{PausedStages: []string{}, Stages: []StageCount{}, Stuck: []Episode{}}

	var controls []PipelineControl
	if err := db.Where("paused = ?", true).Find(&controls).Error; err != nil {
		return serverError(c, err.Error())
	}
	pausedScopes := map[string]bool{}
	for _, control := range controls {
		pausedScopes[control.Scope] = true
		if control.Scope == ControlScopeAll {
			overview.Paused = true
			overview.PauseReason = control.Reason
			continue
		}
		overview.PausedStages = append(overview.PausedStages, control.Scope)
	}

	for _, status := range stageOrder {
		var count int64
		if err := db.Model(&Episode{}).Where("status = ?", status).Count(&count).Error; err != nil {
			return serverError(c, err.Error())
		}
		overview.Stages = append(overview.Stages, StageCount{
			Status: status,
			Count:  count,
			Paused: overview.Paused || pausedScopes[string(status)],
		})

		// In flight is everything still moving: not finished, not parked.
		switch status {
		case StatusAnalyzed, StatusCancelled, StatusFailed:
		default:
			overview.InFlight += count
		}
	}

	db.Model(&Episode{}).Where("status = ?", StatusFailed).Count(&overview.Failed)
	db.Model(&Episode{}).Where("status = ?", StatusPublished).Or("status = ?", StatusAnalyzed).Count(&overview.PublishedTotal)
	db.Model(&Episode{}).Where("status IN ?", []EpisodeStatus{
		gateStatus(Gate1Script), gateStatus(Gate2Release),
	}).Count(&overview.AwaitingReview)

	cutoff := time.Now().Add(-claimLease())
	db.Model(&Episode{}).Where("claimed_by <> ? AND claimed_at > ?", "", cutoff).Count(&overview.ClaimedNow)
	overview.OpenAlerts = countOpenAlerts(db)

	// Anything sitting untouched in an active stage for too long. A human
	// gate counts: an episode nobody reviewed for a day is also stuck.
	if err := db.Where("updated_at < ?", time.Now().Add(-stuckAfter)).
		Where("status NOT IN ?", []EpisodeStatus{StatusAnalyzed, StatusCancelled, StatusFailed}).
		Order("updated_at asc").
		Limit(20).
		Find(&overview.Stuck).Error; err != nil {
		return serverError(c, err.Error())
	}

	return c.JSON(overview)
}

// failEpisode godoc
// @Summary Report that a job could not be finished
// @Description Called by a worker that has given up. The stage is retried until the attempt limit, after which the episode is parked in FAILED for a person to look at rather than being retried forever.
// @Tags pipeline-episodes
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param failure body FailRequest true "What went wrong"
// @Success 200 {object} Episode
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/fail [post]
func failEpisode(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}

	req := new(FailRequest)
	if err := c.BodyParser(req); err != nil {
		return badRequest(c, err.Error())
	}
	workerID := valueOr(req.WorkerID, currentSubject(c))

	if holder := claimHolder(&episode); holder != "" && holder != workerID {
		return conflict(c, fmt.Sprintf("episode is claimed by %s, not %s", holder, workerID))
	}
	if episode.Status == StatusFailed {
		return conflict(c, "episode is already parked in FAILED")
	}

	episode.Attempts++
	episode.LastError = req.Error
	clearClaim(&episode)

	from := episode.Status
	note := fmt.Sprintf("attempt %d of %d failed: %s", episode.Attempts, maxAttempts, req.Error)

	if episode.Attempts >= maxAttempts {
		// Remember where it broke so a retry can put it back there.
		episode.FailedFrom = from
		episode.Status = StatusFailed
		note = fmt.Sprintf("parked after %d attempts: %s", episode.Attempts, req.Error)
	}

	if err := db.Save(&episode).Error; err != nil {
		return serverError(c, err.Error())
	}

	recordEvent(episode.ID, from, episode.Status, workerID, note)
	alertOnTransition(&episode, from)
	return c.JSON(episode)
}

// retryEpisode godoc
// @Summary Put a failed episode back to work
// @Description The one sanctioned way out of FAILED. Restores the stage the episode failed at and clears the attempt count, so a fix to the underlying problem can be tried without recreating the episode.
// @Tags pipeline-control
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Success 200 {object} Episode
// @Failure 403 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/retry [post]
func retryEpisode(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}
	if episode.Status != StatusFailed {
		return conflict(c, fmt.Sprintf("only a FAILED episode can be retried, this one is at %s", episode.Status))
	}
	if !isValidStatus(episode.FailedFrom) {
		return conflict(c, "episode has no recorded stage to return to")
	}

	from := episode.Status
	episode.Status = episode.FailedFrom
	episode.FailedFrom = ""
	episode.Attempts = 0
	episode.LastError = ""
	clearClaim(&episode)

	if err := db.Save(&episode).Error; err != nil {
		return serverError(c, err.Error())
	}

	recordEvent(episode.ID, from, episode.Status, currentSubject(c), "retried by operator")
	alertOnTransition(&episode, from)
	return c.JSON(episode)
}
