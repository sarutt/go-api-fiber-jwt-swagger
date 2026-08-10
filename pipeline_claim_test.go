package main

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
)

// The reason this feature exists: two workers polling one stage must never
// come away with the same episode.
func TestTwoWorkersNeverGetTheSameEpisode(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	first := newEpisode(t, app, token)
	second := newEpisode(t, app, token)

	claimedA := claimAt(t, app, token, StatusIdeaBacklog, "worker-a")
	claimedB := claimAt(t, app, token, StatusIdeaBacklog, "worker-b")

	if claimedA == nil || claimedB == nil {
		t.Fatal("a worker came away empty handed while work was available")
	}
	if claimedA.ID == claimedB.ID {
		t.Fatalf("both workers claimed episode %d", claimedA.ID)
	}
	if claimedA.ID != first.ID || claimedB.ID != second.ID {
		t.Errorf("claims went out of order: got %d then %d, want %d then %d",
			claimedA.ID, claimedB.ID, first.ID, second.ID)
	}
	if claimedA.ClaimedBy != "worker-a" {
		t.Errorf("episode claimed by %q, want worker-a", claimedA.ClaimedBy)
	}
}

// The same guarantee driven concurrently rather than through sequential
// calls. Note what this does and does not prove: SQLite serialises these
// claims through the single pooled connection, so this covers the queue
// draining exactly once under concurrent callers, not the interleaved
// read-then-write race. That race only becomes reachable on a backend that
// runs claims in parallel, which is why the UPDATE in claimNextEpisode
// re-checks the claim rather than trusting the preceding read.
func TestConcurrentClaimsHandOutEachEpisodeOnce(t *testing.T) {
	app := newTestAppAt(t, t.TempDir()+"/claims.db")
	token := tokenFor(t, "agent@example.com", RoleAgent)

	const episodes = 12
	const workers = 8
	for i := 0; i < episodes; i++ {
		newEpisode(t, app, token)
	}

	var mu sync.Mutex
	claims := map[uint]string{}
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			workerID := "worker-" + itoa(uint(worker))
			for {
				claimed := claimAt(t, app, token, StatusIdeaBacklog, workerID)
				if claimed == nil {
					return
				}
				mu.Lock()
				if previous, taken := claims[claimed.ID]; taken {
					t.Errorf("episode %d claimed by both %s and %s", claimed.ID, previous, workerID)
				}
				claims[claimed.ID] = workerID
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	if len(claims) != episodes {
		t.Errorf("handed out %d episodes, want %d", len(claims), episodes)
	}
}

func TestClaimReturnsNoContentWhenTheStageIsEmpty(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	res := call(t, app, http.MethodPost, "/pipeline/queue/claim", token, ClaimRequest{
		Status:   StatusAssembled,
		WorkerID: "qa-worker",
	})
	if res.StatusCode != http.StatusNoContent {
		t.Errorf("claim on an empty stage returned %d, want 204", res.StatusCode)
	}
}

func TestClaimRejectsAnUnknownStage(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	res := call(t, app, http.MethodPost, "/pipeline/queue/claim", token, map[string]any{
		"status":    "NOT_A_STAGE",
		"worker_id": "worker-a",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Errorf("claim on an unknown stage returned %d, want 400", res.StatusCode)
	}
}

// A claim is only useful if it actually keeps other workers out.
func TestOnlyTheClaimHolderCanTransition(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, token)
	claimAt(t, app, token, StatusIdeaBacklog, "worker-a")

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), token, TransitionRequest{
		ToStatus: StatusScriptDraft,
		WorkerID: "worker-b",
	})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("a non-holder transition returned %d, want 409", res.StatusCode)
	}

	res = call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), token, TransitionRequest{
		ToStatus: StatusScriptDraft,
		WorkerID: "worker-a",
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the holder's transition returned %d, want 200", res.StatusCode)
	}
}

// The next stage belongs to a different worker, so finishing a stage has to
// hand the episode back.
func TestTransitionClearsTheClaim(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, token)
	claimAt(t, app, token, StatusIdeaBacklog, "worker-a")

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), token, TransitionRequest{
		ToStatus: StatusScriptDraft,
		WorkerID: "worker-a",
	})
	var moved Episode
	decode(t, res, &moved)

	if moved.ClaimedBy != "" || moved.ClaimedAt != nil {
		t.Errorf("claim survived the transition: %q at %v", moved.ClaimedBy, moved.ClaimedAt)
	}

	// And the episode is immediately available to the next stage's worker.
	next := claimAt(t, app, token, StatusScriptDraft, "worker-b")
	if next == nil || next.ID != episode.ID {
		t.Error("episode was not available to the next stage after transitioning")
	}
}

func TestReleaseReturnsWorkToTheQueue(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, token)
	claimAt(t, app, token, StatusIdeaBacklog, "worker-a")

	// Another worker cannot release someone else's claim.
	res := call(t, app, http.MethodPost, episodePath(episode.ID, "release"), token, WorkerRequest{WorkerID: "worker-b"})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("releasing another worker's claim returned %d, want 409", res.StatusCode)
	}

	res = call(t, app, http.MethodPost, episodePath(episode.ID, "release"), token, WorkerRequest{WorkerID: "worker-a"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("release returned %d, want 200", res.StatusCode)
	}

	reclaimed := claimAt(t, app, token, StatusIdeaBacklog, "worker-b")
	if reclaimed == nil || reclaimed.ID != episode.ID {
		t.Error("released episode did not return to the queue")
	}
}

// A worker that dies mid-job must not strand the episode forever.
func TestAnExpiredLeaseIsReclaimable(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, token)
	claimAt(t, app, token, StatusIdeaBacklog, "dead-worker")

	// Simulate the lease lapsing while the holder is gone.
	expired := time.Now().Add(-claimLease() - time.Minute)
	if err := db.Model(&Episode{}).Where("id = ?", episode.ID).
		Update("claimed_at", expired).Error; err != nil {
		t.Fatalf("cannot age the claim: %v", err)
	}

	reclaimed := claimAt(t, app, token, StatusIdeaBacklog, "fresh-worker")
	if reclaimed == nil {
		t.Fatal("an expired claim was not reclaimable")
	}
	if reclaimed.ClaimedBy != "fresh-worker" {
		t.Errorf("episode reclaimed by %q, want fresh-worker", reclaimed.ClaimedBy)
	}

	// The dead worker must not be able to carry on as though it still held it.
	res := call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), token, TransitionRequest{
		ToStatus: StatusScriptDraft,
		WorkerID: "dead-worker",
	})
	if res.StatusCode != http.StatusConflict {
		t.Errorf("the evicted worker still transitioned the episode: got %d, want 409", res.StatusCode)
	}
}

func TestHeartbeatExtendsALiveClaim(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, token)
	claimed := claimAt(t, app, token, StatusIdeaBacklog, "slow-worker")

	// Age the claim to most of the lease, as a long render would.
	aged := time.Now().Add(-claimLease() + 2*time.Minute)
	if err := db.Model(&Episode{}).Where("id = ?", episode.ID).
		Update("claimed_at", aged).Error; err != nil {
		t.Fatalf("cannot age the claim: %v", err)
	}

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "heartbeat"), token, WorkerRequest{WorkerID: "slow-worker"})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("heartbeat returned %d, want 200", res.StatusCode)
	}

	var extended Episode
	decode(t, res, &extended)
	if !extended.ClaimedAt.After(*claimed.ClaimedAt) {
		t.Error("heartbeat did not push the lease out")
	}
}

func TestHeartbeatRefusesAnExpiredClaim(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, token)
	claimAt(t, app, token, StatusIdeaBacklog, "slow-worker")

	expired := time.Now().Add(-claimLease() - time.Minute)
	if err := db.Model(&Episode{}).Where("id = ?", episode.ID).
		Update("claimed_at", expired).Error; err != nil {
		t.Fatalf("cannot age the claim: %v", err)
	}

	res := call(t, app, http.MethodPost, episodePath(episode.ID, "heartbeat"), token, WorkerRequest{WorkerID: "slow-worker"})
	if res.StatusCode != http.StatusConflict {
		t.Errorf("heartbeat on an expired claim returned %d, want 409", res.StatusCode)
	}
}

// An unclaimed episode stays movable, so a single worker setup needs no
// claiming ceremony at all.
func TestUnclaimedEpisodesCanStillBeTransitioned(t *testing.T) {
	app := newTestApp(t)
	token := tokenFor(t, "agent@example.com", RoleAgent)

	episode := newEpisode(t, app, token)
	res := call(t, app, http.MethodPost, episodePath(episode.ID, "transition"), token, TransitionRequest{
		ToStatus: StatusScriptDraft,
	})
	if res.StatusCode != http.StatusOK {
		t.Errorf("transitioning an unclaimed episode returned %d, want 200", res.StatusCode)
	}
}

// claimAt takes one episode at a stage, or returns nil when none is free.
func claimAt(t *testing.T, app *fiber.App, token string, status EpisodeStatus, workerID string) *Episode {
	t.Helper()

	res := call(t, app, http.MethodPost, "/pipeline/queue/claim", token, ClaimRequest{
		Status:   status,
		WorkerID: workerID,
	})
	if res.StatusCode == http.StatusNoContent {
		return nil
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("claim at %s returned %d, want 200 or 204", status, res.StatusCode)
	}

	var episode Episode
	decode(t, res, &episode)
	return &episode
}
