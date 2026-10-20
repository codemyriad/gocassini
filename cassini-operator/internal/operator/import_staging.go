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

// Staging work — confirming an upload, removing a staged file, sweeping the
// folder — is serialized per user through userLocks, bounded by a deadline so a
// failed Nextcloud cannot hold a lock indefinitely. The service mutex is never
// taken here: it guards only the worker's running job.
const stagingLockTimeout = 10 * time.Second

func (s *importService) stagingAction(w http.ResponseWriter, r *http.Request, job ImportJob, action string) {
	ctx, cancel := context.WithTimeout(r.Context(), stagingLockTimeout)
	defer cancel()
	lockErr := s.withUserLock(ctx, job.CreatedBy, func(ctx context.Context) error {
		// Re-read under the lock: another tab may have settled the row.
		job, ok, err := s.rt.store.ImportJob(ctx, job.ID)
		if err != nil {
			writeJSONError(w, 500, err.Error())
			return nil
		}
		if !ok || job.ClearedAt != "" {
			http.NotFound(w, r)
			return nil
		}
		switch action {
		case "heartbeat":
			touched, err := s.rt.store.TouchImportUpload(ctx, job.ID)
			if err == nil && !touched {
				writeJSONError(w, 409, "upload is no longer active")
				return nil
			}
			if err != nil {
				writeJSONError(w, 500, err.Error())
				return nil
			}
		case "staged":
			if job.State == importStateStaged {
				writeJSON(w, 200, job)
				return nil
			}
			if job.State != importStateUploading {
				writeJSONError(w, 409, "upload is no longer active")
				return nil
			}
			if err := s.inspectUpload(ctx, job); err != nil {
				writeJSONError(w, 500, err.Error())
				return nil
			}
			current, _, err := s.rt.store.ImportJob(ctx, job.ID)
			if err != nil {
				writeJSONError(w, 500, err.Error())
				return nil
			}
			if current.State != importStateStaged {
				writeJSONError(w, 409, "upload is incomplete; add the file again")
				return nil
			}
		case "upload-failed":
			if job.State == importStateUploading {
				if err := s.inspectUpload(ctx, job); err != nil {
					writeJSONError(w, 500, err.Error())
					return nil
				}
			}
		case "remove":
			if importUsesStaging(job) || s.running() == job.ID {
				writeJSONError(w, 409, "upload or import is still active")
				return nil
			}
			if _, pathErr := sanitizeStagedImportPath(job.StagedPath); pathErr != nil {
				writeJSONError(w, 409, "this job does not have a Nextcloud staging file")
				return nil
			}
			if err := s.removeStaging(ctx, job); err != nil {
				writeJSONError(w, 500, err.Error())
				return nil
			}
		}
		writeJSON(w, 200, map[string]bool{"accepted": true})
		return nil
	})
	if lockErr != nil {
		writeJSONError(w, http.StatusServiceUnavailable, "staging is busy; try again")
	}
}

func importUsesStaging(job ImportJob) bool {
	switch job.State {
	case importStateUploading, importStateQueued, importStateFetching, importStateValidating, importStateDelivering, importStateSharing, importStateIndexing:
		return true
	}
	return false
}

// inspectUpload settles an uploading row from what is actually in the caller's
// Files: the whole file means staged, anything else means upload_failed.
func (s *importService) inspectUpload(ctx context.Context, job ImportJob) error {
	leaf, err := s.exapp.davPropfindLeafState(ctx, s.client, job.CreatedBy, job.StagedPath)
	if err != nil {
		return err
	}
	if !leaf.Exists || leaf.Size != job.ExpectedBytes {
		return s.rt.store.SetImportJobState(ctx, job.ID, importStateUploadFailed, "", "Upload did not complete. Add the file again to import it.")
	}
	return s.rt.store.ConfirmImportUpload(ctx, job.ID)
}

// reconcileUploads settles the caller's uploads whose lease has lapsed — the
// browser went away without reporting the PUT's outcome, so the file decides.
func (s *importService) reconcileUploads(ctx context.Context, caller string) {
	ctx, cancel := context.WithTimeout(ctx, stagingLockTimeout)
	defer cancel()
	_ = s.withUserLock(ctx, caller, func(ctx context.Context) error {
		jobs, err := s.rt.store.ImportJobsFor(ctx, caller)
		if err != nil {
			return nil
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
		return nil
	})
}

// removeStaging is the explicit Remove: the staged file goes, and the row is
// hidden from its owner's list. Nothing else ever hides a row.
func (s *importService) removeStaging(ctx context.Context, job ImportJob) error {
	if err := s.exapp.davDeleteUserFile(ctx, s.client, job.CreatedBy, job.StagedPath); err != nil {
		return err
	}
	if err := s.rt.store.MarkImportStagingRemoved(ctx, job.ID); err != nil {
		return err
	}
	return s.rt.store.ClearImportJob(ctx, job.ID)
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

// stagingUsers is everyone whose staging folder may hold files: every user who
// ever registered an import, plus every Nextcloud user, which also covers
// orphan uploads that never got a row. Known owners remain sweepable even when
// Nextcloud's user-list endpoint is temporarily unavailable.
func (s *importService) stagingUsers(ctx context.Context) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	seen := map[string]bool{}
	known, err := s.rt.store.ImportCreators(ctx)
	if err != nil {
		return nil, err
	}
	for _, user := range known {
		seen[user] = true
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

// clearUserStaging deletes every inactive file in one user's staging folder —
// the nightly sweep, and the "Clear staged files" button. Files go; rows stay.
// A row whose file was deleted is marked so (no Retry), and remains listed
// with its error until its owner removes it.
func (s *importService) clearUserStaging(ctx context.Context, user string) error {
	ctx, cancel := context.WithTimeout(ctx, stagingLockTimeout)
	defer cancel()
	return s.withUserLock(ctx, user, func(ctx context.Context) error {
		entries, _, err := s.exapp.davListSizes(ctx, s.client, user, importStagingDir)
		if err != nil {
			return err
		}
		rows, err := s.rt.store.importJobsIncludingCleared(ctx, user)
		if err != nil {
			return err
		}
		byPath := map[string][]ImportJob{}
		for _, job := range rows {
			byPath[job.StagedPath] = append(byPath[job.StagedPath], job)
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
		running := s.running()
		var firstErr error
		for path := range paths {
			jobs := byPath[path]
			active := false
			for _, job := range jobs {
				if (job.ClearedAt == "" && importUsesStaging(job)) || running == job.ID {
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
				if err := s.rt.store.MarkImportStagingRemoved(ctx, job.ID); err != nil && firstErr == nil {
					firstErr = err
				}
			}
		}
		return firstErr
	})
}
