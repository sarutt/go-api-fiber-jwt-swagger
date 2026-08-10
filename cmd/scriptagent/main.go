// Command scriptagent runs the script-writing agent against the pipeline.
// It claims episodes waiting in IDEA_BACKLOG, writes a script and song for
// the next curriculum topic, and hands each episode to the first review gate.
//
//	ANTHROPIC_API_KEY=... go run ./cmd/scriptagent
//
// It never crosses a gate: an episode it writes waits at SCRIPT_DRAFT until a
// person approves it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"gitlhub.com/sarutt/apifiber/agents/script"
	"gitlhub.com/sarutt/apifiber/worker"
)

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "pipeline base URL")
	email := flag.String("email", "agent@example.com", "pipeline account")
	password := flag.String("password", "agent123", "pipeline password")
	workerID := flag.String("worker-id", "script-agent-1", "unique id for this process")
	model := flag.String("model", script.DefaultModel, "model to write with")
	effort := flag.String("effort", string(anthropic.OutputConfigEffortHigh), "low, medium, high, xhigh or max")
	poll := flag.Duration("poll", 5*time.Second, "how often to look for work")
	flag.Parse()

	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		log.Print("ANTHROPIC_API_KEY is not set; falling back to the SDK's own credential resolution")
	}

	token, err := login(*baseURL, *email, *password)
	if err != nil {
		log.Fatalf("cannot log in to the pipeline: %v", err)
	}

	agent, err := script.New(script.Config{
		Pipeline: worker.NewClient(*baseURL, token, nil),
		Model:    *model,
		Effort:   anthropic.OutputConfigEffort(*effort),
	})
	if err != nil {
		log.Fatalf("cannot build the script agent: %v", err)
	}

	// Writing a script takes longer than a lease, so the runtime heartbeats
	// while the model works.
	runner, err := worker.New(worker.Config{
		BaseURL:           *baseURL,
		Token:             token,
		WorkerID:          *workerID,
		Stage:             "IDEA_BACKLOG",
		PollInterval:      *poll,
		HeartbeatInterval: 2 * time.Minute,
		HTTPClient:        &http.Client{Timeout: 15 * time.Minute},
	}, agent.Handler())
	if err != nil {
		log.Fatalf("cannot start the worker: %v", err)
	}

	// Stop on the first interrupt so the job in hand finishes rather than
	// abandoning its claim.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log.Printf("script agent %s writing with %s at %s effort", *workerID, *model, *effort)
	if err := runner.Run(ctx); err != nil {
		log.Fatalf("worker stopped: %v", err)
	}
	log.Print("stopping")
}

func login(baseURL, email, password string) (string, error) {
	body, err := json.Marshal(map[string]string{"email": email, "password": password})
	if err != nil {
		return "", err
	}

	res, err := http.Post(baseURL+"/login", "application/json", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return "", fmt.Errorf("%s: %s", res.Status, bytes.TrimSpace(payload))
	}

	var payload struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Token == "" {
		return "", fmt.Errorf("login returned no token")
	}
	return payload.Token, nil
}
