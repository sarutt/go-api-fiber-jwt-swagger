package script

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"gitlhub.com/sarutt/apifiber/worker"
)

// fakePipeline stands in for the orchestrator: it serves a show bible and a
// curriculum topic, and records what the agent writes back.
type fakePipeline struct {
	mu sync.Mutex

	characters []character
	locations  []location
	topic      topic
	topicFound bool

	saved      map[string]any
	savedCalls int
	retired    bool
	failSave   bool
}

func newFakePipeline() *fakePipeline {
	return &fakePipeline{
		characters: []character{
			{Name: "Pommy", Species: "fox", Role: "the lead", Teaches: "courage", Active: true},
			{Name: "Bibi", Species: "rabbit", Role: "best friend", Teaches: "counting", Active: true},
			{Name: "Retired Rex", Species: "dog", Role: "written out", Active: false},
		},
		locations: []location{
			{Name: "Giggle Meadow", Description: "A flower field.", UsedFor: "counting"},
			{Name: "Star Pond", Description: "A still pond.", UsedFor: "feelings"},
		},
		topic:      topic{ID: 7, Theme: "COUNTING", Title: "Counting to five", LearningGoal: "Count to five.", DifficultyLevel: 1},
		topicFound: true,
	}
}

func (f *fakePipeline) server(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/pipeline/characters", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.characters)
	})
	mux.HandleFunc("/pipeline/locations", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(f.locations)
	})
	mux.HandleFunc("/pipeline/curriculum-topics/next", func(w http.ResponseWriter, r *http.Request) {
		if !f.topicFound {
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"Not Found"}`)
			return
		}
		json.NewEncoder(w).Encode(f.topic)
	})
	mux.HandleFunc("/pipeline/episodes/1", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.savedCalls++
		if f.failSave {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"error":"boom"}`)
			return
		}
		json.NewDecoder(r.Body).Decode(&f.saved)
		json.NewEncoder(w).Encode(map[string]any{"id": 1})
	})
	mux.HandleFunc("/pipeline/curriculum-topics/7/used", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.retired = true
		json.NewEncoder(w).Encode(map[string]any{"id": 7})
	})

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

// fakeClaude replies with a canned draft and records the prompt it was sent.
type fakeClaude struct {
	mu     sync.Mutex
	system string
	user   string
	reply  map[string]any
	status int
}

func (f *fakeClaude) server(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			System []struct {
				Text string `json:"text"`
			} `json:"system"`
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		w.Header().Set("Content-Type", "application/json")

		f.mu.Lock()
		if len(request.System) > 0 {
			f.system = request.System[0].Text
		}
		if len(request.Messages) > 0 && len(request.Messages[0].Content) > 0 {
			f.user = request.Messages[0].Content[0].Text
		}
		f.mu.Unlock()

		if f.status != 0 && f.status != http.StatusOK {
			w.WriteHeader(f.status)
			io.WriteString(w, `{"type":"error","error":{"type":"api_error","message":"boom"}}`)
			return
		}

		encoded, _ := json.Marshal(f.reply)
		json.NewEncoder(w).Encode(map[string]any{
			"id": "msg_1", "type": "message", "role": "assistant", "model": "claude-opus-5",
			"content":     []any{map[string]any{"type": "text", "text": string(encoded)}},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 10, "output_tokens": 20},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func (f *fakeClaude) prompt() (string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.system, f.user
}

// goodDraft is a draft that passes validation.
func goodDraft() map[string]any {
	beats := []any{}
	for _, beat := range requiredBeats {
		beats = append(beats, map[string]any{"beat": beat, "script": "POMMY: Look at the flowers!"})
	}
	return map[string]any{
		"title":       "Counting Flowers in Giggle Meadow",
		"synopsis":    "Pommy and Bibi count five flowers.",
		"location":    "Giggle Meadow",
		"characters":  []any{map[string]any{"name": "Pommy"}, map[string]any{"name": "Bibi"}},
		"song_title":  "Five Little Flowers",
		"song_lyrics": "One little flower, two little flowers...",
		"beats":       beats,
	}
}

func newAgent(t *testing.T, pipeline *fakePipeline, claude *fakeClaude) (*Agent, *fakePipeline, *fakeClaude) {
	t.Helper()

	pipelineServer := pipeline.server(t)
	claudeServer := claude.server(t)

	agent, err := New(Config{
		Pipeline: worker.NewClient(pipelineServer.URL, "test-token", nil),
		APIKey:   "test-key",
		BaseURL:  claudeServer.URL,
	})
	if err != nil {
		t.Fatalf("cannot build agent: %v", err)
	}
	return agent, pipeline, claude
}

func run(t *testing.T, agent *Agent) (worker.Result, error) {
	t.Helper()
	return agent.Handler()(context.Background(), worker.Episode{ID: 1, Code: "PPH-001"})
}

func TestAgentRequiresAPipeline(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("an agent with no pipeline client was accepted")
	}
}

func TestWritesScriptAndHandsToTheGate(t *testing.T) {
	claude := &fakeClaude{reply: goodDraft()}
	agent, pipeline, _ := newAgent(t, newFakePipeline(), claude)

	result, err := run(t, agent)
	if err != nil {
		t.Fatalf("writing the script failed: %v", err)
	}
	if result.NextStatus != "SCRIPT_DRAFT" {
		t.Errorf("agent moved the episode to %q, want SCRIPT_DRAFT", result.NextStatus)
	}

	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()

	if pipeline.saved["title"] != "Counting Flowers in Giggle Meadow" {
		t.Errorf("saved title is %v", pipeline.saved["title"])
	}
	if pipeline.saved["song_lyrics"] == "" {
		t.Error("the song lyrics were not saved")
	}
	// The episode must be linked to the topic it teaches, and the topic
	// retired so the rotation moves on.
	if id, ok := pipeline.saved["curriculum_topic_id"].(float64); !ok || uint(id) != 7 {
		t.Errorf("saved curriculum topic is %v, want 7", pipeline.saved["curriculum_topic_id"])
	}
	if !pipeline.retired {
		t.Error("the curriculum topic was not retired")
	}
}

// The cast is read from the pipeline rather than hardcoded, so an edit to the
// bible reaches the next script without a code change.
func TestPromptIsBuiltFromTheShowBible(t *testing.T) {
	pipeline := newFakePipeline()
	pipeline.characters = append(pipeline.characters, character{
		Name: "Nimbus", Species: "cloud sheep", Role: "newcomer", Teaches: "weather", Active: true,
	})

	claude := &fakeClaude{reply: goodDraft()}
	agent, _, _ := newAgent(t, pipeline, claude)

	if _, err := run(t, agent); err != nil {
		t.Fatalf("writing the script failed: %v", err)
	}

	system, user := claude.prompt()
	if !strings.Contains(system, "Nimbus") {
		t.Error("a character added to the bible did not reach the prompt")
	}
	if !strings.Contains(system, "Giggle Meadow") {
		t.Error("the sets did not reach the prompt")
	}
	// A character written out of the show must not be offered to the model.
	if strings.Contains(system, "Retired Rex") {
		t.Error("an inactive character reached the prompt")
	}
	// The brief has to carry the curriculum goal, or the episode teaches
	// nothing in particular.
	if !strings.Contains(user, "Counting to five") || !strings.Contains(user, "Count to five.") {
		t.Errorf("the curriculum goal did not reach the brief: %q", user)
	}
	if !strings.Contains(user, "PPH-001") {
		t.Error("the episode code did not reach the brief")
	}
}

// Validation is the point of the agent: a draft that would break production
// must fail the attempt rather than reach a reviewer.
func TestRejectsDraftsThatBreakTheShowBible(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		wantErr string
	}{
		{
			name:    "a character who does not exist",
			mutate:  func(d map[string]any) { d["characters"] = []any{map[string]any{"name": "Blorbo"}} },
			wantErr: "Blorbo",
		},
		{
			name:    "a set that does not exist",
			mutate:  func(d map[string]any) { d["location"] = "The Moon" },
			wantErr: "The Moon",
		},
		{
			name:    "a character written out of the show",
			mutate:  func(d map[string]any) { d["characters"] = []any{map[string]any{"name": "Retired Rex"}} },
			wantErr: "Retired Rex",
		},
		{
			name: "a missing beat",
			mutate: func(d map[string]any) {
				beats := d["beats"].([]any)
				d["beats"] = beats[:len(beats)-1]
			},
			wantErr: "CLOSING",
		},
		{
			name: "a duplicated beat",
			mutate: func(d map[string]any) {
				beats := d["beats"].([]any)
				d["beats"] = append(beats, map[string]any{"beat": BeatHook, "script": "again"})
			},
			wantErr: "more than once",
		},
		{
			name: "an empty beat",
			mutate: func(d map[string]any) {
				beats := d["beats"].([]any)
				beats[0].(map[string]any)["script"] = "   "
			},
			wantErr: "empty",
		},
		{
			name:    "no song",
			mutate:  func(d map[string]any) { d["song_lyrics"] = "" },
			wantErr: "song lyrics",
		},
		{
			name:    "no title",
			mutate:  func(d map[string]any) { d["title"] = "" },
			wantErr: "title",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := goodDraft()
			tc.mutate(bad)

			claude := &fakeClaude{reply: bad}
			agent, pipeline, _ := newAgent(t, newFakePipeline(), claude)

			_, err := run(t, agent)
			if err == nil {
				t.Fatal("a broken draft was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error was %q, want it to mention %q", err, tc.wantErr)
			}

			pipeline.mu.Lock()
			defer pipeline.mu.Unlock()
			if pipeline.savedCalls != 0 {
				t.Error("a rejected draft was still saved to the episode")
			}
			if pipeline.retired {
				t.Error("a rejected draft still retired the curriculum topic")
			}
		})
	}
}

// A failure after the model call must leave the topic available, or a retry
// writes the next episode against a topic nobody covered.
func TestAFailedSaveLeavesTheTopicAvailable(t *testing.T) {
	pipeline := newFakePipeline()
	pipeline.failSave = true

	claude := &fakeClaude{reply: goodDraft()}
	agent, _, _ := newAgent(t, pipeline, claude)

	if _, err := run(t, agent); err == nil {
		t.Fatal("a failed save was reported as success")
	}

	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	if pipeline.retired {
		t.Error("the curriculum topic was retired even though the script was not saved")
	}
}

func TestReportsAnEmptyCurriculumBacklog(t *testing.T) {
	pipeline := newFakePipeline()
	pipeline.topicFound = false

	claude := &fakeClaude{reply: goodDraft()}
	agent, _, _ := newAgent(t, pipeline, claude)

	_, err := run(t, agent)
	if err == nil {
		t.Fatal("an empty backlog was reported as success")
	}
	if !strings.Contains(err.Error(), "backlog") {
		t.Errorf("error was %q, want it to name the backlog", err)
	}
}

func TestReportsAnEmptyShowBible(t *testing.T) {
	pipeline := newFakePipeline()
	pipeline.characters = nil

	claude := &fakeClaude{reply: goodDraft()}
	agent, _, _ := newAgent(t, pipeline, claude)

	_, err := run(t, agent)
	if err == nil {
		t.Fatal("an empty show bible was reported as success")
	}
	if !strings.Contains(err.Error(), "show bible") {
		t.Errorf("error was %q, want it to name the show bible", err)
	}
}

func TestModelFailureIsReportedAsAFailedAttempt(t *testing.T) {
	claude := &fakeClaude{reply: goodDraft(), status: http.StatusInternalServerError}
	agent, pipeline, _ := newAgent(t, newFakePipeline(), claude)

	if _, err := run(t, agent); err == nil {
		t.Fatal("a model failure was reported as success")
	}

	pipeline.mu.Lock()
	defer pipeline.mu.Unlock()
	if pipeline.savedCalls != 0 {
		t.Error("a failed model call still wrote to the episode")
	}
}

// The rendered script must follow the template even if the model returns the
// beats in another order.
func TestScriptIsRenderedInTemplateOrder(t *testing.T) {
	written := &draft{}
	for i := len(requiredBeats) - 1; i >= 0; i-- {
		written.Beats = append(written.Beats, struct {
			Beat   string `json:"beat"`
			Script string `json:"script"`
		}{Beat: requiredBeats[i], Script: "line for " + requiredBeats[i]})
	}

	rendered := renderScript(written)
	position := 0
	for _, beat := range requiredBeats {
		at := strings.Index(rendered, "## "+beat)
		if at < 0 {
			t.Fatalf("beat %s is missing from the rendered script", beat)
		}
		if at < position {
			t.Errorf("beat %s is out of template order", beat)
		}
		position = at
	}
}

func TestSchemaPinsTheBeatsToTheTemplate(t *testing.T) {
	schema := draftSchema()
	beats := schema["properties"].(map[string]any)["beats"].(map[string]any)
	item := beats["items"].(map[string]any)
	enum := item["properties"].(map[string]any)["beat"].(map[string]any)["enum"].([]string)

	if len(enum) != len(requiredBeats) {
		t.Fatalf("the schema offers %d beats, the template has %d", len(enum), len(requiredBeats))
	}
	// A schema that drifts from the template lets the model return a beat the
	// renderer would silently drop.
	for i, beat := range requiredBeats {
		if enum[i] != beat {
			t.Errorf("schema beat %d is %q, template has %q", i, enum[i], beat)
		}
	}
	if item["additionalProperties"] != false {
		t.Error("the schema allows extra fields on a beat")
	}
}
