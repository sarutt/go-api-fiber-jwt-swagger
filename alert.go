package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Alert kinds. Each names something an operator has to act on.
const (
	AlertGateWaiting = "GATE_WAITING"
	AlertFailed      = "FAILED"
	AlertStuck       = "STUCK"
)

// defaultSweepMinutes is how often the pipeline looks for episodes that have
// gone quiet. Stuck is measured in hours, so sweeping often buys nothing.
const defaultSweepMinutes = 15

// alertWebhookURL is where alerts are pushed. Unset means alerts are still
// recorded and visible on the console — the operator just has to look.
func alertWebhookURL() string { return os.Getenv("ALERT_WEBHOOK_URL") }

func sweepInterval() time.Duration {
	if raw := os.Getenv("ALERT_SWEEP_MINUTES"); raw != "" {
		if minutes, err := strconv.Atoi(raw); err == nil && minutes > 0 {
			return time.Duration(minutes) * time.Minute
		}
	}
	return defaultSweepMinutes * time.Minute
}

// raiseAlert records something the operator should know about, and pushes it
// once. DedupeKey makes this safe to call on every transition and every sweep:
// an episode parked at a gate is announced when it arrives, not repeatedly.
//
// It never returns an error. An alert is a notification about work, not part
// of the work — a failed webhook must not fail the transition that caused it.
func raiseAlert(kind string, episodeID uint, stage EpisodeStatus, message string) {
	alert := Alert{
		Kind:      kind,
		EpisodeID: episodeID,
		Stage:     stage,
		Message:   message,
		DedupeKey: fmt.Sprintf("%s:%d:%s", kind, episodeID, stage),
	}

	// Do nothing if this alert is already open. An alert that was resolved
	// earlier can be raised again, because the condition genuinely recurred.
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&alert)
	if result.Error != nil {
		log.Printf("cannot record %s alert for episode %d: %v", kind, episodeID, result.Error)
		return
	}
	if result.RowsAffected == 0 {
		return
	}

	// The connection is captured rather than read again inside the goroutine:
	// delivery outlives the call, and tests swap the global between cases.
	go deliverAlert(db, alert)
}

// resolveAlerts closes any open alert for an episode whose condition has
// passed. Without this an operator ends up reading a board of alerts that are
// no longer true, which is worse than no board at all.
func resolveAlerts(episodeID uint, kinds ...string) {
	now := time.Now()
	query := db.Model(&Alert{}).
		Where("episode_id = ? AND resolved_at IS NULL", episodeID)
	if len(kinds) > 0 {
		query = query.Where("kind IN ?", kinds)
	}
	if err := query.Update("resolved_at", now).Error; err != nil {
		log.Printf("cannot resolve alerts for episode %d: %v", episodeID, err)
	}
}

// alertOnTransition raises and resolves alerts around a stage change. It is
// called after the episode has been saved, so an alert never describes a move
// that did not happen.
func alertOnTransition(episode *Episode, from EpisodeStatus) {
	// Leaving a stage clears whatever that stage was waiting for. Leaving
	// FAILED clears the failure too — a retry is the operator saying they
	// have dealt with it. This is decided by the stage being left, never by
	// the one being entered: a retry that lands straight back on a gate has
	// still left FAILED behind, and its failure alert has to go with it.
	passed := []string{AlertGateWaiting, AlertStuck}
	if from == StatusFailed {
		passed = append(passed, AlertFailed)
	}
	resolveAlerts(episode.ID, passed...)

	switch {
	case episode.Status == StatusFailed:
		raiseAlert(AlertFailed, episode.ID, episode.FailedFrom,
			fmt.Sprintf("%s failed at %s: %s", episode.Code, episode.FailedFrom, episode.LastError))
	case gateWaitingAt(episode.Status):
		raiseAlert(AlertGateWaiting, episode.ID, episode.Status,
			fmt.Sprintf("%s is waiting for a review at %s", episode.Code, episode.Status))
	}
}

// gateWaitingAt reports whether a stage is one a human has to clear.
func gateWaitingAt(status EpisodeStatus) bool {
	return status == gateStatus(Gate1Script) || status == gateStatus(Gate2Release)
}

// deliverAlert pushes one alert to the configured webhook. The payload has a
// text field so it works with Slack and Discord unchanged, and the full alert
// alongside it for anything that wants structure.
func deliverAlert(tx *gorm.DB, alert Alert) {
	url := alertWebhookURL()
	if url == "" {
		return
	}

	payload, err := json.Marshal(map[string]any{
		"text":  fmt.Sprintf("[%s] %s", alert.Kind, alert.Message),
		"alert": alert,
	})
	if err != nil {
		log.Printf("cannot encode alert %d: %v", alert.ID, err)
		return
	}

	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		log.Printf("cannot deliver alert %d: %v", alert.ID, err)
		return
	}
	defer res.Body.Close()

	if res.StatusCode >= 300 {
		log.Printf("alert %d was refused by the webhook: %s", alert.ID, res.Status)
		return
	}
	now := time.Now()
	tx.Model(&Alert{}).Where("id = ?", alert.ID).Update("delivered_at", now)
}

// startAlertSweeper watches for episodes that have gone quiet. The other two
// alert kinds fire on a transition; this one exists precisely because nothing
// happens — no event will ever announce it.
func startAlertSweeper() {
	interval := sweepInterval()
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			sweepStuckEpisodes()
		}
	}()
	log.Printf("alert sweeper running every %s", interval)
}

// sweepStuckEpisodes raises an alert for each episode untouched for too long.
func sweepStuckEpisodes() {
	var stuck []Episode
	err := db.Where("updated_at < ?", time.Now().Add(-stuckAfter)).
		Where("status NOT IN ?", []EpisodeStatus{StatusAnalyzed, StatusCancelled, StatusFailed}).
		Find(&stuck).Error
	if err != nil {
		log.Printf("cannot sweep for stuck episodes: %v", err)
		return
	}

	for _, episode := range stuck {
		// A gate already has its own alert; saying it twice adds noise
		// without adding information.
		if gateWaitingAt(episode.Status) {
			continue
		}
		raiseAlert(AlertStuck, episode.ID, episode.Status,
			fmt.Sprintf("%s has sat at %s since %s", episode.Code, episode.Status,
				episode.UpdatedAt.Format(time.RFC3339)))
	}
}

/* ---------- endpoints ---------- */

// getAlerts godoc
// @Summary List alerts
// @Description What the pipeline has flagged for a person. Defaults to open alerts; pass ?all=true for the history.
// @Tags pipeline-control
// @Produce json
// @Security ApiKeyAuth
// @Param all query bool false "Include resolved and acknowledged alerts"
// @Success 200 {array} Alert
// @Router /pipeline/alerts [get]
func getAlerts(c *fiber.Ctx) error {
	query := db.Order("created_at desc").Limit(200)
	if c.Query("all") != "true" {
		query = query.Where("resolved_at IS NULL AND acknowledged_at IS NULL")
	}

	alerts := []Alert{}
	if err := query.Find(&alerts).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(alerts)
}

// acknowledgeAlert godoc
// @Summary Acknowledge an alert
// @Description Marks an alert as seen. Distinct from resolved, which the pipeline sets by itself once the condition passes — acknowledging says a person looked, not that the problem went away.
// @Tags pipeline-control
// @Produce json
// @Security ApiKeyAuth
// @Param id path int true "Alert ID"
// @Success 200 {object} Alert
// @Failure 404 {object} ErrorResponse
// @Router /pipeline/alerts/{id}/ack [post]
func acknowledgeAlert(c *fiber.Ctx) error {
	id, err := paramID(c)
	if err != nil {
		return badRequest(c, "id must be a number")
	}

	var alert Alert
	if err := db.First(&alert, id).Error; err != nil {
		return notFound(c, "alert not found")
	}

	now := time.Now()
	alert.AcknowledgedAt = &now
	alert.AcknowledgedBy = currentSubject(c)
	if err := db.Save(&alert).Error; err != nil {
		return serverError(c, err.Error())
	}
	return c.JSON(alert)
}

// countOpenAlerts is used by the overview.
func countOpenAlerts(tx *gorm.DB) int64 {
	var count int64
	tx.Model(&Alert{}).Where("resolved_at IS NULL AND acknowledged_at IS NULL").Count(&count)
	return count
}
