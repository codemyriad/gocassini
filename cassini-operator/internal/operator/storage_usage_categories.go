package operator

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// These are the policy keys used by retentionSettings, plus an explicit bucket
// for files whose ownership is not established (including work in progress).
type storageUsageDay struct {
	Date  string `json:"date"`
	Bytes int64  `json:"bytes"`
	Files int    `json:"files"`
}
type storageUsageCategory struct {
	ID           string            `json:"id"`
	Bytes        int64             `json:"bytes"`
	Files        int               `json:"files"`
	UndatedBytes int64             `json:"undated_bytes"`
	UndatedFiles int               `json:"undated_files"`
	Days         []storageUsageDay `json:"days"`
}
type storagePathCategory struct{ category, date string }
type storageCategoryIndex struct {
	workRoot   string
	paths      map[string]storagePathCategory
	categories map[string]*storageUsageCategory
	days       map[string]map[string]*storageUsageDay
}

var storageCategoryIDs = []string{"recordings", "current", "failed_capture", "failed_build", "superseded", "failed_publish", "logs", "other"}

func newStorageCategoryIndex(workRoot string) *storageCategoryIndex {
	index := &storageCategoryIndex{workRoot: workRoot, paths: map[string]storagePathCategory{}, categories: map[string]*storageUsageCategory{}, days: map[string]map[string]*storageUsageDay{}}
	for _, id := range storageCategoryIDs {
		index.categories[id] = &storageUsageCategory{ID: id, Days: []storageUsageDay{}}
		index.days[id] = map[string]*storageUsageDay{}
	}
	return index
}
func (index *storageCategoryIndex) assign(category string, anchor time.Time, paths ...string) {
	date := ""
	if !anchor.IsZero() {
		date = anchor.UTC().Format("2006-01-02")
	}
	for _, path := range paths {
		index.paths[filepath.Clean(path)] = storagePathCategory{category, date}
	}
}

// Use retention's ownership and lifecycle anchors, never file modification
// times: a copied file must not appear newer than the policy considers it.
// Read only the metadata needed for classification, once per attempt.
func (rt *Runtime) storageCategoryIndex(ctx context.Context) (*storageCategoryIndex, error) {
	index := newStorageCategoryIndex(rt.cfg.WorkRoot)
	rows, err := rt.store.db.QueryContext(ctx, `SELECT j.id, j.artifact_run_path,
 a.attempt_number, a.stage, a.state, a.record_finished_at, a.publish_finished_at,
 a.completed_at, a.interrupted_at, COALESCE(v.published_attempt, 0)
 FROM jobs j JOIN job_attempts a ON a.job_id=j.id
 LEFT JOIN artifact_availability v ON v.job_id=j.id
 ORDER BY j.id, a.attempt_number DESC`)
	if err != nil {
		return index, err
	}
	defer rows.Close()
	var lastJob string
	var replacement time.Time
	sourceAssigned := false
	for rows.Next() {
		var id, stage, state string
		var source, recorded, published, completed, interrupted sql.NullString
		var attempt, current int
		if err := rows.Scan(&id, &source, &attempt, &stage, &state, &recorded, &published, &completed, &interrupted, &current); err != nil {
			return newStorageCategoryIndex(rt.cfg.WorkRoot), err
		}
		if !validArtifactJob(id) {
			continue
		}
		if id != lastJob {
			lastJob = id
			replacement = time.Time{}
			sourceAssigned = false
		}
		anchor := func(s sql.NullString) time.Time { return retentionAnchor(&s.String) }
		root := rt.cfg.WorkRoot
		// This follows expireCanonicalArchives, including the capture duplicate.
		if !sourceAssigned && source.String == canonicalRunPath(root, id) && recorded.Valid {
			index.assign("recordings", anchor(recorded), canonicalRunPath(root, id), attemptRunPath(root, id, attempt))
			sourceAssigned = true
		}
		if attempt == current && state == "succeeded" && published.Valid {
			index.assign("current", anchor(published), canonicalMeetingPath(root, id), canonicalOpusPath(root, id), attemptSealDir(root, id, attempt), attemptMeetingPath(root, id, attempt))
		}
		if stage != "done" {
			continue
		}
		ended := anchor(completed)
		if ended.IsZero() {
			ended = anchor(interrupted)
		}
		index.assign("logs", ended, attemptLogsDir(root, id, attempt))
		if state == "succeeded" && published.Valid {
			if !replacement.IsZero() && attempt != current {
				index.assign("superseded", replacement, attemptSealDir(root, id, attempt), attemptMeetingPath(root, id, attempt))
			}
			replacement = anchor(published)
			continue
		}
		if state != "failed" && state != "interrupted" {
			continue
		}
		if source.String != canonicalRunPath(root, id) {
			index.assign("failed_capture", ended, attemptRunPath(root, id, attempt))
		}
		index.assign("failed_build", ended, attemptMeetingPath(root, id, attempt), attemptSealDir(root, id, attempt))
		index.assign("failed_publish", ended, attemptSitePath(root, id, attempt))
	}
	if err := rows.Err(); err != nil {
		return newStorageCategoryIndex(rt.cfg.WorkRoot), err
	}
	return index, nil
}
func (index *storageCategoryIndex) add(path string, bytes int64) {
	// All policy-owned bundles are direct children of current/ or runs/. Match
	// that complete component, so job.run-extra cannot inherit job.run's policy.
	relative, err := filepath.Rel(index.workRoot, path)
	parts := strings.Split(relative, string(filepath.Separator))
	owner := storagePathCategory{category: "other"}
	if err == nil && len(parts) >= 2 {
		if known, ok := index.paths[filepath.Join(index.workRoot, parts[0], parts[1])]; ok {
			owner = known
		}
	}
	category := index.categories[owner.category]
	category.Bytes += bytes
	category.Files++
	if owner.date == "" {
		category.UndatedBytes += bytes
		category.UndatedFiles++
		return
	}
	day := index.days[owner.category][owner.date]
	if day == nil {
		day = &storageUsageDay{Date: owner.date}
		index.days[owner.category][owner.date] = day
	}
	day.Bytes += bytes
	day.Files++
}
func (index *storageCategoryIndex) result() []storageUsageCategory {
	result := make([]storageUsageCategory, 0, len(storageCategoryIDs))
	for _, id := range storageCategoryIDs {
		category := *index.categories[id]
		for _, day := range index.days[id] {
			category.Days = append(category.Days, *day)
		}
		sort.Slice(category.Days, func(i, j int) bool { return category.Days[i].Date < category.Days[j].Date })
		result = append(result, category)
	}
	return result
}
