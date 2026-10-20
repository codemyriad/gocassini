package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// A browser renews this lease while its PUT is live. A closed browser leaves
// durable intent; reconciliation checks the actual file after the lease ends.
const importUploadLease = 2 * time.Minute

func (s *importService) stagingAction(w http.ResponseWriter, r *http.Request, job ImportJob, action string) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok, err := s.rt.store.ImportJob(r.Context(), job.ID)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	if !ok || job.ClearedAt != "" {
		http.NotFound(w, r)
		return
	}
	switch action {
	case "heartbeat":
		if job.State != importStateUploading {
			writeJSONError(w, 409, "upload is no longer active")
			return
		}
		_, err = s.rt.store.db.ExecContext(r.Context(), `UPDATE import_jobs SET updated_at=? WHERE id=?`, nowUTCString(), job.ID)
	case "staged":
		if job.State == importStateStaged {
			writeJSON(w, 200, job)
			return
		}
		if job.State != importStateUploading {
			writeJSONError(w, 409, "upload is no longer active")
			return
		}
		err = s.inspectUpload(r.Context(), job)
		if err == nil {
			current, _, readErr := s.rt.store.ImportJob(r.Context(), job.ID)
			if readErr != nil {
				err = readErr
			} else if current.State != importStateStaged {
				writeJSONError(w, 409, "upload is incomplete; add the file again")
				return
			}
		}
	case "upload-failed":
		if job.State == importStateUploading {
			err = s.inspectUpload(r.Context(), job)
		}
	case "remove":
		if importUsesStaging(job) || s.runningID == job.ID {
			writeJSONError(w, 409, "upload or import is still active")
			return
		}
		if _, pathErr := sanitizeStagedImportPath(job.StagedPath); pathErr != nil {
			writeJSONError(w, 409, "this job does not have a Nextcloud staging file")
			return
		}
		err = s.removeStaging(r.Context(), job)
	}
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"accepted": true})
}

func importUsesStaging(job ImportJob) bool {
	switch job.State {
	case importStateUploading, importStateQueued, importStateFetching, importStateValidating, importStateDelivering, importStateSharing, importStateIndexing:
		return true
	}
	return false
}

func (s *importService) inspectUpload(ctx context.Context, job ImportJob) error {
	leaf, err := s.exapp.davPropfindLeafState(ctx, s.client, job.CreatedBy, job.StagedPath)
	if err != nil {
		return err
	}
	if !leaf.Exists || leaf.Size != job.ExpectedBytes {
		return s.rt.store.SetImportJobState(ctx, job.ID, importStateUploadFailed, "", "Upload did not complete. Add the file again to import it.")
	}
	_, err = s.rt.store.db.ExecContext(ctx, `UPDATE import_jobs SET state=?,staging_state='present',error='',updated_at=? WHERE id=? AND state=?`, importStateStaged, nowUTCString(), job.ID, importStateUploading)
	return err
}

func (s *importService) reconcileUploads(ctx context.Context, caller string) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.rt.store.ImportJobsFor(ctx, caller)
	if err != nil {
		return
	}
	for _, job := range jobs {
		updated, err := time.Parse(time.RFC3339Nano, job.UpdatedAt)
		if job.State != importStateUploading || err != nil || time.Since(updated) < importUploadLease {
			continue
		}
		if err := s.inspectUpload(ctx, job); err != nil {
			s.logf("inspect upload %s: %v", job.ID, err)
			_ = s.rt.store.SetImportJobState(ctx, job.ID, importStateUploadFailed, "", "Upload could not be confirmed. Add the file again to import it.")
		}
	}
}

// Successful rows are permanent retention provenance. Unsuccessful rows are
// hidden only after deletion succeeds; failures remain visible and actionable.
func (s *importService) markStagingRemoved(ctx context.Context, id string) error {
	_, err := s.rt.store.db.ExecContext(ctx, `UPDATE import_jobs SET staging_state='removed',cleared_at=CASE WHEN state=? THEN '' ELSE ? END WHERE id=?`, importStateDone, nowUTCString(), id)
	return err
}

func (s *importService) removeStaging(ctx context.Context, job ImportJob) error {
	if err := s.exapp.davDeleteUserFile(ctx, s.client, job.CreatedBy, job.StagedPath); err != nil {
		return err
	}
	return s.markStagingRemoved(ctx, job.ID)
}

func nextImportStagingSweep(now time.Time) time.Time {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), 3, 3, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (s *importService) startStagingGC() {
	s.rt.workerWG.Add(1)
	go func() {
		defer s.rt.workerWG.Done()
		for {
			timer := time.NewTimer(time.Until(nextImportStagingSweep(time.Now())))
			select {
			case <-s.rt.ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				s.logf("staging GC: starting scheduled 03:03 UTC sweep")
				if err := s.sweepImportStaging(s.rt.ctx); err != nil {
					s.logf("staging GC: %v", err)
				}
			}
		}
	}()
}

// Enumerating Nextcloud users also covers orphan uploads from the old client,
// which uploaded before creating a DB row. Known owners remain sweepable even
// when Nextcloud's user-list endpoint is temporarily unavailable.
func (s *importService) stagingUsers(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	seen := map[string]bool{}
	rows, err := s.rt.store.db.QueryContext(ctx, `SELECT DISTINCT created_by FROM import_jobs`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var user string
		if err := rows.Scan(&user); err != nil {
			rows.Close()
			return nil, err
		}
		seen[user] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var listErr error
	for offset := 0; ; offset += 100 {
		status, body, err := s.exapp.apiGet(ctx, s.client, s.exapp.ocsURL(fmt.Sprintf("/cloud/users?limit=100&offset=%d", offset)))
		if err != nil || status/100 != 2 || ocsRefusal(status, body) != "" {
			listErr = fmt.Errorf("list users for staging GC: HTTP %d: %v", status, err)
			break
		}
		var env struct {
			OCS struct {
				Data struct {
					Users []string `json:"users"`
				} `json:"data"`
			} `json:"ocs"`
		}
		if err := json.Unmarshal(body, &env); err != nil {
			listErr = err
			break
		}
		for _, user := range env.OCS.Data.Users {
			seen[user] = true
		}
		if len(env.OCS.Data.Users) < 100 {
			break
		}
	}
	users := make([]string, 0, len(seen))
	for user := range seen {
		users = append(users, user)
	}
	return users, listErr
}

func (s *importService) sweepImportStaging(ctx context.Context) error {
	users, listErr := s.stagingUsers(ctx)
	for _, user := range users {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.reconcileUploads(ctx, user)
		if err := s.clearUserStaging(ctx, user); err != nil {
			s.logf("staging GC user %s: %v", user, err)
		}
	}
	s.logf("staging GC: finished sweep across %d users", len(users))
	return listErr
}

func (s *importService) clearUserStaging(ctx context.Context, user string) error {
	// Serialize against queue admission, worker claims and upload heartbeats.
	// Requests have a deadline so a failed Nextcloud cannot hold this indefinitely.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, _, err := s.exapp.davListSizes(ctx, s.client, user, importStagingDir)
	if err != nil {
		return err
	}
	rows, err := s.rt.store.db.QueryContext(ctx, `SELECT `+importJobColumns+` FROM import_jobs WHERE created_by=?`, user)
	if err != nil {
		return err
	}
	byPath := map[string][]ImportJob{}
	for rows.Next() {
		job, err := scanImportJob(rows.Scan)
		if err != nil {
			rows.Close()
			return err
		}
		byPath[job.StagedPath] = append(byPath[job.StagedPath], job)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// Include absent files: an earlier DELETE may have succeeded before the DB
	// update failed, or the user may have removed the file in Nextcloud Files.
	paths := map[string]bool{}
	for path, jobs := range byPath {
		if _, err := sanitizeStagedImportPath(path); err != nil {
			continue
		}
		for _, job := range jobs {
			if job.StagingState != "removed" {
				paths[path] = true
				break
			}
		}
	}
	for _, entry := range entries {
		if entry.collection {
			continue
		}
		if _, err := sanitizeStagedImportPath(entry.relPath); err == nil {
			paths[entry.relPath] = true
		}
	}
	var firstErr error
	for path := range paths {
		jobs := byPath[path]
		active := false
		for _, job := range jobs {
			if (job.ClearedAt == "" && importUsesStaging(job)) || s.runningID == job.ID {
				active = true
			}
		}
		if active {
			continue
		}
		if err := s.exapp.davDeleteUserFile(ctx, s.client, user, path); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		for _, job := range jobs {
			if err := s.markStagingRemoved(ctx, job.ID); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
