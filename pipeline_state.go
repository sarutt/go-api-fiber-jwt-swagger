package main

import "fmt"

// episodeTransitions is the whole state machine. An episode may only move to
// a status listed here for its current status; anything else is rejected.
// Backward edges exist where a gate rejection sends work back for rework.
var episodeTransitions = map[EpisodeStatus][]EpisodeStatus{
	StatusIdeaBacklog:       {StatusScriptDraft, StatusFailed, StatusCancelled},
	StatusScriptDraft:       {StatusScriptApproved, StatusIdeaBacklog, StatusFailed, StatusCancelled},
	StatusScriptApproved:    {StatusVOGenerated, StatusScriptDraft, StatusFailed, StatusCancelled},
	StatusVOGenerated:       {StatusMusicGenerated, StatusFailed, StatusCancelled},
	StatusMusicGenerated:    {StatusAnimationRendered, StatusFailed, StatusCancelled},
	StatusAnimationRendered: {StatusAssembled, StatusFailed, StatusCancelled},
	StatusAssembled:         {StatusQAReview, StatusFailed, StatusCancelled},
	StatusQAReview:          {StatusPlatformAdapted, StatusAssembled, StatusFailed, StatusCancelled},
	StatusPlatformAdapted:   {StatusScheduled, StatusFailed, StatusCancelled},
	StatusScheduled:         {StatusPublished, StatusPlatformAdapted, StatusFailed, StatusCancelled},
	StatusPublished:         {StatusAnalyzed},
	StatusAnalyzed:          {},
	// FAILED is terminal as far as the table goes. The only sanctioned way
	// out is the admin retry endpoint, which restores the stage the episode
	// failed at rather than allowing an arbitrary jump.
	StatusFailed:    {StatusCancelled},
	StatusCancelled: {},
}

// gateFor returns the human review gate guarding a transition, if any.
// Transitions guarded by a gate cannot be performed by an agent through the
// plain transition endpoint — they must go through the approvals endpoint.
func gateFor(from, to EpisodeStatus) string {
	switch {
	case from == StatusScriptDraft && to == StatusScriptApproved:
		return Gate1Script
	case from == StatusQAReview && to == StatusPlatformAdapted:
		return Gate2Release
	}
	return ""
}

// stageOwners maps a status to the agent responsible for moving an episode
// out of it. Agents poll /pipeline/queue for their own stage.
var stageOwners = map[EpisodeStatus]string{
	StatusIdeaBacklog:       "script-agent",
	StatusScriptDraft:       "educational-advisor (human)",
	StatusScriptApproved:    "voiceover-agent",
	StatusVOGenerated:       "music-agent",
	StatusMusicGenerated:    "animation-agent",
	StatusAnimationRendered: "editing-agent",
	StatusAssembled:         "qa-agent",
	StatusQAReview:          "release-reviewer (human)",
	StatusPlatformAdapted:   "platform-adaptation-agent",
	StatusScheduled:         "upload-agent",
	StatusPublished:         "analytics-agent",
	StatusAnalyzed:          "",
	StatusFailed:            "admin (human)",
	StatusCancelled:         "",
}

// stageLayers maps a status to the architecture layer that owns it.
var stageLayers = map[EpisodeStatus]string{
	StatusIdeaBacklog:       "2-creative",
	StatusScriptDraft:       "2-creative",
	StatusScriptApproved:    "3-asset-generation",
	StatusVOGenerated:       "3-asset-generation",
	StatusMusicGenerated:    "3-asset-generation",
	StatusAnimationRendered: "4-assembly-qa",
	StatusAssembled:         "4-assembly-qa",
	StatusQAReview:          "4-assembly-qa",
	StatusPlatformAdapted:   "5-distribution",
	StatusScheduled:         "5-distribution",
	StatusPublished:         "6-feedback",
	StatusAnalyzed:          "6-feedback",
	StatusFailed:            "-",
	StatusCancelled:         "-",
}

// stageOrder is the happy path, used for ordering the discovery endpoint.
var stageOrder = []EpisodeStatus{
	StatusIdeaBacklog,
	StatusScriptDraft,
	StatusScriptApproved,
	StatusVOGenerated,
	StatusMusicGenerated,
	StatusAnimationRendered,
	StatusAssembled,
	StatusQAReview,
	StatusPlatformAdapted,
	StatusScheduled,
	StatusPublished,
	StatusAnalyzed,
	StatusFailed,
	StatusCancelled,
}

// isValidStatus reports whether s is a known pipeline status.
func isValidStatus(s EpisodeStatus) bool {
	_, ok := episodeTransitions[s]
	return ok
}

// canTransition reports whether from -> to is an edge in the state machine.
func canTransition(from, to EpisodeStatus) bool {
	for _, allowed := range episodeTransitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// validateTransition checks a requested move and explains why it is refused.
// Set viaGate when the caller is a human reviewer coming through the
// approvals endpoint; gate-guarded edges are refused for anyone else.
func validateTransition(from, to EpisodeStatus, viaGate bool) error {
	if !isValidStatus(to) {
		return fmt.Errorf("unknown status %q", to)
	}
	if from == to {
		return fmt.Errorf("episode is already at %s", from)
	}
	if !canTransition(from, to) {
		return fmt.Errorf("cannot move from %s to %s", from, to)
	}
	if gate := gateFor(from, to); gate != "" && !viaGate {
		return fmt.Errorf("%s -> %s requires human approval at %s: post to /pipeline/episodes/{id}/approvals instead", from, to, gate)
	}
	return nil
}

// pipelineStages builds the discovery payload describing every stage.
func pipelineStages() []StageInfo {
	stages := make([]StageInfo, 0, len(stageOrder))
	for _, status := range stageOrder {
		next := episodeTransitions[status]
		if next == nil {
			next = []EpisodeStatus{}
		}
		info := StageInfo{
			Status:       status,
			Layer:        stageLayers[status],
			OwnerAgent:   stageOwners[status],
			NextStatuses: next,
		}
		// Surface the gate on the stage an episode waits in, so a dashboard
		// can list exactly what is sitting in front of a human.
		for _, to := range next {
			if gate := gateFor(status, to); gate != "" {
				info.RequiresGate = gate
			}
		}
		stages = append(stages, info)
	}
	return stages
}

// gateTarget returns the status an approved gate decision moves an episode
// to, and the status a rejection sends it back to.
func gateTarget(gate string) (approved, rejected EpisodeStatus, ok bool) {
	switch gate {
	case Gate1Script:
		return StatusScriptApproved, StatusIdeaBacklog, true
	case Gate2Release:
		return StatusPlatformAdapted, StatusAssembled, true
	}
	return "", "", false
}

// gateStatus returns the status an episode must be in to face a given gate.
func gateStatus(gate string) EpisodeStatus {
	switch gate {
	case Gate1Script:
		return StatusScriptDraft
	case Gate2Release:
		return StatusQAReview
	}
	return ""
}
