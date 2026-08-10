# Pom Pom Hollow — Production Pipeline API

Orchestration backend for the automated short-form video pipeline. Episodes move
through a fixed sequence of stages; each production agent polls for work at its
own stage and hands the episode on. Two stages are human review gates that no
agent can cross.

Built on the existing Fiber + JWT + Swagger app. The original `/books`,
`/upload` and `/config` endpoints are untouched.

## Running

```bash
go run .
```

The pipeline uses SQLite (pure Go, no cgo, no external service). The database
file is created and seeded with the show bible on first run.

| Variable               | Default              | Purpose                       |
| ---------------------- | -------------------- | ----------------------------- |
| `SECRET_KEY`           | — (required, `.env`) | JWT signing key               |
| `DB_PATH`              | `pompomhollow.db`    | Pipeline database file        |
| `CLAIM_LEASE_MINUTES`  | `15`                 | How long a worker holds a job |

Swagger UI: <http://localhost:8080/swagger/index.html>
Regenerate docs after changing annotations: `swag init`

Run the tests with `go test ./...`. CI runs build, vet, gofmt and tests on
every pull request.

## Roles

All `/pipeline/*` routes require a JWT from `POST /login`, which carries the
account's role.

| Role       | Can                                                             |
| ---------- | --------------------------------------------------------------- |
| `agent`    | Move episodes between stages, register assets, read everything   |
| `reviewer` | The above, plus record decisions at the two human gates          |
| `admin`    | The above, plus pause production and rescue failed episodes      |

A higher role satisfies a lower requirement, so one admin account runs the
whole operation without a second login. This does not weaken the gates: they
exist to stop an agent approving its own output, and every role above `agent`
is a person.

`POST /pipeline/episodes/{id}/approvals` requires the `reviewer` role, so an
agent cannot approve its own work. The decision is attributed to the identity
in the token — there is no `reviewer` field in the request body, so a decision
cannot be recorded against someone who did not make it.

The account list is still in `auth.go` rather than a database, carried over
from the boilerplate. Replace it with real credential storage, and give each
agent its own identity, before running this anywhere real.

| Account             | Password      | Role       |
| ------------------- | ------------- | ---------- |
| `admin@example.com` | `admin123`    | `admin`    |
| `user@example.com`  | `password123` | `reviewer` |
| `agent@example.com` | `agent123`    | `agent`    |

## Running it as one person

The system is built to be operated by a single admin. Open
<http://localhost:8080/admin> and sign in.

The console shows what needs a person first, then the numbers, then what has
broken. From it the operator can stop and start production, work both review
gates, and retry a failed episode. It polls every five seconds, so a board
left open stays current. The page is public because it is where people sign
in; every call it makes afterwards carries the token and is checked like any
other client's.

Everything it does is available on the API directly:

**`GET /pipeline/overview`** is the whole board on one screen: whether
production is running, how many episodes sit at each stage, what is waiting on
a human, what has failed, and what has gone quiet — anything untouched for 24
hours is surfaced, because one operator will not notice it otherwise.

**`POST /pipeline/control/pause`** is the stop switch.

```bash
# Stop everything
curl -X POST localhost:8080/pipeline/control/pause -H "$ADMIN" -H 'Content-Type: application/json' \
  -d '{"scope":"ALL","reason":"checking a policy question"}'

# Or hold just one step — uploads paused, production carries on
curl -X POST localhost:8080/pipeline/control/pause -H "$ADMIN" -H 'Content-Type: application/json' \
  -d '{"scope":"SCHEDULED","reason":"holding uploads over the weekend"}'

curl -X POST localhost:8080/pipeline/control/resume -H "$ADMIN" -H 'Content-Type: application/json' \
  -d '{"scope":"ALL"}'
```

A pause **drains** rather than halts: it is enforced at the moment work is
taken, so claimed jobs finish and nothing new starts. Workers get `423` with
the reason, and pick up again on their next poll after a resume — nothing
needs restarting.

### When a job keeps failing

A worker that gives up posts to `/pipeline/episodes/{id}/fail`. The stage is
retried up to three times; after that the episode is parked in `FAILED` with
the last error and the stage it broke at, instead of being retried forever and
spending generation budget on every attempt.

`FAILED` is terminal in the state machine. The only way out is
`POST /pipeline/episodes/{id}/retry`, which is admin-only and puts the episode
back at the stage it failed at with the attempt count cleared — so a fix to the
underlying problem can be tried without recreating the episode.

## Writing an agent

Agents are built on the `worker` package rather than reimplementing the loop.
Supply a handler that does one stage's actual work; the runtime takes exactly
one job at a time, keeps the claim alive while a long job runs, records the
assets, advances the episode, and reports failures so a job that cannot
succeed stops being retried.

```go
agent, err := worker.New(worker.Config{
    BaseURL:  "http://localhost:8080",
    Token:    token,
    WorkerID: "voiceover-agent-7", // unique per process
    Stage:    "SCRIPT_APPROVED",
}, func(ctx context.Context, episode worker.Episode) (worker.Result, error) {
    uri, err := renderVoiceOver(ctx, episode.ScriptText)
    if err != nil {
        return worker.Result{}, err // reported as a failed attempt
    }
    return worker.Result{
        NextStatus: "VO_GENERATED",
        Assets:     []worker.Asset{{Kind: "VOICEOVER", URI: uri}},
    }, nil
})
agent.Run(ctx)
```

The runtime talks HTTP and does not import the server's types, so an agent can
live in its own repository. Cancelling the context stops a worker between
jobs, so a shutdown never abandons a claim.

`cmd/demoworker` runs every automated stage with stub handlers, which is how
the pipeline can be exercised end to end before any generation tooling exists:

```bash
go run .                  # the pipeline
go run ./cmd/demoworker   # nine stub workers
```

Create an episode and the workers carry it to the first gate and stop, because
approving is a person's job. Approve it and they pick it up again on their own.

## The script agent

`cmd/scriptagent` is the first real agent. It serves `IDEA_BACKLOG`: takes the
next curriculum topic, writes a script and song with Claude, and hands the
episode to Gate 1.

```bash
export ANTHROPIC_API_KEY=sk-ant-...
go run ./cmd/scriptagent

# and stop the stub from competing for the same stage
go run ./cmd/demoworker -skip IDEA_BACKLOG
```

Two things about it are deliberate:

**The prompt is built from the show bible, not from a constant.** The cast and
sets are read from `/pipeline/characters` and `/pipeline/locations` on every
run, so editing a character in the bible changes the next script with no code
change, and the prompt cannot quietly fall out of step with the show. Inactive
characters are left out.

**The draft is validated before it is saved.** A script naming a character or
set that is not in the bible, missing one of the six beats, repeating a beat,
or arriving without a song is rejected as a failed attempt — so it is retried
and eventually parked in `FAILED`, rather than becoming a reviewer's problem.
The model is also constrained by a JSON schema, so there is no malformed
output to repair.

The curriculum topic is retired only after the script is saved: a failure
part-way through leaves the topic available for the retry rather than burning
it.

| Flag | Default | Purpose |
| ---- | ------- | ------- |
| `-model` | `claude-opus-5` | Model to write with |
| `-effort` | `high` | `low`, `medium`, `high`, `xhigh` or `max` |
| `-worker-id` | `script-agent-1` | Must be unique per process |

## The state machine

```
IDEA_BACKLOG → SCRIPT_DRAFT → [GATE 1] → SCRIPT_APPROVED → VO_GENERATED
  → MUSIC_GENERATED → ANIMATION_RENDERED → ASSEMBLED → QA_REVIEW → [GATE 2]
  → PLATFORM_ADAPTED → SCHEDULED → PUBLISHED → ANALYZED

any active stage → FAILED (after 3 attempts) → retry → back to that stage
any active stage → CANCELLED
```

Rules enforced by the API, not by convention:

- An episode always enters at `IDEA_BACKLOG`. A `status` in a create request is
  ignored, so nothing can be created part-way past a gate.
- `PUT /pipeline/episodes/{id}` cannot change status. Stage changes only happen
  through the transition endpoint.
- Only the moves listed in `episodeTransitions` (`pipeline_state.go`) are legal.
  Anything else returns `409 Conflict` explaining the refusal.
- The two gate edges are refused on the transition endpoint entirely. They
  require a decision posted to the approvals endpoint by a `reviewer`.
- A gate rejection sends the episode back for rework and is recorded either way.
- Every stage change is written to the episode event log, attributed to the
  authenticated caller.

These rules are covered by tests in `pipeline_state_test.go` and
`pipeline_api_test.go`.

`CANCELLED` is reachable from any active stage. `ANALYZED` and `CANCELLED` are
terminal.

## Work claiming

`GET /pipeline/queue` only looks. To take work, a worker posts to
`/pipeline/queue/claim` with its stage and a `worker_id`, and gets back exactly
one episode — two workers polling the same stage never receive the same job.

A claim is a lease, not a lock:

- It expires after `CLAIM_LEASE_MINUTES`, so a worker that dies mid-job does
  not strand the episode. Another worker picks it up once the lease lapses.
- While a claim is live, only the holder may transition that episode. Everyone
  else gets `409`.
- Finishing a stage clears the claim, because the next stage belongs to a
  different worker.
- A worker that cannot finish should `POST .../release` rather than wait out
  the lease.
- Work that runs longer than the lease — an animation render — should
  `POST .../heartbeat` to push it out. An expired claim cannot be extended;
  the worker must claim the episode again.

Episodes that are not claimed can still be transitioned by anyone, so a
single-worker setup needs no claiming ceremony.

```bash
# Take the next voice-over job
curl -X POST localhost:8080/pipeline/queue/claim -H "$AGENT" -H 'Content-Type: application/json' \
  -d '{"status":"SCRIPT_APPROVED","worker_id":"voiceover-agent-7"}'

# Hand it on when done, quoting the same worker_id
curl -X POST localhost:8080/pipeline/episodes/1/transition -H "$AGENT" -H 'Content-Type: application/json' \
  -d '{"to_status":"VO_GENERATED","worker_id":"voiceover-agent-7"}'
```

Note for scaling: SQLite takes one writer at a time, so the connection pool is
capped at a single connection and concurrent workers queue behind each other.
That is what serialises claims today. Moving to Postgres lifts the cap — the
conditional update in `claimNextEpisode` is what keeps claiming correct once it
does, and should not be simplified away.

## Endpoints

**Pipeline**

| Method | Path                     | Purpose                                        |
| ------ | ------------------------ | ---------------------------------------------- |
| GET    | `/pipeline/stages`       | Describes every stage, its owner agent and gate |
| GET    | `/pipeline/queue`        | `?status=` — read-only view of a stage's queue  |
| POST   | `/pipeline/queue/claim`  | Take the next job at a stage                    |
| GET    | `/pipeline/overview`     | The whole operation on one screen                |
| GET    | `/pipeline/control`      | What is currently paused                         |
| POST   | `/pipeline/control/pause`  | Stop production (admin)                        |
| POST   | `/pipeline/control/resume` | Start it again (admin)                         |

**Episodes**

| Method | Path                                     | Purpose                      |
| ------ | ---------------------------------------- | ---------------------------- |
| GET    | `/pipeline/episodes`                     | List, filter with `?status=` |
| POST   | `/pipeline/episodes`                     | Create at `IDEA_BACKLOG`     |
| GET    | `/pipeline/episodes/{id}`                | Read one                     |
| PUT    | `/pipeline/episodes/{id}`                | Update content, not status   |
| DELETE | `/pipeline/episodes/{id}`                | Delete                       |
| POST   | `/pipeline/episodes/{id}/transition`     | Move to the next stage       |
| POST   | `/pipeline/episodes/{id}/release`        | Give a claimed episode back  |
| POST   | `/pipeline/episodes/{id}/heartbeat`      | Extend a claim on a long job |
| POST   | `/pipeline/episodes/{id}/fail`           | Report a job that could not finish |
| POST   | `/pipeline/episodes/{id}/retry`          | Put a failed episode back to work (admin) |
| GET    | `/pipeline/episodes/{id}/events`         | Stage history                |

**Human review gates**

| Method | Path                                   | Purpose                        |
| ------ | -------------------------------------- | ------------------------------ |
| GET    | `/pipeline/reviews/pending`            | Everything waiting on a human  |
| GET    | `/pipeline/episodes/{id}/approvals`    | Gate decisions for an episode  |
| POST   | `/pipeline/episodes/{id}/approvals`    | Record a decision at a gate    |

**Assets**

| Method | Path                              | Purpose                  |
| ------ | --------------------------------- | ------------------------ |
| GET    | `/pipeline/episodes/{id}/assets`  | Files for an episode     |
| POST   | `/pipeline/episodes/{id}/assets`  | Register a produced file |
| DELETE | `/pipeline/assets/{id}`           | Delete an asset          |

**Show bible**

| Method | Path                                       | Purpose                        |
| ------ | ------------------------------------------ | ------------------------------ |
| GET    | `/pipeline/characters`                     | The cast                       |
| POST   | `/pipeline/characters`                     | Add a character                |
| GET    | `/pipeline/characters/{id}`                | Read one                       |
| PUT    | `/pipeline/characters/{id}`                | Update one                     |
| GET    | `/pipeline/locations`                      | Recurring sets                 |
| POST   | `/pipeline/locations`                      | Add a set                      |
| GET    | `/pipeline/curriculum-topics`              | Backlog, filter by theme/used  |
| GET    | `/pipeline/curriculum-topics/next`         | Next topic due in the rotation |
| POST   | `/pipeline/curriculum-topics`              | Add a topic                    |
| POST   | `/pipeline/curriculum-topics/{id}/used`    | Mark a topic used              |

## Example: one episode end to end

```bash
login() {
  curl -s -X POST localhost:8080/login -H 'Content-Type: application/json' \
    -d "{\"email\":\"$1\",\"password\":\"$2\"}" | jq -r .token
}
AGENT="Authorization: Bearer $(login agent@example.com agent123)"
REVIEWER="Authorization: Bearer $(login user@example.com password123)"

# Create — always lands in IDEA_BACKLOG
curl -X POST localhost:8080/pipeline/episodes -H "$AGENT" -H 'Content-Type: application/json' \
  -d '{"title":"Counting Flowers in Giggle Meadow","curriculum_topic_id":1}'

# Script agent picks it up
curl -X POST localhost:8080/pipeline/episodes/1/transition -H "$AGENT" -H 'Content-Type: application/json' \
  -d '{"to_status":"SCRIPT_DRAFT","actor":"script-agent"}'

# Gate 1 — the same call with the agent token is refused 403
curl -X POST localhost:8080/pipeline/episodes/1/approvals -H "$REVIEWER" -H 'Content-Type: application/json' \
  -d '{"gate":"GATE_1_SCRIPT","decision":"APPROVED","notes":"Age appropriate."}'

# Production agents run their stages
for S in VO_GENERATED MUSIC_GENERATED ANIMATION_RENDERED ASSEMBLED QA_REVIEW; do
  curl -X POST localhost:8080/pipeline/episodes/1/transition -H "$AGENT" \
    -H 'Content-Type: application/json' -d "{\"to_status\":\"$S\"}"
done

# Gate 2 — human sign-off before anything publishes
curl -X POST localhost:8080/pipeline/episodes/1/approvals -H "$REVIEWER" -H 'Content-Type: application/json' \
  -d '{"gate":"GATE_2_RELEASE","decision":"APPROVED","notes":"Made-for-Kids flag verified."}'
```

## Compliance notes

`made_for_kids` and `ai_disclosure` default to true on every episode and are set
at creation rather than at upload time. The upload agent is expected to pass
`made_for_kids` through to the YouTube Data API as `selfDeclaredMadeForKids`.
Gate 2 is the point where a human confirms this before publishing.

## Not built yet

The scaffold is the orchestrator only. Still to come, per the roadmap:

- The agents themselves — nothing calls the LLM, TTS, music or animation tools yet
- YouTube Data API and TikTok Content Posting API integration in an upload worker
- The reviewer dashboard UI (`/pipeline/reviews/pending` is the API behind it)
- Analytics ingestion feeding back into curriculum planning
- Real credential storage, and one identity per agent rather than a shared
  account (see Roles above)
- Pagination on the list endpoints
- Alerting. The overview reports stuck and failed episodes, but nothing pushes
  that to the operator — they have to look
- Editing an episode from the console — it reviews and controls, but content
  is still changed through the API
