package worker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// recorder is a stub pipeline that records the calls a worker makes.
type recorder struct {
	mu    sync.Mutex
	calls []string

	// queue is handed out one episode per claim, then empty.
	queue  []Episode
	paused bool
}

func (r *recorder) record(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, path)
}

func (r *recorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func (r *recorder) server(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/pipeline/queue/claim", func(w http.ResponseWriter, req *http.Request) {
		r.record("claim")

		r.mu.Lock()
		defer r.mu.Unlock()

		if r.paused {
			w.WriteHeader(http.StatusLocked)
			io.WriteString(w, `{"error":"Paused","message":"operator stopped production"}`)
			return
		}
		if len(r.queue) == 0 {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next := r.queue[0]
		r.queue = r.queue[1:]
		json.NewEncoder(w).Encode(next)
	})

	for _, path := range []string{"assets", "transition", "release", "fail", "heartbeat"} {
		name := path
		mux.HandleFunc("/pipeline/episodes/1/"+name, func(w http.ResponseWriter, req *http.Request) {
			r.record(name)
			body, _ := io.ReadAll(req.Body)
			r.mu.Lock()
			r.calls = append(r.calls, name+":"+string(body))
			r.mu.Unlock()
			json.NewEncoder(w).Encode(map[string]any{"id": 1})
		})
	}

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server
}

func quietConfig(url string) Config {
	return Config{
		BaseURL:           url,
		WorkerID:          "test-worker",
		Stage:             "IDEA_BACKLOG",
		PollInterval:      10 * time.Millisecond,
		PausedInterval:    10 * time.Millisecond,
		HeartbeatInterval: time.Hour,
		Logger:            log.New(io.Discard, "", 0),
	}
}

// runBriefly runs the worker until the stop condition holds or time runs out.
func runBriefly(t *testing.T, w *Worker, stub *recorder, done func() bool) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() {
		w.Run(ctx)
		close(finished)
	}()

	deadline := time.After(3 * time.Second)
	for !done() {
		select {
		case <-deadline:
			cancel()
			<-finished
			t.Fatalf("worker did not reach the expected state, calls so far: %v", stub.seen())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-finished
}

func TestWorkerRequiresItsIdentityAndStage(t *testing.T) {
	handler := func(context.Context, Episode) (Result, error) { return Result{}, nil }

	cases := []struct {
		name   string
		config Config
	}{
		{"no base url", Config{WorkerID: "w", Stage: "S"}},
		{"no worker id", Config{BaseURL: "http://x", Stage: "S"}},
		{"no stage", Config{BaseURL: "http://x", WorkerID: "w"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.config, handler); err == nil {
				t.Error("a half-configured worker was accepted")
			}
		})
	}

	if _, err := New(Config{BaseURL: "http://x", WorkerID: "w", Stage: "S"}, nil); err == nil {
		t.Error("a worker with no handler was accepted")
	}
}

// The happy path: take a job, record what it produced, move it on.
func TestWorkerRecordsAssetsThenTransitions(t *testing.T) {
	stub := &recorder{queue: []Episode{{ID: 1, Code: "PPH-001", Status: "IDEA_BACKLOG"}}}
	server := stub.server(t)

	worker, err := New(quietConfig(server.URL), func(_ context.Context, episode Episode) (Result, error) {
		return Result{
			NextStatus: "SCRIPT_DRAFT",
			Assets:     []Asset{{Kind: "SCRIPT", URI: "s3://script.txt"}},
			Note:       "drafted",
		}, nil
	})
	if err != nil {
		t.Fatalf("cannot build worker: %v", err)
	}

	runBriefly(t, worker, stub, func() bool { return hasCall(stub.seen(), "transition") })

	calls := stub.seen()
	assetAt, transitionAt := indexOf(calls, "assets"), indexOf(calls, "transition")
	if assetAt < 0 || transitionAt < 0 {
		t.Fatalf("worker did not record and advance, calls: %v", calls)
	}
	// Assets must land before the episode moves, or the next stage sees an
	// episode whose inputs are not attached yet.
	if assetAt > transitionAt {
		t.Errorf("worker advanced the episode before recording its assets: %v", calls)
	}
	if !containsSubstring(calls, `"worker_id":"test-worker"`) {
		t.Error("the transition did not quote the worker's claim")
	}
}

// A handler that returns an error must report a failure, not silently drop
// the job or leave it claimed.
func TestWorkerReportsHandlerFailures(t *testing.T) {
	stub := &recorder{queue: []Episode{{ID: 1, Code: "PPH-001"}}}
	server := stub.server(t)

	worker, err := New(quietConfig(server.URL), func(context.Context, Episode) (Result, error) {
		return Result{}, errors.New("model call timed out")
	})
	if err != nil {
		t.Fatalf("cannot build worker: %v", err)
	}

	runBriefly(t, worker, stub, func() bool { return hasCall(stub.seen(), "fail") })

	calls := stub.seen()
	if hasCall(calls, "transition") {
		t.Error("a failed job still advanced the episode")
	}
	if !containsSubstring(calls, "model call timed out") {
		t.Errorf("the failure reason was not reported: %v", calls)
	}
}

// A handler with nothing to advance to should hand the episode back rather
// than sit on the claim until the lease expires.
func TestWorkerReleasesWhenThereIsNoNextStage(t *testing.T) {
	stub := &recorder{queue: []Episode{{ID: 1, Code: "PPH-001"}}}
	server := stub.server(t)

	worker, err := New(quietConfig(server.URL), func(context.Context, Episode) (Result, error) {
		return Result{}, nil
	})
	if err != nil {
		t.Fatalf("cannot build worker: %v", err)
	}

	runBriefly(t, worker, stub, func() bool { return hasCall(stub.seen(), "release") })

	if hasCall(stub.seen(), "transition") {
		t.Error("worker advanced an episode with no next stage")
	}
}

// A paused pipeline must not be hammered, and the worker must keep running so
// it resumes on its own once the operator lifts the pause.
func TestWorkerBacksOffWhilePausedAndDoesNotStop(t *testing.T) {
	stub := &recorder{paused: true, queue: []Episode{{ID: 1, Code: "PPH-001"}}}
	server := stub.server(t)

	config := quietConfig(server.URL)
	config.PausedInterval = 40 * time.Millisecond
	handled := make(chan struct{}, 1)

	worker, err := New(config, func(context.Context, Episode) (Result, error) {
		select {
		case handled <- struct{}{}:
		default:
		}
		return Result{NextStatus: "SCRIPT_DRAFT"}, nil
	})
	if err != nil {
		t.Fatalf("cannot build worker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go worker.Run(ctx)

	// While paused nothing is handled, however long we wait.
	select {
	case <-handled:
		t.Fatal("worker took work while production was paused")
	case <-time.After(200 * time.Millisecond):
	}

	// The operator lifts the pause and the worker picks up by itself.
	stub.mu.Lock()
	stub.paused = false
	stub.mu.Unlock()

	select {
	case <-handled:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not resume after the pause was lifted")
	}
}

// An unreachable pipeline must not kill the worker.
func TestWorkerSurvivesTheApiBeingDown(t *testing.T) {
	stub := &recorder{queue: []Episode{{ID: 1, Code: "PPH-001"}}}
	server := stub.server(t)

	config := quietConfig(server.URL)
	worker, err := New(config, func(context.Context, Episode) (Result, error) {
		return Result{NextStatus: "SCRIPT_DRAFT"}, nil
	})
	if err != nil {
		t.Fatalf("cannot build worker: %v", err)
	}

	// Close the server before the worker ever reaches it.
	server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	finished := make(chan error, 1)
	go func() { finished <- worker.Run(ctx) }()

	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("worker exited with %v, want a clean stop on context cancellation", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop when its context was cancelled")
	}
}

func TestWorkerStopsBetweenJobsOnCancel(t *testing.T) {
	stub := &recorder{}
	server := stub.server(t)

	worker, err := New(quietConfig(server.URL), func(context.Context, Episode) (Result, error) {
		return Result{}, nil
	})
	if err != nil {
		t.Fatalf("cannot build worker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { finished <- worker.Run(ctx) }()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("worker returned %v on shutdown", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("worker ignored its cancelled context")
	}
}

func hasCall(calls []string, want string) bool {
	return indexOf(calls, want) >= 0
}

func indexOf(calls []string, want string) int {
	for i, call := range calls {
		if call == want {
			return i
		}
	}
	return -1
}

func containsSubstring(calls []string, want string) bool {
	for _, call := range calls {
		if len(call) >= len(want) {
			for i := 0; i+len(want) <= len(call); i++ {
				if call[i:i+len(want)] == want {
					return true
				}
			}
		}
	}
	return false
}
