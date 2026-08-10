package main

import (
	"net/http"
	"testing"
	"time"
)

// The stop switch has to actually stop workers, not just record an intent.
func TestPauseStopsWorkersTakingWork(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	newEpisode(t, app, agentToken)

	res := call(t, app, http.MethodPost, "/pipeline/control/pause", adminToken, PauseRequest{
		Scope:  ControlScopeAll,
		Reason: "checking a compliance question",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("pause returned %d, want 200", res.StatusCode)
	}

	res = call(t, app, http.MethodPost, "/pipeline/queue/claim", agentToken, ClaimRequest{
		Status:   StatusIdeaBacklog,
		WorkerID: "worker-a",
	})
	if res.StatusCode != http.StatusLocked {
		t.Fatalf("claim while paused returned %d, want 423", res.StatusCode)
	}

	var body ErrorResponse
	decode(t, res, &body)
	if body.Message != "checking a compliance question" {
		t.Errorf("pause reason not returned to the worker, got %q", body.Message)
	}

	// And resuming lets work flow again with nothing to restart.
	res = call(t, app, http.MethodPost, "/pipeline/control/resume", adminToken, PauseRequest{Scope: ControlScopeAll})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("resume returned %d, want 200", res.StatusCode)
	}
	if claimed := claimAt(t, app, agentToken, StatusIdeaBacklog, "worker-a"); claimed == nil {
		t.Error("no work available after resuming")
	}
}

// Pausing one stage is how uploads get held while production carries on.
func TestPausingOneStageLeavesTheRestRunning(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	newEpisode(t, app, agentToken)

	res := call(t, app, http.MethodPost, "/pipeline/control/pause", adminToken, PauseRequest{
		Scope:  string(StatusScheduled),
		Reason: "holding uploads over the weekend",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("stage pause returned %d, want 200", res.StatusCode)
	}

	// The paused stage refuses work.
	res = call(t, app, http.MethodPost, "/pipeline/queue/claim", agentToken, ClaimRequest{
		Status:   StatusScheduled,
		WorkerID: "upload-worker",
	})
	if res.StatusCode != http.StatusLocked {
		t.Errorf("claim on the paused stage returned %d, want 423", res.StatusCode)
	}

	// Every other stage keeps going.
	if claimed := claimAt(t, app, agentToken, StatusIdeaBacklog, "script-worker"); claimed == nil {
		t.Error("pausing one stage stopped an unrelated stage")
	}
}

func TestOnlyAdminsCanPauseProduction(t *testing.T) {
	app := newTestApp(t)

	for _, role := range []string{RoleAgent, RoleReviewer} {
		token := tokenFor(t, "someone", role)
		res := call(t, app, http.MethodPost, "/pipeline/control/pause", token, PauseRequest{Scope: ControlScopeAll})
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("%s pausing production returned %d, want 403", role, res.StatusCode)
		}
	}
}

func TestPauseRejectsAnUnknownScope(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)

	res := call(t, app, http.MethodPost, "/pipeline/control/pause", adminToken, PauseRequest{Scope: "NOT_A_STAGE"})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("pausing an unknown scope returned %d, want 400", res.StatusCode)
	}
}

// One operator handling everything means the admin account must also be able
// to clear the review gates, without needing a second login.
func TestAdminCanAlsoReview(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), adminToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionApproved,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("admin approval returned %d, want 201", res.StatusCode)
	}
}

// An agent must still not reach a gate, whatever the role hierarchy allows
// above it.
func TestRoleHierarchyDoesNotLetAgentsApprove(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), agentToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionApproved,
	})
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("agent approval returned %d, want 403", res.StatusCode)
	}
}

// A job that always fails must stop being retried.
func TestRepeatedFailuresParkTheEpisode(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, agentToken)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		claimed := claimAt(t, app, agentToken, StatusIdeaBacklog, "flaky-worker")
		if attempt < maxAttempts && claimed == nil {
			t.Fatalf("attempt %d found no work to retry", attempt)
		}

		res := call(t, app, http.MethodPost, episodePath(episode.ID, "fail"), agentToken, FailRequest{
			WorkerID: "flaky-worker",
			Error:    "model call timed out",
		})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("attempt %d fail returned %d, want 200", attempt, res.StatusCode)
		}

		var after Episode
		decode(t, res, &after)
		if after.Attempts != attempt {
			t.Errorf("after attempt %d the counter read %d", attempt, after.Attempts)
		}

		if attempt < maxAttempts {
			if after.Status != StatusIdeaBacklog {
				t.Errorf("episode left its stage after attempt %d: %s", attempt, after.Status)
			}
		} else if after.Status != StatusFailed {
			t.Errorf("episode was not parked after %d attempts, sits at %s", maxAttempts, after.Status)
		}
	}

	// Once parked it must not be handed out again.
	if claimed := claimAt(t, app, agentToken, StatusIdeaBacklog, "another-worker"); claimed != nil {
		t.Error("a parked episode was still handed to a worker")
	}
}

func TestFailedEpisodeRecordsWhatBroke(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	var parked Episode
	for attempt := 0; attempt < maxAttempts; attempt++ {
		res := call(t, app, http.MethodPost, episodePath(episode.ID, "fail"), agentToken, FailRequest{
			WorkerID: "script-worker",
			Error:    "prompt exceeded the context window",
		})
		decode(t, res, &parked)
	}

	if parked.Status != StatusFailed {
		t.Fatalf("episode sits at %s, want FAILED", parked.Status)
	}
	if parked.FailedFrom != StatusScriptDraft {
		t.Errorf("recorded failure stage %s, want %s", parked.FailedFrom, StatusScriptDraft)
	}
	if parked.LastError != "prompt exceeded the context window" {
		t.Errorf("last error reads %q", parked.LastError)
	}
}

func TestRetryReturnsAnEpisodeToTheStageItFailedAt(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		call(t, app, http.MethodPost, episodePath(episode.ID, "fail"), agentToken, FailRequest{
			WorkerID: "script-worker",
			Error:    "boom",
		})
	}

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "retry"), adminToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("retry returned %d, want 200", res.StatusCode)
	}

	var revived Episode
	decode(t, res, &revived)
	if revived.Status != StatusScriptDraft {
		t.Errorf("retried episode returned to %s, want %s", revived.Status, StatusScriptDraft)
	}
	if revived.Attempts != 0 {
		t.Errorf("retry left the attempt counter at %d", revived.Attempts)
	}
	if revived.LastError != "" {
		t.Errorf("retry left the previous error in place: %q", revived.LastError)
	}
}

func TestRetryIsAdminOnlyAndRefusesHealthyEpisodes(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)
	reviewerToken := tokenFor(t, "user@example.com", RoleReviewer)

	episode := newEpisode(t, app, adminToken)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "retry"), reviewerToken, nil)
	if res.StatusCode != http.StatusForbidden {
		t.Errorf("reviewer retry returned %d, want 403", res.StatusCode)
	}

	res = call(t, app, http.MethodPost, episodePath(episode.ID, "retry"), adminToken, nil)
	if res.StatusCode != http.StatusConflict {
		t.Errorf("retrying a healthy episode returned %d, want 409", res.StatusCode)
	}
}

// The overview is the operator's whole screen, so it has to add up.
func TestOverviewSummarisesTheOperation(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	waiting := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, waiting.ID, StatusScriptDraft)

	newEpisode(t, app, agentToken)
	claimAt(t, app, agentToken, StatusIdeaBacklog, "worker-a")

	broken := newEpisode(t, app, agentToken)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		call(t, app, http.MethodPost, episodePath(broken.ID, "fail"), agentToken, FailRequest{
			WorkerID: "worker-b", Error: "boom",
		})
	}

	call(t, app, http.MethodPost, "/pipeline/control/pause", adminToken, PauseRequest{
		Scope: string(StatusScheduled), Reason: "holding uploads",
	})

	var overview PipelineOverview
	decode(t, call(t, app, http.MethodGet, "/pipeline/overview", adminToken, nil), &overview)

	if overview.AwaitingReview != 1 {
		t.Errorf("awaiting review reads %d, want 1", overview.AwaitingReview)
	}
	if overview.Failed != 1 {
		t.Errorf("failed reads %d, want 1", overview.Failed)
	}
	if overview.ClaimedNow != 1 {
		t.Errorf("claimed now reads %d, want 1", overview.ClaimedNow)
	}
	// The parked episode is not in flight; the other two are.
	if overview.InFlight != 2 {
		t.Errorf("in flight reads %d, want 2", overview.InFlight)
	}
	if overview.Paused {
		t.Error("overview reports a global pause when only one stage is paused")
	}
	if len(overview.PausedStages) != 1 || overview.PausedStages[0] != string(StatusScheduled) {
		t.Errorf("paused stages read %v", overview.PausedStages)
	}
	if len(overview.Stages) != len(stageOrder) {
		t.Errorf("overview described %d stages, want %d", len(overview.Stages), len(stageOrder))
	}
}

func TestOverviewReportsAGlobalPause(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)

	call(t, app, http.MethodPost, "/pipeline/control/pause", adminToken, PauseRequest{
		Scope: ControlScopeAll, Reason: "platform policy review",
	})

	var overview PipelineOverview
	decode(t, call(t, app, http.MethodGet, "/pipeline/overview", adminToken, nil), &overview)

	if !overview.Paused {
		t.Fatal("overview does not report the pause")
	}
	if overview.PauseReason != "platform policy review" {
		t.Errorf("pause reason reads %q", overview.PauseReason)
	}
	// A global pause marks every stage as held, not just the ALL row.
	for _, stage := range overview.Stages {
		if !stage.Paused {
			t.Errorf("stage %s not marked paused under a global pause", stage.Status)
			break
		}
	}
}

// An episode nobody has touched for a day is surfaced, because a lone
// operator will not notice it otherwise.
func TestOverviewFlagsStuckEpisodes(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)

	fresh := newEpisode(t, app, adminToken)
	stale := newEpisode(t, app, adminToken)

	untouchedSince := time.Now().Add(-stuckAfter - time.Hour)
	if err := db.Model(&Episode{}).Where("id = ?", stale.ID).
		Update("updated_at", untouchedSince).Error; err != nil {
		t.Fatalf("cannot age the episode: %v", err)
	}

	var overview PipelineOverview
	decode(t, call(t, app, http.MethodGet, "/pipeline/overview", adminToken, nil), &overview)

	if len(overview.Stuck) != 1 {
		t.Fatalf("overview flagged %d stuck episodes, want 1", len(overview.Stuck))
	}
	if overview.Stuck[0].ID != stale.ID {
		t.Errorf("flagged episode %d, want %d", overview.Stuck[0].ID, stale.ID)
	}
	if overview.Stuck[0].ID == fresh.ID {
		t.Error("a freshly touched episode was flagged as stuck")
	}
}

func TestMovingStageResetsTheAttemptCount(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	call(t, app, http.MethodPost, episodePath(episode.ID, "fail"), agentToken, FailRequest{
		WorkerID: "worker-a", Error: "transient",
	})

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), agentToken, TransitionRequest{
		ToStatus: StatusScriptDraft,
	})
	var moved Episode
	decode(t, res, &moved)

	if moved.Attempts != 0 {
		t.Errorf("attempts carried across a stage change: %d", moved.Attempts)
	}
	if moved.LastError != "" {
		t.Errorf("the previous stage's error carried over: %q", moved.LastError)
	}
}
