package main

import (
	"os"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// defaultLeaseMinutes is how long a worker holds an episode before the claim
// expires and another worker may take it. It is deliberately longer than a
// fast stage needs, because the alternative — a lease that expires under a
// slow render — hands the same job to a second worker. Long jobs should send
// a heartbeat rather than rely on a large value here.
const defaultLeaseMinutes = 15

// claimLease reads the lease duration from CLAIM_LEASE_MINUTES.
func claimLease() time.Duration {
	if raw := os.Getenv("CLAIM_LEASE_MINUTES"); raw != "" {
		if minutes, err := strconv.Atoi(raw); err == nil && minutes > 0 {
			return time.Duration(minutes) * time.Minute
		}
	}
	return defaultLeaseMinutes * time.Minute
}

// freeClaimScope matches episodes whose claim is unheld or has expired.
// Rows written before the claim columns existed can hold NULL, so both the
// empty string and NULL count as unclaimed.
func freeClaimScope(cutoff time.Time) func(*gorm.DB) *gorm.DB {
	return func(query *gorm.DB) *gorm.DB {
		return query.Where(
			db.Where("claimed_by = ?", "").
				Or("claimed_by IS NULL").
				Or("claimed_at IS NULL").
				Or("claimed_at < ?", cutoff),
		)
	}
}

// claimNextEpisode hands the oldest available episode at a stage to one
// worker. Returns nil when the stage has no work free to take.
//
// On SQLite the single connection in the pool is what actually serialises
// concurrent claims — the whole transaction runs start to finish before the
// next one begins, so no interleaving is possible.
//
// The conditional update is written for the database this outgrows. On a
// backend that runs claims in parallel, the read below can go stale between
// selecting a candidate and taking it, and only re-checking the claim in the
// UPDATE's own WHERE clause closes that window: a worker that loses the race
// sees RowsAffected of 0 and moves to the next candidate rather than
// overwriting a claim someone else just made. Keep it when moving to
// Postgres; it is the part that stops this becoming a read-then-write race.
func claimNextEpisode(status EpisodeStatus, workerID string) (*Episode, error) {
	now := time.Now()
	cutoff := now.Add(-claimLease())

	var claimed *Episode
	err := db.Transaction(func(tx *gorm.DB) error {
		var candidates []Episode
		if err := tx.Where("status = ?", status).
			Scopes(freeClaimScope(cutoff)).
			Order("id asc").
			Find(&candidates).Error; err != nil {
			return err
		}

		for _, candidate := range candidates {
			result := tx.Model(&Episode{}).
				Where("id = ? AND status = ?", candidate.ID, status).
				Scopes(freeClaimScope(cutoff)).
				Updates(map[string]any{"claimed_by": workerID, "claimed_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				// Another worker took it between the read and the write.
				continue
			}

			var episode Episode
			if err := tx.First(&episode, candidate.ID).Error; err != nil {
				return err
			}
			claimed = &episode
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return claimed, nil
}

// claimHolder returns the worker holding a live claim on the episode, or ""
// when the episode is free or the claim has expired.
func claimHolder(episode *Episode) string {
	if episode.ClaimedBy == "" || episode.ClaimedAt == nil {
		return ""
	}
	if episode.ClaimedAt.Before(time.Now().Add(-claimLease())) {
		return ""
	}
	return episode.ClaimedBy
}

// clearClaim frees the episode in memory. The caller saves it.
func clearClaim(episode *Episode) {
	episode.ClaimedBy = ""
	episode.ClaimedAt = nil
}
