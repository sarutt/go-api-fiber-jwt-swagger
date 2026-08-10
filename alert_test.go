package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// openAlerts reads the alerts still waiting on a person.
func openAlerts(t *testing.T, app *fiber.App, token string) []Alert {
	t.Helper()

	alerts := []Alert{}
	decode(t, call(t, app, http.MethodGet, "/pipeline/alerts", token, nil), &alerts)
	return alerts
}

func alertsOfKind(alerts []Alert, kind string) []Alert {
	matched := []Alert{}
	for _, alert := range alerts {
		if alert.Kind == kind {
			matched = append(matched, alert)
		}
	}
	return matched
}

// An episode arriving at a gate is the one thing that cannot proceed without
// a person, so it has to announce itself.
func TestReachingAGateRaisesAnAlert(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	waiting := alertsOfKind(openAlerts(t, app, agentToken), AlertGateWaiting)
	if len(waiting) != 1 {
		t.Fatalf("reaching a gate raised %d alerts, want 1", len(waiting))
	}
	if waiting[0].EpisodeID != episode.ID {
		t.Errorf("alert names episode %d, want %d", waiting[0].EpisodeID, episode.ID)
	}
	if waiting[0].Stage != StatusScriptDraft {
		t.Errorf("alert names stage %s, want %s", waiting[0].Stage, StatusScriptDraft)
	}
}

// The alert has to close by itself. An operator reading a board of alerts
// that are no longer true is worse off than one reading no board at all.
func TestApprovingAGateResolvesItsAlert(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)
	reviewerToken := tokenFor(t, "advisor@studio.com", RoleReviewer)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), reviewerToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionApproved,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("approval returned %d", res.StatusCode)
	}

	if waiting := alertsOfKind(openAlerts(t, app, agentToken), AlertGateWaiting); len(waiting) != 0 {
		t.Errorf("%d gate alerts survived the approval", len(waiting))
	}
}

// A condition that persists must be announced once, not on every sweep.
func TestAnAlertIsNotRaisedTwiceForTheSameCondition(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)

	episode := newEpisode(t, app, adminToken)
	advance(t, app, adminToken, episode.ID, StatusScriptDraft)

	// Raising the identical alert again is what a repeat sweep does.
	raiseAlert(AlertGateWaiting, episode.ID, StatusScriptDraft, "again")
	raiseAlert(AlertGateWaiting, episode.ID, StatusScriptDraft, "and again")

	if waiting := alertsOfKind(openAlerts(t, app, adminToken), AlertGateWaiting); len(waiting) != 1 {
		t.Errorf("a repeated condition raised %d alerts, want 1", len(waiting))
	}
}

// Once resolved, a condition that genuinely recurs must alert again — the
// dedupe key must not silence the second occurrence forever.
func TestAResolvedConditionCanAlertAgain(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)
	reviewerToken := tokenFor(t, "advisor@studio.com", RoleReviewer)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	// Reject it back to the backlog, then walk it to the gate a second time.
	call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), reviewerToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionRejected,
	})
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	if waiting := alertsOfKind(openAlerts(t, app, agentToken), AlertGateWaiting); len(waiting) != 1 {
		t.Errorf("a recurring condition left %d open alerts, want 1", len(waiting))
	}
}

func TestParkingAnEpisodeRaisesAFailureAlert(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "voice-agent", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		call(t, app, http.MethodPost, episodePath(episode.ID, "fail"), agentToken, FailRequest{
			WorkerID: "voice-agent", Error: "TTS provider returned 503",
		})
	}

	failed := alertsOfKind(openAlerts(t, app, agentToken), AlertFailed)
	if len(failed) != 1 {
		t.Fatalf("parking an episode raised %d failure alerts, want 1", len(failed))
	}
	// The alert has to carry enough to act on without opening anything else.
	if !strings.Contains(failed[0].Message, "503") {
		t.Errorf("the alert does not say what broke: %q", failed[0].Message)
	}
}

// Retrying is the operator saying they have dealt with it.
func TestRetryingAFailedEpisodeResolvesItsAlert(t *testing.T) {
	app := newTestApp(t)
	adminToken := tokenFor(t, "admin@example.com", RoleAdmin)

	episode := newEpisode(t, app, adminToken)
	advance(t, app, adminToken, episode.ID, StatusScriptDraft)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		call(t, app, http.MethodPost, episodePath(episode.ID, "fail"), adminToken, FailRequest{Error: "boom"})
	}
	if failed := alertsOfKind(openAlerts(t, app, adminToken), AlertFailed); len(failed) != 1 {
		t.Fatalf("expected a failure alert before the retry, got %d", len(failed))
	}

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "retry"), adminToken, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("retry returned %d", res.StatusCode)
	}

	if failed := alertsOfKind(openAlerts(t, app, adminToken), AlertFailed); len(failed) != 0 {
		t.Errorf("%d failure alerts survived the retry", len(failed))
	}
}

// Nothing happening is exactly why no event can announce a stuck episode.
func TestTheSweeperFindsEpisodesThatHaveGoneQuiet(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "admin@example.com", RoleAdmin)

	fresh := newEpisode(t, app, token)
	stale := newEpisode(t, app, token)

	untouchedSince := time.Now().Add(-stuckAfter - time.Hour)
	if err := db.Model(&Episode{}).Where("id = ?", stale.ID).
		Update("updated_at", untouchedSince).Error; err != nil {
		t.Fatalf("cannot age the episode: %v", err)
	}

	sweepStuckEpisodes()

	stuck := alertsOfKind(openAlerts(t, app, token), AlertStuck)
	if len(stuck) != 1 {
		t.Fatalf("the sweep raised %d stuck alerts, want 1", len(stuck))
	}
	if stuck[0].EpisodeID != fresh.ID && stuck[0].EpisodeID != stale.ID {
		t.Fatalf("the alert names an unknown episode %d", stuck[0].EpisodeID)
	}
	if stuck[0].EpisodeID == fresh.ID {
		t.Error("a freshly touched episode was flagged as stuck")
	}

	// Sweeping again must not pile up duplicates.
	sweepStuckEpisodes()
	if stuck := alertsOfKind(openAlerts(t, app, token), AlertStuck); len(stuck) != 1 {
		t.Errorf("a second sweep produced %d stuck alerts, want 1", len(stuck))
	}
}

// An episode parked at a gate already has its own alert; the sweeper saying
// the same thing again is noise, not information.
func TestTheSweeperDoesNotDuplicateAGateAlert(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "admin@example.com", RoleAdmin)

	episode := newEpisode(t, app, token)
	advance(t, app, token, episode.ID, StatusScriptDraft)
	db.Model(&Episode{}).Where("id = ?", episode.ID).
		Update("updated_at", time.Now().Add(-stuckAfter-time.Hour))

	sweepStuckEpisodes()

	if stuck := alertsOfKind(openAlerts(t, app, token), AlertStuck); len(stuck) != 0 {
		t.Errorf("the sweeper raised %d stuck alerts for an episode already at a gate", len(stuck))
	}
}

func TestAcknowledgingAnAlertTakesItOffTheBoard(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "admin@example.com", RoleAdmin)

	episode := newEpisode(t, app, token)
	advance(t, app, token, episode.ID, StatusScriptDraft)

	alerts := openAlerts(t, app, token)
	if len(alerts) == 0 {
		t.Fatal("no alert to acknowledge")
	}

	res := call(t, app, http.MethodPost, "/pipeline/alerts/"+itoa(alerts[0].ID)+"/ack", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("acknowledging returned %d, want 200", res.StatusCode)
	}

	var acknowledged Alert
	decode(t, res, &acknowledged)
	// Who looked matters as much as that someone did.
	if acknowledged.AcknowledgedBy != "admin@example.com" {
		t.Errorf("acknowledged by %q, want the authenticated identity", acknowledged.AcknowledgedBy)
	}
	if len(openAlerts(t, app, token)) != 0 {
		t.Error("an acknowledged alert is still on the board")
	}
	// It stays in the history, though — acknowledging is not deleting.
	history := []Alert{}
	decode(t, call(t, app, http.MethodGet, "/pipeline/alerts?all=true", token, nil), &history)
	if len(history) == 0 {
		t.Error("acknowledging an alert erased it from the history")
	}
}

func TestOverviewCountsOpenAlerts(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "admin@example.com", RoleAdmin)

	episode := newEpisode(t, app, token)
	advance(t, app, token, episode.ID, StatusScriptDraft)

	var overview PipelineOverview
	decode(t, call(t, app, http.MethodGet, "/pipeline/overview", token, nil), &overview)
	if overview.OpenAlerts != 1 {
		t.Errorf("overview reports %d open alerts, want 1", overview.OpenAlerts)
	}
}

/* ---------- delivery ---------- */

type webhookSpy struct {
	mu       sync.Mutex
	payloads []map[string]any
	status   int
	received chan struct{}
}

func newWebhookSpy(t *testing.T) *webhookSpy {
	t.Helper()

	spy := &webhookSpy{received: make(chan struct{}, 10)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		json.Unmarshal(body, &payload)

		spy.mu.Lock()
		spy.payloads = append(spy.payloads, payload)
		status := spy.status
		spy.mu.Unlock()

		if status != 0 {
			w.WriteHeader(status)
		}
		select {
		case spy.received <- struct{}{}:
		default:
		}
	}))
	t.Cleanup(server.Close)

	t.Setenv("ALERT_WEBHOOK_URL", server.URL)
	return spy
}

func (s *webhookSpy) wait(t *testing.T) map[string]any {
	t.Helper()

	select {
	case <-s.received:
	case <-time.After(3 * time.Second):
		t.Fatal("the webhook was never called")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.payloads[len(s.payloads)-1]
}

func TestAlertsArePushedToTheWebhook(t *testing.T) {
	app := newTestApp(t)
	spy := newWebhookSpy(t)
	token := tokenFor(t, "script-agent", RoleAgent)

	episode := newEpisode(t, app, token)
	advance(t, app, token, episode.ID, StatusScriptDraft)

	payload := spy.wait(t)
	// A plain text field is what Slack and Discord read, so the same webhook
	// works with either without a translation layer.
	text, ok := payload["text"].(string)
	if !ok || !strings.Contains(text, AlertGateWaiting) {
		t.Errorf("webhook payload text is %v", payload["text"])
	}
	if _, ok := payload["alert"]; !ok {
		t.Error("webhook payload carries no structured alert")
	}
}

// An alert is a notification about work, not part of it: a webhook that is
// down must not fail the transition that triggered it.
func TestAFailingWebhookDoesNotFailTheTransition(t *testing.T) {
	app := newTestApp(t)
	spy := newWebhookSpy(t)
	spy.status = http.StatusInternalServerError
	token := tokenFor(t, "script-agent", RoleAgent)

	episode := newEpisode(t, app, token)
	res := call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), token, TransitionRequest{
		ToStatus: StatusScriptDraft,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the transition returned %d while the webhook was failing", res.StatusCode)
	}

	spy.wait(t)
	// The alert is still recorded even though it could not be delivered.
	if waiting := alertsOfKind(openAlerts(t, app, token), AlertGateWaiting); len(waiting) != 1 {
		t.Errorf("a failed delivery lost the alert: %d open", len(waiting))
	}
}

func TestNoWebhookConfiguredStillRecordsAlerts(t *testing.T) {
	app := newTestApp(t)
	t.Setenv("ALERT_WEBHOOK_URL", "")
	token := tokenFor(t, "script-agent", RoleAgent)

	episode := newEpisode(t, app, token)
	advance(t, app, token, episode.ID, StatusScriptDraft)

	if waiting := alertsOfKind(openAlerts(t, app, token), AlertGateWaiting); len(waiting) != 1 {
		t.Error("alerts are lost when no webhook is configured")
	}
}
