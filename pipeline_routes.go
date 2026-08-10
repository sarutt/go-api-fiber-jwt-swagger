package main

import "github.com/gofiber/fiber/v2"

// registerPipelineRoutes mounts the production pipeline API. It must be
// called after the JWT middleware so every route below inherits auth.
func registerPipelineRoutes(app *fiber.App) {
	pipeline := app.Group("/pipeline")

	// Discovery and work queues, polled by the production agents.
	pipeline.Get("/stages", getPipelineStages)
	pipeline.Get("/queue", getPipelineQueue)

	// Episodes and their movement through the state machine.
	pipeline.Get("/episodes", getEpisodes)
	pipeline.Post("/episodes", createEpisode)
	pipeline.Get("/episodes/:id", getEpisode)
	pipeline.Put("/episodes/:id", updateEpisode)
	pipeline.Delete("/episodes/:id", deleteEpisode)
	pipeline.Post("/episodes/:id/transition", transitionEpisode)
	pipeline.Get("/episodes/:id/events", getEpisodeEvents)

	// Human review gates.
	pipeline.Get("/reviews/pending", getPendingReviews)
	pipeline.Get("/episodes/:id/approvals", getEpisodeApprovals)
	pipeline.Post("/episodes/:id/approvals", createApproval)

	// Files produced by the generation agents.
	pipeline.Get("/episodes/:id/assets", getEpisodeAssets)
	pipeline.Post("/episodes/:id/assets", createAsset)
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
