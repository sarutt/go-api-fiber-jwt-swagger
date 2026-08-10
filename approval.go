package main

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
)

// createApproval godoc
// @Summary Record a human decision at a gate
// @Description The only way past the two mandatory review gates. GATE_1_SCRIPT releases an approved script into production; GATE_2_RELEASE clears an assembled episode for publishing. A rejection sends the episode back for rework and is recorded either way.
// @Tags pipeline-approvals
// @Accept json
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Param approval body ApprovalRequest true "Gate decision"
// @Success 201 {object} ApprovalLog
// @Failure 400 {object} ErrorResponse
// @Failure 404 {object} ErrorResponse
// @Failure 409 {object} ErrorResponse
// @Router /pipeline/episodes/{id}/approvals [post]
func createApproval(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var episode Episode
	if err := db.First(&episode, id).Error; err != nil {
		return notFound(c, "episode not found")
	}

	req := new(ApprovalRequest)
	if err := c.BodyParser(req); err != nil {
		return badRequest(c, err.Error())
	}
	if req.Reviewer == "" {
		return badRequest(c, "reviewer is required: gate decisions must name the person who made them")
	}
	if req.Decision != DecisionApproved && req.Decision != DecisionRejected {
		return badRequest(c, fmt.Sprintf("decision must be %s or %s", DecisionApproved, DecisionRejected))
	}

	approvedTo, rejectedTo, ok := gateTarget(req.Gate)
	if !ok {
		return badRequest(c, fmt.Sprintf("gate must be %s or %s", Gate1Script, Gate2Release))
	}
	if expected := gateStatus(req.Gate); episode.Status != expected {
		return conflict(c, fmt.Sprintf("%s applies to episodes at %s, but this one is at %s", req.Gate, expected, episode.Status))
	}

	target := approvedTo
	if req.Decision == DecisionRejected {
		target = rejectedTo
	}

	// A rejection is still a state machine move, so it is validated too.
	// Only an approval is allowed to cross the gate edge.
	if err := validateTransition(episode.Status, target, req.Decision == DecisionApproved); err != nil {
		return conflict(c, err.Error())
	}

	from := episode.Status
	episode.Status = target
	if err := db.Save(&episode).Error; err != nil {
		return serverError(c, err.Error())
	}

	approval := ApprovalLog{
		EpisodeID:  episode.ID,
		Gate:       req.Gate,
		Decision:   req.Decision,
		Reviewer:   req.Reviewer,
		Notes:      req.Notes,
		FromStatus: from,
		ToStatus:   target,
	}
	if err := db.Create(&approval).Error; err != nil {
		return serverError(c, err.Error())
	}

	recordEvent(episode.ID, from, target, req.Reviewer, fmt.Sprintf("%s %s", req.Gate, req.Decision))
	return c.Status(fiber.StatusCreated).JSON(approval)
}

// getEpisodeApprovals godoc
// @Summary List the gate decisions for an episode
// @Tags pipeline-approvals
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Episode ID"
// @Success 200 {array} ApprovalLog
// @Router /pipeline/episodes/{id}/approvals [get]
func getEpisodeApprovals(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var approvals []ApprovalLog
	if err := db.Where("episode_id = ?", id).Order("id asc").Find(&approvals).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(approvals)
}

// getPendingReviews godoc
// @Summary List everything waiting on a human
// @Description Powers the reviewer dashboard: every episode currently parked at a gate. Filter to one gate with ?gate=GATE_1_SCRIPT
// @Tags pipeline-approvals
// @Produce json
// @Security ApiKeyAuth
// @Param gate query string false "Limit to one gate" Enums(GATE_1_SCRIPT,GATE_2_RELEASE)
// @Success 200 {array} Episode
// @Failure 400 {object} ErrorResponse
// @Router /pipeline/reviews/pending [get]
func getPendingReviews(c *fiber.Ctx) error {
	waiting := []EpisodeStatus{gateStatus(Gate1Script), gateStatus(Gate2Release)}

	if gate := c.Query("gate"); gate != "" {
		status := gateStatus(gate)
		if status == "" {
			return badRequest(c, fmt.Sprintf("gate must be %s or %s", Gate1Script, Gate2Release))
		}
		waiting = []EpisodeStatus{status}
	}

	var episodes []Episode
	if err := db.Where("status IN ?", waiting).Order("id asc").Find(&episodes).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(episodes)
}
