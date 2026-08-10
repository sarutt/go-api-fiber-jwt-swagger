package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	jwtware "github.com/gofiber/jwt/v2"
	"github.com/golang-jwt/jwt/v4"
)

const testSecret = "test-secret"

// newTestApp builds the pipeline API over a fresh in-memory database, wired
// with the same JWT middleware main.go uses.
func newTestApp(t *testing.T) *fiber.App {
	// A private cache keeps each test's database to itself.
	return newTestAppAt(t, "file::memory:")
}

// newTestAppAt is newTestApp against a named database, for tests that need
// several connections to see the same data.
func newTestAppAt(t *testing.T, path string) *fiber.App {
	t.Helper()

	if err := setupDB(path); err != nil {
		t.Fatalf("cannot set up test database: %v", err)
	}

	app := fiber.New(fiber.Config{BodyLimit: bodyLimit()})
	app.Use(jwtware.New(jwtware.Config{
		SigningKey: []byte(testSecret),
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			return c.Status(fiber.StatusUnauthorized).JSON(ErrorResponse{Error: "Unauthorized"})
		},
	}))
	registerPipelineRoutes(app)
	return app
}

// tokenFor mints a token for an identity in the given role.
func tokenFor(t *testing.T, subject, role string) string {
	t.Helper()

	token := jwt.New(jwt.SigningMethodHS256)
	claims := token.Claims.(jwt.MapClaims)
	claims["sub"] = subject
	claims["role"] = role
	claims["exp"] = time.Now().Add(time.Hour).Unix()

	signed, err := token.SignedString([]byte(testSecret))
	if err != nil {
		t.Fatalf("cannot sign test token: %v", err)
	}
	return signed
}

// call performs a request and returns the response. A nil body sends none.
func call(t *testing.T, app *fiber.App, method, path, token string, body any) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("cannot encode request body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	res, err := app.Test(req, 5000)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, path, err)
	}
	return res
}

// decode reads a JSON response body into target.
func decode(t *testing.T, res *http.Response, target any) {
	t.Helper()
	if err := json.NewDecoder(res.Body).Decode(target); err != nil {
		t.Fatalf("cannot decode response: %v", err)
	}
}

// newEpisode creates an episode and returns it.
func newEpisode(t *testing.T, app *fiber.App, token string) Episode {
	t.Helper()

	res := call(t, app, http.MethodPost, "/pipeline/episodes", token, map[string]any{
		"title": "Counting Flowers in Giggle Meadow",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create episode returned %d, want 201", res.StatusCode)
	}

	var episode Episode
	decode(t, res, &episode)
	return episode
}

// advance moves an episode through a run of ungated stages.
func advance(t *testing.T, app *fiber.App, token string, id uint, stages ...EpisodeStatus) {
	t.Helper()

	for _, stage := range stages {
		res := call(t, app, http.MethodPost, episodePath(id, "transition"), token, TransitionRequest{ToStatus: stage})
		if res.StatusCode != http.StatusOK {
			t.Fatalf("transition to %s returned %d, want 200", stage, res.StatusCode)
		}
	}
}

func episodePath(id uint, suffix string) string {
	path := "/pipeline/episodes/" + itoa(id)
	if suffix != "" {
		path += "/" + suffix
	}
	return path
}

func itoa(v uint) string {
	return strconv.FormatUint(uint64(v), 10)
}

// The gate authorisation check: an agent token must not be able to record a
// decision at a gate, which is the guarantee the whole pipeline rests on.
func TestAgentCannotApproveAtAGate(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), agentToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionApproved,
	})
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("agent approval returned %d, want 403", res.StatusCode)
	}

	// And the episode must not have moved.
	var after Episode
	decode(t, call(t, app, http.MethodGet, episodePath(episode.ID, ""), agentToken, nil), &after)
	if after.Status != StatusScriptDraft {
		t.Errorf("episode moved to %s after a refused approval", after.Status)
	}
}

func TestReviewerCanApproveAtAGate(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)
	reviewerToken := tokenFor(t, "advisor@studio.com", RoleReviewer)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), reviewerToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionApproved,
		Notes:    "Age appropriate.",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("reviewer approval returned %d, want 201", res.StatusCode)
	}

	var approval ApprovalLog
	decode(t, res, &approval)
	if approval.ToStatus != StatusScriptApproved {
		t.Errorf("approval moved episode to %s, want %s", approval.ToStatus, StatusScriptApproved)
	}
	// The decision is attributed to the token, never to the request body.
	if approval.Reviewer != "advisor@studio.com" {
		t.Errorf("approval attributed to %q, want the authenticated identity", approval.Reviewer)
	}
}

func TestAgentCannotCrossAGateThroughTheTransitionEndpoint(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), agentToken, TransitionRequest{
		ToStatus: StatusScriptApproved,
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("gate crossing via transition returned %d, want 409", res.StatusCode)
	}
}

func TestEpisodeAlwaysEntersAtTheFirstStage(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "someone", RoleAgent)

	res := call(t, app, http.MethodPost, "/pipeline/episodes", token, map[string]any{
		"title":         "Trying to skip ahead",
		"status":        StatusPublished,
		"made_for_kids": false,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create returned %d, want 201", res.StatusCode)
	}

	var episode Episode
	decode(t, res, &episode)
	if episode.Status != StatusIdeaBacklog {
		t.Errorf("episode created at %s, want %s", episode.Status, StatusIdeaBacklog)
	}
	// The compliance flags are set by the pipeline, not by the caller.
	if !episode.MadeForKids {
		t.Error("made_for_kids was overridden by the request body")
	}
}

func TestUpdateCannotChangeStatus(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "someone", RoleAgent)

	episode := newEpisode(t, app, token)
	res := call(t, app, http.MethodPut, episodePath(episode.ID, ""), token, map[string]any{
		"title":  "Renamed",
		"status": StatusPublished,
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("update returned %d, want 200", res.StatusCode)
	}

	var updated Episode
	decode(t, res, &updated)
	if updated.Status != StatusIdeaBacklog {
		t.Errorf("update changed status to %s", updated.Status)
	}
	if updated.Title != "Renamed" {
		t.Errorf("update did not apply the title, got %q", updated.Title)
	}
}

func TestRejectionSendsTheEpisodeBackForRework(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)
	reviewerToken := tokenFor(t, "advisor@studio.com", RoleReviewer)

	episode := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, episode.ID, StatusScriptDraft)

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), reviewerToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionRejected,
		Notes:    "Vocabulary too advanced.",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("rejection returned %d, want 201", res.StatusCode)
	}

	var after Episode
	decode(t, call(t, app, http.MethodGet, episodePath(episode.ID, ""), agentToken, nil), &after)
	if after.Status != StatusIdeaBacklog {
		t.Errorf("rejected episode sits at %s, want %s", after.Status, StatusIdeaBacklog)
	}
}

func TestGateRefusesAnEpisodeAtTheWrongStage(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)
	reviewerToken := tokenFor(t, "producer@studio.com", RoleReviewer)

	episode := newEpisode(t, app, agentToken)

	// Still in IDEA_BACKLOG, so there is nothing to review yet.
	res := call(t, app, http.MethodPost, episodePath(episode.ID, "approvals"), reviewerToken, ApprovalRequest{
		Gate:     Gate1Script,
		Decision: DecisionApproved,
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("premature approval returned %d, want 409", res.StatusCode)
	}
}

func TestFullRunToPublished(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "pipeline-agent", RoleAgent)
	advisorToken := tokenFor(t, "advisor@studio.com", RoleReviewer)
	producerToken := tokenFor(t, "producer@studio.com", RoleReviewer)

	episode := newEpisode(t, app, agentToken)

	advance(t, app, agentToken, episode.ID, StatusScriptDraft)
	approve(t, app, advisorToken, episode.ID, Gate1Script)
	advance(t, app, agentToken, episode.ID,
		StatusVOGenerated,
		StatusMusicGenerated,
		StatusAnimationRendered,
		StatusAssembled,
		StatusQAReview,
	)
	approve(t, app, producerToken, episode.ID, Gate2Release)
	advance(t, app, agentToken, episode.ID, StatusScheduled, StatusPublished, StatusAnalyzed)

	var final Episode
	decode(t, call(t, app, http.MethodGet, episodePath(episode.ID, ""), agentToken, nil), &final)
	if final.Status != StatusAnalyzed {
		t.Fatalf("episode finished at %s, want %s", final.Status, StatusAnalyzed)
	}
	if final.PublishedAt == nil {
		t.Error("published_at was not stamped when the episode reached PUBLISHED")
	}

	// The audit trail must cover the whole run, both gates included.
	var events []EpisodeEvent
	decode(t, call(t, app, http.MethodGet, episodePath(episode.ID, "events"), agentToken, nil), &events)
	if len(events) != 12 {
		t.Errorf("recorded %d events for a full run, want 12", len(events))
	}

	var approvals []ApprovalLog
	decode(t, call(t, app, http.MethodGet, episodePath(episode.ID, "approvals"), agentToken, nil), &approvals)
	if len(approvals) != 2 {
		t.Fatalf("recorded %d gate decisions, want 2", len(approvals))
	}
	if approvals[0].Reviewer != "advisor@studio.com" || approvals[1].Reviewer != "producer@studio.com" {
		t.Errorf("gate decisions attributed to %q and %q", approvals[0].Reviewer, approvals[1].Reviewer)
	}
}

func approve(t *testing.T, app *fiber.App, token string, id uint, gate string) {
	t.Helper()

	res := call(t, app, http.MethodPost, episodePath(id, "approvals"), token, ApprovalRequest{
		Gate:     gate,
		Decision: DecisionApproved,
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("approval at %s returned %d, want 201", gate, res.StatusCode)
	}
}

func TestPendingReviewsListsWhatIsWaitingOnAHuman(t *testing.T) {
	app := newTestApp(t)
	agentToken := tokenFor(t, "script-agent", RoleAgent)
	reviewerToken := tokenFor(t, "advisor@studio.com", RoleReviewer)

	waiting := newEpisode(t, app, agentToken)
	advance(t, app, agentToken, waiting.ID, StatusScriptDraft)
	newEpisode(t, app, agentToken) // still in the backlog, not waiting on anyone

	var pending []Episode
	decode(t, call(t, app, http.MethodGet, "/pipeline/reviews/pending?gate="+Gate1Script, reviewerToken, nil), &pending)
	if len(pending) != 1 {
		t.Fatalf("pending review queue had %d episodes, want 1", len(pending))
	}
	if pending[0].ID != waiting.ID {
		t.Errorf("pending queue returned episode %d, want %d", pending[0].ID, waiting.ID)
	}
}

func TestPipelineRequiresAuthentication(t *testing.T) {
	app := newTestApp(t)

	res := call(t, app, http.MethodGet, "/pipeline/episodes", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("unauthenticated request returned %d, want 401", res.StatusCode)
	}
}

func TestQueueRejectsAnUnknownStage(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent", RoleAgent)

	res := call(t, app, http.MethodGet, "/pipeline/queue?status=NOT_A_STAGE", token, nil)
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown stage returned %d, want 400", res.StatusCode)
	}
}

func TestAssetsAreFiledAgainstTheEpisodeInThePath(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "editing-agent", RoleAgent)

	episode := newEpisode(t, app, token)
	res := call(t, app, http.MethodPost, episodePath(episode.ID, "assets"), token, map[string]any{
		"episode_id":   9999, // must be ignored in favour of the path
		"kind":         AssetMasterVideo,
		"uri":          "s3://pph/ep1/master.mp4",
		"generated_by": "editing-agent",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create asset returned %d, want 201", res.StatusCode)
	}

	var asset Asset
	decode(t, res, &asset)
	if asset.EpisodeID != episode.ID {
		t.Errorf("asset filed against episode %d, want %d", asset.EpisodeID, episode.ID)
	}
}

func TestNextCurriculumTopicFollowsTheRotation(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "content-idea-agent", RoleAgent)

	var first CurriculumTopic
	decode(t, call(t, app, http.MethodGet, "/pipeline/curriculum-topics/next", token, nil), &first)
	if first.Theme != ThemeCounting || first.DifficultyLevel != 1 {
		t.Fatalf("first topic was %s level %d, want %s level 1", first.Theme, first.DifficultyLevel, ThemeCounting)
	}

	res := call(t, app, http.MethodPost, "/pipeline/curriculum-topics/"+itoa(first.ID)+"/used", token, nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("marking topic used returned %d, want 200", res.StatusCode)
	}

	var second CurriculumTopic
	decode(t, call(t, app, http.MethodGet, "/pipeline/curriculum-topics/next", token, nil), &second)
	if second.ID == first.ID {
		t.Error("the rotation returned the same topic after it was marked used")
	}
	if second.Theme != ThemeLetters {
		t.Errorf("second topic was %s, want %s", second.Theme, ThemeLetters)
	}
}

func TestShowBibleIsSeededOnce(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent", RoleAgent)

	var characters []Character
	decode(t, call(t, app, http.MethodGet, "/pipeline/characters", token, nil), &characters)
	if len(characters) != 7 {
		t.Errorf("seeded %d characters, want 7", len(characters))
	}

	// Seeding again must not duplicate rows.
	seedShowBible()
	decode(t, call(t, app, http.MethodGet, "/pipeline/characters", token, nil), &characters)
	if len(characters) != 7 {
		t.Errorf("re-seeding produced %d characters, want 7", len(characters))
	}
}
