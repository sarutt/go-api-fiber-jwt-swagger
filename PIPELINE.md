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

| Variable     | Default             | Purpose                        |
| ------------ | ------------------- | ------------------------------ |
| `SECRET_KEY` | — (required, `.env`) | JWT signing key                |
| `DB_PATH`    | `pompomhollow.db`   | Pipeline database file         |

Swagger UI: <http://localhost:8080/swagger/index.html>
Regenerate docs after changing annotations: `swag init`

Run the tests with `go test ./...`. CI runs build, vet, gofmt and tests on
every pull request.

## Roles

All `/pipeline/*` routes require a JWT from `POST /login`, which carries the
account's role.

| Role       | Can                                                            |
| ---------- | -------------------------------------------------------------- |
| `agent`    | Move episodes between stages, register assets, read everything  |
| `reviewer` | The above, plus record decisions at the two human gates         |

`POST /pipeline/episodes/{id}/approvals` requires the `reviewer` role, so an
agent cannot approve its own work. The decision is attributed to the identity
in the token — there is no `reviewer` field in the request body, so a decision
cannot be recorded against someone who did not make it.

The account list is still in `auth.go` rather than a database, carried over
from the boilerplate. Replace it with real credential storage, and give each
agent its own identity, before running this anywhere real.

| Account             | Password      | Role       |
| ------------------- | ------------- | ---------- |
| `user@example.com`  | `password123` | `reviewer` |
| `agent@example.com` | `agent123`    | `agent`    |

## The state machine

```
IDEA_BACKLOG → SCRIPT_DRAFT → [GATE 1] → SCRIPT_APPROVED → VO_GENERATED
  → MUSIC_GENERATED → ANIMATION_RENDERED → ASSEMBLED → QA_REVIEW → [GATE 2]
  → PLATFORM_ADAPTED → SCHEDULED → PUBLISHED → ANALYZED
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

## Endpoints

**Pipeline**

| Method | Path                 | Purpose                                        |
| ------ | -------------------- | ---------------------------------------------- |
| GET    | `/pipeline/stages`   | Describes every stage, its owner agent and gate |
| GET    | `/pipeline/queue`    | `?status=` — an agent's work queue              |

**Episodes**

| Method | Path                                     | Purpose                      |
| ------ | ---------------------------------------- | ---------------------------- |
| GET    | `/pipeline/episodes`                     | List, filter with `?status=` |
| POST   | `/pipeline/episodes`                     | Create at `IDEA_BACKLOG`     |
| GET    | `/pipeline/episodes/{id}`                | Read one                     |
| PUT    | `/pipeline/episodes/{id}`                | Update content, not status   |
| DELETE | `/pipeline/episodes/{id}`                | Delete                       |
| POST   | `/pipeline/episodes/{id}/transition`     | Move to the next stage       |
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
- **Work claiming.** `GET /pipeline/queue` returns everything at a stage and
  nothing marks work as taken, so two workers polling the same stage will both
  pick up the same episode and generate it twice. Run one worker per stage until
  this lands.
- Real credential storage, and one identity per agent rather than a shared
  account (see Roles above)
- Pagination on the list endpoints
