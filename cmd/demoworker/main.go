// Command demoworker runs the production stages with stub handlers, so the
// orchestrator can be driven end to end before any model, voice or render
// tooling exists. It is a harness for exercising the pipeline, not an agent:
// every handler simply records a placeholder asset and moves the episode on.
//
//	go run ./cmd/demoworker -email agent@example.com -password agent123
//
// Human gates are left alone. The demo will carry an episode as far as
// SCRIPT_DRAFT and stop, because approving is a person's job — approve it and
// the workers pick the episode up again on their own.
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
	"strings"
	"syscall"
	"time"

	"gitlhub.com/sarutt/apifiber/worker"
)

// stages maps each stage a demo worker serves to the stage it moves work to.
// The two gates are absent on purpose: no automated worker may cross them.
var stages = []struct {
	from string
	to   string
	kind string
}{
	{"IDEA_BACKLOG", "SCRIPT_DRAFT", "SCRIPT"},
	{"SCRIPT_APPROVED", "VO_GENERATED", "VOICEOVER"},
	{"VO_GENERATED", "MUSIC_GENERATED", "MUSIC"},
	{"MUSIC_GENERATED", "ANIMATION_RENDERED", "ANIMATION"},
	{"ANIMATION_RENDERED", "ASSEMBLED", "MASTER_VIDEO"},
	{"ASSEMBLED", "QA_REVIEW", ""},
	{"PLATFORM_ADAPTED", "SCHEDULED", "SHORTS_CUT"},
	{"SCHEDULED", "PUBLISHED", ""},
	{"PUBLISHED", "ANALYZED", ""},
}

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "pipeline base URL")
	email := flag.String("email", "agent@example.com", "account to log in as")
	password := flag.String("password", "agent123", "account password")
	poll := flag.Duration("poll", 2*time.Second, "how often to look for work")
	skip := flag.String("skip", "", "comma-separated stages to leave to a real agent, e.g. IDEA_BACKLOG")
	flag.Parse()

	// A stage served by a real agent must not also be served by a stub, or the
	// two race for the same episodes and whichever claims first decides
	// whether the work is real.
	skipped := map[string]bool{}
	for _, stage := range strings.Split(*skip, ",") {
		if stage = strings.TrimSpace(stage); stage != "" {
			skipped[strings.ToUpper(stage)] = true
		}
	}

	token, err := login(*baseURL, *email, *password)
	if err != nil {
		log.Fatalf("cannot log in: %v", err)
	}

	// Stop on the first interrupt so workers finish the job in hand rather
	// than abandoning a claim.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	started := 0
	for _, stage := range stages {
		stage := stage
		if skipped[stage.from] {
			log.Printf("leaving %s to a real agent", stage.from)
			continue
		}
		agent, err := worker.New(worker.Config{
			BaseURL:      *baseURL,
			Token:        token,
			WorkerID:     fmt.Sprintf("demo-%s", strings.ToLower(stage.from)),
			Stage:        stage.from,
			PollInterval: *poll,
			// Short enough to see the heartbeat working during a demo.
			HeartbeatInterval: 30 * time.Second,
		}, demoHandler(stage.to, stage.kind))
		if err != nil {
			log.Fatalf("cannot start the %s worker: %v", stage.from, err)
		}
		go agent.Run(ctx)
		started++
	}

	log.Printf("%d demo workers running; the two human gates are left for a person", started)
	log.Print("press ctrl-c to stop")
	<-ctx.Done()
	log.Print("stopping")
}

// demoHandler stands in for real work: it records a placeholder asset, if the
// stage produces one, and advances the episode.
func demoHandler(nextStatus, assetKind string) worker.Handler {
	return func(ctx context.Context, episode worker.Episode) (worker.Result, error) {
		// Stand-in for the seconds or minutes real generation would take.
		select {
		case <-ctx.Done():
			return worker.Result{}, ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}

		result := worker.Result{
			NextStatus: nextStatus,
			Note:       "produced by the demo worker",
		}
		if assetKind != "" {
			result.Assets = []worker.Asset{{
				Kind: assetKind,
				URI:  fmt.Sprintf("demo://%s/%s", episode.Code, strings.ToLower(assetKind)),
			}}
		}
		return result, nil
	}
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
