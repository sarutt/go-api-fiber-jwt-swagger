// Package script is the first real production agent: it turns a curriculum
// topic into an episode script and song lyrics, then hands the episode to the
// human review gate.
//
// Two things matter more than the model call itself. The cast and sets are
// read from the pipeline's show bible on every run rather than copied into a
// prompt constant, so the characters cannot drift as the bible is edited. And
// the model's output is validated before it is written back — a script naming
// a character who does not exist, or missing one of the six beats, is a failed
// attempt rather than something a reviewer has to catch.
package script

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"gitlhub.com/sarutt/apifiber/worker"
)

// Beat names the six parts of the standard episode, in order.
const (
	BeatOpeningSong = "OPENING_SONG"
	BeatHook        = "HOOK"
	BeatAdventure   = "ADVENTURE"
	BeatTeamwork    = "TEAMWORK"
	BeatRecapSong   = "RECAP_SONG"
	BeatClosing     = "CLOSING"
)

// requiredBeats is the episode template every script must fill.
var requiredBeats = []string{
	BeatOpeningSong,
	BeatHook,
	BeatAdventure,
	BeatTeamwork,
	BeatRecapSong,
	BeatClosing,
}

// DefaultModel is the model the agent writes with unless one is configured.
const DefaultModel = "claude-opus-5"

// Config describes one script agent.
type Config struct {
	// Pipeline reads the show bible and writes the finished script back.
	Pipeline *worker.Client

	// APIKey authenticates to the Claude API. Empty falls back to the
	// SDK's own credential resolution.
	APIKey string
	// BaseURL overrides the Claude API endpoint. Used by the tests.
	BaseURL string
	Model   string
	// Effort trades thoroughness against cost and latency.
	Effort anthropic.OutputConfigEffort
}

// Agent writes episode scripts.
type Agent struct {
	config Config
	claude anthropic.Client
}

// New builds a script agent.
func New(config Config) (*Agent, error) {
	if config.Pipeline == nil {
		return nil, fmt.Errorf("script: a pipeline client is required")
	}
	if config.Model == "" {
		config.Model = DefaultModel
	}
	if config.Effort == "" {
		config.Effort = anthropic.OutputConfigEffortHigh
	}

	options := []option.RequestOption{}
	if config.APIKey != "" {
		options = append(options, option.WithAPIKey(config.APIKey))
	}
	if config.BaseURL != "" {
		options = append(options, option.WithBaseURL(config.BaseURL))
	}

	return &Agent{config: config, claude: anthropic.NewClient(options...)}, nil
}

// Handler adapts the agent to the worker runtime.
func (a *Agent) Handler() worker.Handler {
	return func(ctx context.Context, episode worker.Episode) (worker.Result, error) {
		return a.write(ctx, episode)
	}
}

/* ---------- show bible ---------- */

type character struct {
	Name    string `json:"name"`
	Species string `json:"species"`
	Role    string `json:"role"`
	Teaches string `json:"teaches"`
	Active  bool   `json:"active"`
}

type location struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	UsedFor     string `json:"used_for"`
}

type topic struct {
	ID              uint   `json:"id"`
	Theme           string `json:"theme"`
	Title           string `json:"title"`
	LearningGoal    string `json:"learning_goal"`
	DifficultyLevel int    `json:"difficulty_level"`
}

type bible struct {
	characters []character
	locations  []location
}

// loadBible reads the cast and sets from the pipeline. It is fetched per
// episode rather than cached so an edit to the bible takes effect on the next
// script rather than on the next restart.
func (a *Agent) loadBible(ctx context.Context) (*bible, error) {
	var loaded bible
	if _, err := a.config.Pipeline.Do(ctx, http.MethodGet, "/pipeline/characters", nil, &loaded.characters); err != nil {
		return nil, fmt.Errorf("reading the cast: %w", err)
	}
	if _, err := a.config.Pipeline.Do(ctx, http.MethodGet, "/pipeline/locations", nil, &loaded.locations); err != nil {
		return nil, fmt.Errorf("reading the sets: %w", err)
	}
	if len(loaded.characters) == 0 || len(loaded.locations) == 0 {
		return nil, fmt.Errorf("the show bible is empty: seed the cast and sets before writing scripts")
	}
	return &loaded, nil
}

/* ---------- the model's output ---------- */

// draft is the shape the model must return.
type draft struct {
	Title      string `json:"title"`
	Synopsis   string `json:"synopsis"`
	Location   string `json:"location"`
	Characters []struct {
		Name string `json:"name"`
	} `json:"characters"`
	SongTitle  string `json:"song_title"`
	SongLyrics string `json:"song_lyrics"`
	Beats      []struct {
		Beat   string `json:"beat"`
		Script string `json:"script"`
	} `json:"beats"`
}

// draftSchema constrains the model's response. Structured output removes a
// whole class of failure: the reply is either valid against this shape or the
// request errors, so there is no JSON to repair.
func draftSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"title":    map[string]any{"type": "string"},
			"synopsis": map[string]any{"type": "string"},
			"location": map[string]any{"type": "string"},
			"characters": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"name": map[string]any{"type": "string"}},
					"required":             []string{"name"},
					"additionalProperties": false,
				},
			},
			"song_title":  map[string]any{"type": "string"},
			"song_lyrics": map[string]any{"type": "string"},
			"beats": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"beat":   map[string]any{"type": "string", "enum": requiredBeats},
						"script": map[string]any{"type": "string"},
					},
					"required":             []string{"beat", "script"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"title", "synopsis", "location", "characters", "song_title", "song_lyrics", "beats"},
		"additionalProperties": false,
	}
}

/* ---------- writing ---------- */

func (a *Agent) write(ctx context.Context, episode worker.Episode) (worker.Result, error) {
	showBible, err := a.loadBible(ctx)
	if err != nil {
		return worker.Result{}, err
	}

	var next topic
	status, err := a.config.Pipeline.Do(ctx, http.MethodGet, "/pipeline/curriculum-topics/next", nil, &next)
	if err != nil {
		if status == http.StatusNotFound {
			return worker.Result{}, fmt.Errorf("no curriculum topic is due: add more to the backlog")
		}
		return worker.Result{}, fmt.Errorf("reading the curriculum backlog: %w", err)
	}

	written, err := a.draftScript(ctx, showBible, next, episode)
	if err != nil {
		return worker.Result{}, err
	}
	if err := validate(written, showBible); err != nil {
		// A rejected draft is a failed attempt, so the pipeline retries it and
		// eventually parks the episode rather than sending a broken script to
		// a reviewer.
		return worker.Result{}, fmt.Errorf("the draft was rejected: %w", err)
	}

	update := map[string]any{
		"title":               written.Title,
		"synopsis":            written.Synopsis,
		"script_text":         renderScript(written),
		"song_title":          written.SongTitle,
		"song_lyrics":         written.SongLyrics,
		"curriculum_topic_id": next.ID,
	}
	path := fmt.Sprintf("/pipeline/episodes/%d", episode.ID)
	if _, err := a.config.Pipeline.Do(ctx, http.MethodPut, path, update, nil); err != nil {
		return worker.Result{}, fmt.Errorf("saving the script: %w", err)
	}

	// Retiring the topic last means a failure above leaves it available for
	// the retry rather than silently burning it.
	usedPath := fmt.Sprintf("/pipeline/curriculum-topics/%d/used", next.ID)
	if _, err := a.config.Pipeline.Do(ctx, http.MethodPost, usedPath, nil, nil); err != nil {
		return worker.Result{}, fmt.Errorf("retiring the curriculum topic: %w", err)
	}

	return worker.Result{
		NextStatus: "SCRIPT_DRAFT",
		Note:       fmt.Sprintf("script written for %s: %s", next.Theme, next.Title),
	}, nil
}

// draftScript asks the model for one episode.
func (a *Agent) draftScript(ctx context.Context, showBible *bible, next topic, episode worker.Episode) (*draft, error) {
	adaptive := anthropic.ThinkingConfigAdaptiveParam{}

	message, err := a.claude.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.config.Model),
		MaxTokens: 16000,
		Thinking:  anthropic.ThinkingConfigParamUnion{OfAdaptive: &adaptive},
		OutputConfig: anthropic.OutputConfigParam{
			Effort: a.config.Effort,
			Format: anthropic.JSONOutputFormatParam{Schema: draftSchema()},
		},
		System: []anthropic.TextBlockParam{{Text: systemPrompt(showBible)}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(episodeBrief(next, episode))),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("asking the model for a script: %w", err)
	}

	var written draft
	for _, block := range message.Content {
		text, ok := block.AsAny().(anthropic.TextBlock)
		if !ok {
			continue
		}
		if err := json.Unmarshal([]byte(text.Text), &written); err != nil {
			return nil, fmt.Errorf("reading the model's reply: %w", err)
		}
		return &written, nil
	}
	return nil, fmt.Errorf("the model returned no script")
}

// systemPrompt describes the show. The cast and sets come from the bible the
// pipeline holds, so this text cannot fall out of step with it.
func systemPrompt(showBible *bible) string {
	var b strings.Builder

	b.WriteString("You write episodes of Pom Pom Hollow, an animated musical series for children aged two to six.\n\n")
	b.WriteString("The cast. Use only these characters, spelled exactly as written:\n")
	for _, c := range showBible.characters {
		if !c.Active {
			continue
		}
		fmt.Fprintf(&b, "- %s, a %s. %s. Teaches %s.\n", c.Name, c.Species, c.Role, c.Teaches)
	}

	b.WriteString("\nThe sets. Choose one as the episode's main location, named exactly as written:\n")
	for _, l := range showBible.locations {
		fmt.Fprintf(&b, "- %s: %s Used for %s.\n", l.Name, l.Description, l.UsedFor)
	}

	b.WriteString(`
Every episode follows the same six beats:
- OPENING_SONG: the recurring theme song, "Hop Hop into Pom Pom Hollow!"
- HOOK: Pommy notices a problem or a question. Ten to fifteen seconds.
- ADVENTURE: the friends travel to the episode's location; the lesson plays out in song.
- TEAMWORK: the friends solve the problem together.
- RECAP_SONG: the lesson is repeated, and the audience is asked to join in.
- CLOSING: everyone returns to the Great Pom Tree.

Writing for this audience:
- Short sentences. Concrete nouns. Words a three-year-old already knows.
- Nothing frightening, no peril, no unkindness left unresolved.
- Teach the curriculum goal through what the characters do, not by explaining it.
- Give the audience something to say or count along with.
- The whole episode reads aloud in four to six minutes.

Write the script as dialogue and simple stage directions. Write the song as
lyrics only, with line breaks — no chords and no notation.`)

	return b.String()
}

// episodeBrief is the per-episode instruction.
func episodeBrief(next topic, episode worker.Episode) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Write episode %s.\n\n", episode.Code)
	fmt.Fprintf(&b, "This episode teaches: %s (%s).\n", next.Title, next.Theme)
	fmt.Fprintf(&b, "The learning goal is: %s\n", next.LearningGoal)
	fmt.Fprintf(&b, "Difficulty level %d, so keep it within what that level of the curriculum has covered.\n", next.DifficultyLevel)

	if episode.Title != "" {
		fmt.Fprintf(&b, "\nA working title already exists: %q. Keep it if it suits the episode, or replace it.\n", episode.Title)
	}
	if episode.Synopsis != "" {
		fmt.Fprintf(&b, "A synopsis was sketched out: %s\n", episode.Synopsis)
	}
	return b.String()
}

/* ---------- validation ---------- */

// validate refuses a draft that would not survive production: a character or
// set that does not exist, a missing beat, or an empty song.
func validate(written *draft, showBible *bible) error {
	if strings.TrimSpace(written.Title) == "" {
		return fmt.Errorf("no title")
	}
	if strings.TrimSpace(written.SongLyrics) == "" {
		return fmt.Errorf("no song lyrics")
	}

	knownLocations := map[string]bool{}
	for _, l := range showBible.locations {
		knownLocations[l.Name] = true
	}
	if !knownLocations[written.Location] {
		return fmt.Errorf("location %q is not in the show bible (have: %s)", written.Location, names(knownLocations))
	}

	knownCharacters := map[string]bool{}
	for _, c := range showBible.characters {
		if c.Active {
			knownCharacters[c.Name] = true
		}
	}
	if len(written.Characters) == 0 {
		return fmt.Errorf("no characters")
	}
	for _, c := range written.Characters {
		if !knownCharacters[c.Name] {
			return fmt.Errorf("character %q is not in the show bible (have: %s)", c.Name, names(knownCharacters))
		}
	}

	seen := map[string]bool{}
	for _, beat := range written.Beats {
		if strings.TrimSpace(beat.Script) == "" {
			return fmt.Errorf("beat %s is empty", beat.Beat)
		}
		if seen[beat.Beat] {
			return fmt.Errorf("beat %s appears more than once", beat.Beat)
		}
		seen[beat.Beat] = true
	}
	for _, required := range requiredBeats {
		if !seen[required] {
			return fmt.Errorf("beat %s is missing", required)
		}
	}
	return nil
}

// names lists a set for an error message, in a stable order.
func names(set map[string]bool) string {
	listed := make([]string, 0, len(set))
	for name := range set {
		listed = append(listed, name)
	}
	sort.Strings(listed)
	return strings.Join(listed, ", ")
}

// renderScript lays the beats out in template order, whatever order the model
// returned them in.
func renderScript(written *draft) string {
	byBeat := map[string]string{}
	for _, beat := range written.Beats {
		byBeat[beat.Beat] = beat.Script
	}

	var b strings.Builder
	for i, beat := range requiredBeats {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "## %s\n\n%s", beat, strings.TrimSpace(byBeat[beat]))
	}
	return b.String()
}
