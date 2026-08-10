package main

import "github.com/gofiber/fiber/v2"

// registerPipelineRoutes mounts the production pipeline API. It must be
// called after the JWT middleware so every route below inherits auth.
func registerPipelineRoutes(app *fiber.App) {
	pipeline := app.Group("/pipeline")

	// Discovery and work queues, polled by the production agents. The GET is
	// a read-only view; claiming is what actually hands work to one worker.
	pipeline.Get("/stages", getPipelineStages)
	pipeline.Get("/queue", getPipelineQueue)
	pipeline.Post("/queue/claim", claimWork)

	// Episodes and their movement through the state machine.
	pipeline.Get("/episodes", getEpisodes)
	pipeline.Post("/episodes", createEpisode)
	pipeline.Get("/episodes/:id", getEpisode)
	pipeline.Put("/episodes/:id", updateEpisode)
	pipeline.Delete("/episodes/:id", deleteEpisode)
	pipeline.Post("/episodes/:id/transition", transitionEpisode)
	pipeline.Post("/episodes/:id/release", releaseClaim)
	pipeline.Post("/episodes/:id/heartbeat", heartbeatClaim)
	pipeline.Post("/episodes/:id/fail", failEpisode)
	pipeline.Get("/episodes/:id/events", getEpisodeEvents)

	// Operator controls. Pausing production, and rescuing a failed episode,
	// are deliberately admin-only: they change what the whole system does.
	pipeline.Get("/overview", getOverview)
	pipeline.Get("/alerts", getAlerts)
	pipeline.Post("/alerts/:id/ack", acknowledgeAlert)
	pipeline.Get("/control", getControl)
	pipeline.Post("/control/pause", requireRole(RoleAdmin), pausePipeline)
	pipeline.Post("/control/resume", requireRole(RoleAdmin), resumePipeline)
	pipeline.Post("/episodes/:id/retry", requireRole(RoleAdmin), retryEpisode)

	// Human review gates. Recording a decision is restricted to the reviewer
	// role: this is what stops an agent approving its own work, since the
	// state machine alone cannot tell a human caller from a robot one.
	pipeline.Get("/reviews/pending", getPendingReviews)
	pipeline.Get("/episodes/:id/approvals", getEpisodeApprovals)
	pipeline.Post("/episodes/:id/approvals", requireRole(RoleReviewer), createApproval)

	// Files produced by the generation agents.
	pipeline.Get("/episodes/:id/assets", getEpisodeAssets)
	pipeline.Post("/episodes/:id/assets", createAsset)
	pipeline.Post("/episodes/:id/assets/upload", uploadAsset)
	pipeline.Get("/assets/:id/content", getAssetContent)
	pipeline.Delete("/assets/:id", deleteAsset)

	// Show bible: cast, sets and the curriculum backlog.
	pipeline.Get("/characters", getCharacters)
	pipeline.Post("/characters", createCharacter)
	pipeline.Get("/characters/:id", getCharacter)
	pipeline.Put("/characters/:id", updateCharacter)

	pipeline.Get("/locations", getLocations)
	pipeline.Post("/locations", createLocation)

	// The literal /next path is registered before the :id routes so it is not
	// captured as an id parameter.
	pipeline.Get("/curriculum-topics/next", getNextCurriculumTopic)
	pipeline.Get("/curriculum-topics", getCurriculumTopics)
	pipeline.Post("/curriculum-topics", createCurriculumTopic)
	pipeline.Post("/curriculum-topics/:id/used", markCurriculumTopicUsed)
}
