// Package worker is the runtime every production agent is built on. An agent
// supplies a Handler that does one stage's actual work — call a model, render
// audio, composite a video — and this package deals with everything around
// it: taking exactly one job at a time, keeping the claim alive while a long
// job runs, moving the episode on, and reporting failures so a job that
// cannot succeed stops being retried.
//
// It talks to the pipeline over HTTP rather than sharing the server's types,
// so agents can live in their own repositories and be written in their own
// time.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Episode is the part of a pipeline episode a worker needs. It is
// deliberately not the server's model: an agent should not care about
// storage details, and the two can move independently.
type Episode struct {
	ID         uint   `json:"id"`
	Code       string `json:"code"`
	Title      string `json:"title"`
	Synopsis   string `json:"synopsis"`
	Status     string `json:"status"`
	ScriptText string `json:"script_text"`
	SongTitle  string `json:"song_title"`
	SongLyrics string `json:"song_lyrics"`
	Attempts   int    `json:"attempts"`
}

// Asset is a file the handler produced and wants recorded against the episode.
type Asset struct {
	Kind            string `json:"kind"`
	Platform        string `json:"platform,omitempty"`
	URI             string `json:"uri"`
	GeneratedBy     string `json:"generated_by,omitempty"`
	DurationSeconds int    `json:"duration_seconds,omitempty"`
}

// Result is what a handler returns when its stage is done.
type Result struct {
	// NextStatus is the stage to move the episode to. Leave it empty to keep
	// the episode where it is and simply release it.
	NextStatus string
	Assets     []Asset
	Note       string
}

// Handler does one stage's work. Returning an error reports the job as
// failed; the pipeline retries it until the attempt limit and then parks it.
type Handler func(ctx context.Context, episode Episode) (Result, error)

// Config describes one worker process.
type Config struct {
	BaseURL  string // e.g. http://localhost:8080
	Token    string // JWT from POST /login
	WorkerID string // unique per process, e.g. voiceover-agent-7
	Stage    string // the pipeline stage this worker serves

	// PollInterval is the wait after finding no work. PausedInterval is the
	// longer wait used when the operator has paused production, so a paused
	// pipeline is not hammered. Both have sensible defaults.
	PollInterval   time.Duration
	PausedInterval time.Duration

	// HeartbeatInterval extends the claim while a long job runs. It must be
	// comfortably shorter than the server's lease.
	HeartbeatInterval time.Duration

	HTTPClient *http.Client
	Logger     *log.Logger
}

func (c *Config) applyDefaults() {
	if c.PollInterval <= 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.PausedInterval <= 0 {
		c.PausedInterval = 60 * time.Second
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 2 * time.Minute
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if c.Logger == nil {
		c.Logger = log.Default()
	}
}

// Worker runs one stage.
type Worker struct {
	config  Config
	handler Handler

	// paused tracks whether the last poll was refused, so the worker says so
	// once rather than either spamming the log every poll or going silent.
	// Only Run touches it, and Run is a single goroutine.
	paused bool
}

// New builds a worker. It fails rather than starting half-configured, since a
// worker missing its stage or identity would silently do nothing useful.
func New(config Config, handler Handler) (*Worker, error) {
	if config.BaseURL == "" {
		return nil, errors.New("worker: BaseURL is required")
	}
	if config.WorkerID == "" {
		return nil, errors.New("worker: WorkerID is required, and must be unique per process")
	}
	if config.Stage == "" {
		return nil, errors.New("worker: Stage is required")
	}
	if handler == nil {
		return nil, errors.New("worker: a handler is required")
	}
	config.applyDefaults()
	return &Worker{config: config, handler: handler}, nil
}

// Run polls until the context is cancelled. A cancelled context stops the
// worker between jobs, so shutting one down does not abandon a claim.
func (w *Worker) Run(ctx context.Context) error {
	w.config.Logger.Printf("worker %s serving %s", w.config.WorkerID, w.config.Stage)

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		wait, err := w.runOnce(ctx)
		if err != nil {
			// A transport failure must not kill the worker: the pipeline may
			// simply be restarting. Back off and try again.
			w.config.Logger.Printf("worker %s: %v", w.config.WorkerID, err)
			wait = w.config.PollInterval
		}

		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(wait):
			}
		}
	}
}

// runOnce claims at most one episode and sees it through. The returned
// duration is how long to wait before polling again.
func (w *Worker) runOnce(ctx context.Context) (time.Duration, error) {
	episode, state, err := w.claim(ctx)
	if err != nil {
		return 0, err
	}

	// An operator watching worker logs should see a pause, not silence.
	if state == statePaused && !w.paused {
		w.config.Logger.Printf("worker %s: production is paused, waiting", w.config.WorkerID)
	} else if state != statePaused && w.paused {
		w.config.Logger.Printf("worker %s: production resumed", w.config.WorkerID)
	}
	w.paused = state == statePaused

	switch state {
	case stateEmpty:
		return w.config.PollInterval, nil
	case statePaused:
		return w.config.PausedInterval, nil
	}

	w.config.Logger.Printf("worker %s took %s at %s", w.config.WorkerID, episode.Code, w.config.Stage)

	// Keep the claim alive for as long as the handler runs, so a slow job is
	// not handed to a second worker mid-flight.
	jobCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go w.heartbeat(jobCtx, episode.ID)

	result, err := w.handler(jobCtx, *episode)
	stopHeartbeat()

	if err != nil {
		w.config.Logger.Printf("worker %s failed %s: %v", w.config.WorkerID, episode.Code, err)
		if reportErr := w.reportFailure(ctx, episode.ID, err); reportErr != nil {
			return 0, reportErr
		}
		return 0, nil
	}

	if err := w.publishResult(ctx, episode, result); err != nil {
		// The work itself succeeded but recording it did not. Report the
		// failure so the episode is retried rather than left claimed.
		w.config.Logger.Printf("worker %s could not record %s: %v", w.config.WorkerID, episode.Code, err)
		if reportErr := w.reportFailure(ctx, episode.ID, err); reportErr != nil {
			return 0, reportErr
		}
	}
	return 0, nil
}

// publishResult records the handler's assets and moves the episode on.
func (w *Worker) publishResult(ctx context.Context, episode *Episode, result Result) error {
	for _, asset := range result.Assets {
		if asset.GeneratedBy == "" {
			asset.GeneratedBy = w.config.WorkerID
		}
		path := fmt.Sprintf("/pipeline/episodes/%d/assets", episode.ID)
		if _, err := w.do(ctx, http.MethodPost, path, asset, nil); err != nil {
			return fmt.Errorf("recording %s asset: %w", asset.Kind, err)
		}
	}

	if result.NextStatus == "" {
		// Nothing to advance to, so hand the episode back rather than
		// holding it until the lease runs out.
		return w.release(ctx, episode.ID)
	}

	path := fmt.Sprintf("/pipeline/episodes/%d/transition", episode.ID)
	body := map[string]any{
		"to_status": result.NextStatus,
		"worker_id": w.config.WorkerID,
		"actor":     w.config.WorkerID,
		"note":      result.Note,
	}
	if _, err := w.do(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("moving to %s: %w", result.NextStatus, err)
	}

	w.config.Logger.Printf("worker %s moved %s to %s", w.config.WorkerID, episode.Code, result.NextStatus)
	return nil
}

type claimState int

const (
	stateClaimed claimState = iota
	stateEmpty
	statePaused
)

// claim asks for one job, reporting whether the queue was empty or paused.
func (w *Worker) claim(ctx context.Context) (*Episode, claimState, error) {
	body := map[string]any{"status": w.config.Stage, "worker_id": w.config.WorkerID}

	var episode Episode
	status, err := w.do(ctx, http.MethodPost, "/pipeline/queue/claim", body, &episode)
	if err != nil {
		if status == http.StatusLocked {
			return nil, statePaused, nil
		}
		return nil, stateEmpty, err
	}
	if status == http.StatusNoContent {
		return nil, stateEmpty, nil
	}
	return &episode, stateClaimed, nil
}

// heartbeat extends the claim until the job finishes or the context ends.
func (w *Worker) heartbeat(ctx context.Context, episodeID uint) {
	ticker := time.NewTicker(w.config.HeartbeatInterval)
	defer ticker.Stop()

	path := fmt.Sprintf("/pipeline/episodes/%d/heartbeat", episodeID)
	body := map[string]any{"worker_id": w.config.WorkerID}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.do(ctx, http.MethodPost, path, body, nil); err != nil {
				// Losing the claim is not recoverable from here: another
				// worker may already hold the episode. Say so and stop.
				w.config.Logger.Printf("worker %s lost its claim on episode %d: %v", w.config.WorkerID, episodeID, err)
				return
			}
		}
	}
}

func (w *Worker) release(ctx context.Context, episodeID uint) error {
	path := fmt.Sprintf("/pipeline/episodes/%d/release", episodeID)
	body := map[string]any{"worker_id": w.config.WorkerID}
	_, err := w.do(ctx, http.MethodPost, path, body, nil)
	return err
}

func (w *Worker) reportFailure(ctx context.Context, episodeID uint, cause error) error {
	path := fmt.Sprintf("/pipeline/episodes/%d/fail", episodeID)
	body := map[string]any{"worker_id": w.config.WorkerID, "error": cause.Error()}
	if _, err := w.do(ctx, http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("reporting failure: %w", err)
	}
	return nil
}

// do performs one API call through the worker's client.
func (w *Worker) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	return w.client().Do(ctx, method, path, body, out)
}

// Client is a small authenticated client for the pipeline API. The worker uses
// it internally, and an agent that needs calls the loop does not make for it —
// reading the show bible, say — can use the same one rather than rebuilding
// request plumbing.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// client exposes the worker's own configuration as a Client.
func (w *Worker) client() *Client {
	return &Client{BaseURL: w.config.BaseURL, Token: w.config.Token, HTTP: w.config.HTTPClient}
}

// NewClient builds a pipeline client. A nil http.Client gets a sane default.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{BaseURL: baseURL, Token: token, HTTP: httpClient}
}

// Do performs one API call, decoding a JSON response into out when given. It
// returns the status code even on error so the caller can tell a deliberate
// refusal — a pause, say — from a genuine problem.
func (c *Client) Do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	res, err := c.HTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusNoContent {
		return res.StatusCode, nil
	}
	if res.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return res.StatusCode, fmt.Errorf("%s %s: %s: %s", method, path, res.Status, bytes.TrimSpace(payload))
	}

	if out != nil {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			return res.StatusCode, fmt.Errorf("decoding %s response: %w", path, err)
		}
	}
	return res.StatusCode, nil
}
