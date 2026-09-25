package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// The per-job gate serializes pipeline handoffs, rerun admission and expiry.
// Archive-wide backfill readers additionally reserve artifactGate: long-running
// recording jobs must not prevent unrelated terminal jobs from being cleaned.
func (s *Store) lockArtifacts(job string) func() {
	m, _ := s.artifactJobs.LoadOrStore(job, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func (s *Store) tryLockArtifacts(job string) (func(), bool) {
	m, _ := s.artifactJobs.LoadOrStore(job, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	if !mu.TryLock() {
		return nil, false
	}
	return mu.Unlock, true
}
func (s *Store) ensureRetentionSchema() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS artifact_operations (job_id TEXT PRIMARY KEY, operation TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS artifact_availability (job_id TEXT PRIMARY KEY, published_attempt INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT '', output TEXT NOT NULL DEFAULT '', video TEXT NOT NULL DEFAULT '');`)
	return err
}

// This is a pending-operation journal, not retained eviction-event history.
type artifactOperation struct {
	MissingMeeting bool     `json:"missing_meeting,omitempty"`
	Job            string   `json:"job"`
	Attempt        int      `json:"attempt"`
	Action         string   `json:"action"`
	Kind           string   `json:"kind"`
	Targets        []string `json:"targets"`
	Digest         string   `json:"digest,omitempty"`
	Revision       int      `json:"revision,omitempty"`
	Deadline       string   `json:"deadline,omitempty"`
}

func decodeArtifactOperation(raw string) (artifactOperation, error) {
	var op artifactOperation
	err := json.Unmarshal([]byte(raw), &op)
	return op, err
}

func validArtifactJob(id string) bool {
	return id != "" && id != "." && id != ".." && filepath.Base(id) == id && !strings.ContainsAny(id, `/\`)
}
func safeArtifactPath(root, path string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return errors.New("artifact path escapes work root")
	}
	for p := path; ; p = filepath.Dir(p) {
		info, e := os.Lstat(p)
		if e == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink artifact path: %s", p)
		}
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if p == root || p == filepath.Dir(p) {
			break
		}
	}
	return nil
}
func validateArtifactTree(root, path string) error {
	if err := safeArtifactPath(root, path); err != nil {
		return err
	}
	return filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if os.IsNotExist(err) && p == path {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink in artifact: %s", p)
		}
		if !d.IsDir() && !d.Type().IsRegular() {
			return fmt.Errorf("non-regular artifact: %s", p)
		}
		return nil
	})
}
func (rt *Runtime) operationDir(job string) string {
	return filepath.Join(rt.cfg.WorkRoot, ".artifact-operations", job)
}
func (rt *Runtime) saveOperation(op artifactOperation) error {
	// Flush prepared replacements before making the recovery promise durable.
	if err := syncArtifactTree(rt.operationDir(op.Job)); err != nil {
		return err
	}
	b, err := json.Marshal(op)
	if err != nil {
		return err
	}
	_, err = rt.store.db.Exec(`INSERT INTO artifact_operations(job_id,operation) VALUES(?,?) ON CONFLICT(job_id) DO UPDATE SET operation=excluded.operation`, op.Job, string(b))
	return err
}
func (rt *Runtime) pendingArtifactOperation(job string) bool {
	var n int
	err := rt.store.db.QueryRow(`SELECT count(*) FROM artifact_operations WHERE job_id=?`, job).Scan(&n)
	return err != nil || n != 0
}
func (rt *Runtime) finishOperation(op artifactOperation) error {
	if !validArtifactJob(op.Job) {
		return errors.New("invalid operation job")
	}
	dir := rt.operationDir(op.Job)
	if err := validateArtifactTree(rt.cfg.WorkRoot, dir); err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, p := range []string{canonicalRunPath(rt.cfg.WorkRoot, op.Job), canonicalMeetingPath(rt.cfg.WorkRoot, op.Job), canonicalOpusPath(rt.cfg.WorkRoot, op.Job), attemptRunPath(rt.cfg.WorkRoot, op.Job, op.Attempt), attemptMeetingPath(rt.cfg.WorkRoot, op.Job, op.Attempt), attemptSealDir(rt.cfg.WorkRoot, op.Job, op.Attempt), attemptSitePath(rt.cfg.WorkRoot, op.Job, op.Attempt), attemptLogsDir(rt.cfg.WorkRoot, op.Job, op.Attempt)} {
		rel, _ := filepath.Rel(rt.cfg.WorkRoot, p)
		allowed[rel] = true
	}
	for i, rel := range op.Targets {
		if !allowed[rel] {
			return fmt.Errorf("unowned operation target %q", rel)
		}
		dst := filepath.Join(rt.cfg.WorkRoot, rel)
		if err := validateArtifactTree(rt.cfg.WorkRoot, dst); err != nil {
			return err
		}
		if op.Action == "promote" || op.Action == "video" {
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return err
			}
		}
		staged := filepath.Join(dir, fmt.Sprintf("new-%d", i))
		old := filepath.Join(dir, fmt.Sprintf("old-%d", i))
		removeOnly := op.Action == "remove" || (op.Action == "promote" && op.MissingMeeting && i == 0)
		if !removeOnly && (op.Action == "promote" || op.Action == "video") {
			if _, err := os.Stat(staged); os.IsNotExist(err) {
				continue
			} else if err != nil {
				return err
			}
		} else if !removeOnly {
			return errors.New("unknown artifact operation")
		}
		if _, err := os.Stat(old); os.IsNotExist(err) {
			if err = os.Rename(dst, old); err != nil && !os.IsNotExist(err) {
				return err
			}
		} else if err != nil {
			return err
		}
		if !removeOnly {
			if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
				return err
			}
			if err := os.Rename(staged, dst); err != nil {
				return err
			}
		}
		for _, p := range []string{filepath.Dir(dst), dir} {
			if err := syncArtifactDir(p); err != nil {
				return err
			}
		}
	}
	switch op.Action {
	case "promote":
		tx, err := rt.store.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		var meeting any = canonicalMeetingPath(rt.cfg.WorkRoot, op.Job)
		if op.MissingMeeting {
			meeting = nil
		}
		if _, err = tx.Exec(`UPDATE jobs SET artifact_meeting_path=?,artifact_opus_path=?,artifact_opus_sha256=? WHERE id=?`, meeting, canonicalOpusPath(rt.cfg.WorkRoot, op.Job), op.Digest, op.Job); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO artifact_availability(job_id,published_attempt,output) VALUES(?,?,'present') ON CONFLICT(job_id) DO UPDATE SET published_attempt=excluded.published_attempt,output='present'`, op.Job, op.Attempt); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	case "video":
		// Finish a video-only operation journalled before whole-recording policies.
		if _, err := rt.store.db.Exec(`INSERT INTO artifact_availability(job_id,video) VALUES(?,'expired') ON CONFLICT(job_id) DO UPDATE SET video='expired'`, op.Job); err != nil {
			return err
		}
	case "remove":
		// "audio" and "video" remain supported only to recover pre-upgrade journals.
		if op.Kind == "recordings" || op.Kind == "audio" || op.Kind == "current" {
			column := "source"
			if op.Kind == "current" {
				column = "output"
			}
			if _, err := rt.store.db.Exec(`INSERT INTO artifact_availability(job_id,`+column+`) VALUES(?,'expired') ON CONFLICT(job_id) DO UPDATE SET `+column+`='expired'`, op.Job); err != nil {
				return err
			}
		}
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if _, err := rt.store.db.Exec(`DELETE FROM artifact_operations WHERE job_id=?`, op.Job); err != nil {
		return err
	}
	rt.logger.Printf("artifact operation completed job=%s attempt=%d action=%s kind=%s deadline=%s revision=%d", op.Job, op.Attempt, op.Action, op.Kind, op.Deadline, op.Revision)
	rt.store.emitStateChange(context.Background(), "job.updated", op.Job, op.Attempt)
	return nil
}

func syncArtifactDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
func syncArtifactTree(path string) error {
	var dirs []string
	err := filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Sync()
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = syncArtifactDir(dirs[i]); err != nil {
			return err
		}
	}
	return syncArtifactDir(filepath.Dir(path))
}
func (rt *Runtime) recoverArtifactOperations() {
	rows, err := rt.store.db.Query(`SELECT operation FROM artifact_operations`)
	if err != nil {
		rt.logger.Printf("artifact recovery failed: %v", err)
		return
	}
	var ops []artifactOperation
	for rows.Next() {
		var b string
		var op artifactOperation
		if err = rows.Scan(&b); err == nil {
			err = json.Unmarshal([]byte(b), &op)
		}
		if err != nil {
			rt.logger.Printf("invalid artifact journal: %v", err)
			continue
		}
		ops = append(ops, op)
	}
	rows.Close()
	for _, op := range ops {
		if err := rt.finishOperation(op); err != nil {
			rt.logger.Printf("artifact recovery job=%s failed: %v", op.Job, err)
		}
	}
}
func (rt *Runtime) promotePublishedPair(job string, attempt int) error {
	if !validArtifactJob(job) {
		return errors.New("invalid job ID")
	}
	attempts, err := rt.store.ListJobAttempts(context.Background(), job)
	if err != nil {
		return err
	}
	var a *JobAttempt
	for i := range attempts {
		if attempts[i].AttemptNumber == attempt {
			a = &attempts[i]
			break
		}
	}
	if a == nil || a.State != "succeeded" || a.PublishFinishedAt == nil || a.ArtifactOpusSHA256 == nil {
		return errors.New("publication is not durably successful")
	}
	var prior int
	_ = rt.store.db.QueryRow(`SELECT published_attempt FROM artifact_availability WHERE job_id=?`, job).Scan(&prior)
	if prior == attempt {
		return nil
	}
	// Legacy installs may have removed the successful attempt's intermediate.
	// Adopt only a canonical seal whose digest proves the published version,
	// and an intermediate whose own lineage agrees (or is genuinely absent).
	if prior == 0 {
		cp := canonicalOpusPath(rt.cfg.WorkRoot, job)
		mp := canonicalMeetingPath(rt.cfg.WorkRoot, job)
		for _, p := range []string{cp, mp} {
			if e := validateArtifactTree(rt.cfg.WorkRoot, p); e != nil {
				return e
			}
		}
		if digest, e := fileSHA256(cp); e == nil && digest == *a.ArtifactOpusSHA256 {
			var m MeetingBundleManifest
			b, e := os.ReadFile(filepath.Join(mp, "cassini.json"))
			_, statErr := os.Stat(mp)
			matches := os.IsNotExist(statErr)
			if e == nil && json.Unmarshal(b, &m) == nil {
				matches = m.JobID == job && m.AttemptNumber == attempt
			}
			if matches {
				var meeting any = mp
				if os.IsNotExist(statErr) {
					meeting = nil
				}
				tx, e := rt.store.db.Begin()
				if e != nil {
					return e
				}
				defer tx.Rollback()
				if _, e = tx.Exec(`UPDATE jobs SET artifact_meeting_path=?,artifact_opus_path=?,artifact_opus_sha256=? WHERE id=?`, meeting, cp, digest, job); e != nil {
					return e
				}
				if _, e = tx.Exec(`INSERT INTO artifact_availability(job_id,published_attempt,output) VALUES(?,?,'present') ON CONFLICT(job_id) DO UPDATE SET published_attempt=excluded.published_attempt,output='present'`, job, attempt); e != nil {
					return e
				}
				return tx.Commit()
			}
		}
		// Preserve identified failed output in its attempt namespace before
		// restoring the last published seal. Unknown lineage fails closed.
		if _, e := os.Stat(cp); e == nil {
			if err := rt.preserveLegacyFailedPair(job, attempts); err != nil {
				return err
			}
		}
	}
	meeting, opus := attemptMeetingPath(rt.cfg.WorkRoot, job, attempt), attemptOpusPath(rt.cfg.WorkRoot, job, attempt)
	if err = validateArtifactTree(rt.cfg.WorkRoot, meeting); err != nil {
		return err
	}
	if err = validateArtifactTree(rt.cfg.WorkRoot, opus); err != nil {
		return err
	}
	if err = verifySealedPublishInput(job, opus, *a.ArtifactOpusSHA256); err != nil {
		return err
	}
	dir := rt.operationDir(job)
	if err = validateArtifactTree(rt.cfg.WorkRoot, dir); err != nil {
		return err
	}
	if rt.pendingArtifactOperation(job) {
		return errors.New("pending artifact operation")
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	missingMeeting := false
	if _, e := os.Stat(meeting); os.IsNotExist(e) && prior == 0 {
		missingMeeting = true
	} else if e != nil {
		return e
	}
	if !missingMeeting {
		if err = copyDirectory(meeting, filepath.Join(dir, "new-0")); err != nil {
			return err
		}
	}
	if err = os.Link(opus, filepath.Join(dir, "new-1")); err != nil {
		if err = copyFile(opus, filepath.Join(dir, "new-1"), 0600); err != nil {
			return err
		}
	}
	op := artifactOperation{Job: job, Attempt: attempt, Action: "promote", Digest: *a.ArtifactOpusSHA256, MissingMeeting: missingMeeting}
	for _, p := range []string{canonicalMeetingPath(rt.cfg.WorkRoot, job), canonicalOpusPath(rt.cfg.WorkRoot, job)} {
		rel, _ := filepath.Rel(rt.cfg.WorkRoot, p)
		op.Targets = append(op.Targets, rel)
	}
	if err = rt.saveOperation(op); err != nil {
		return err
	}
	return rt.finishOperation(op)
}

func (rt *Runtime) preserveLegacyFailedPair(job string, attempts []JobAttempt) error {
	mp, cp := canonicalMeetingPath(rt.cfg.WorkRoot, job), canonicalOpusPath(rt.cfg.WorkRoot, job)
	for _, p := range []string{mp, cp} {
		if err := validateArtifactTree(rt.cfg.WorkRoot, p); err != nil {
			return err
		}
	}
	b, err := os.ReadFile(filepath.Join(mp, "cassini.json"))
	if err != nil {
		return errors.New("ambiguous legacy intermediate")
	}
	var m MeetingBundleManifest
	if json.Unmarshal(b, &m) != nil || m.JobID != job {
		return errors.New("ambiguous legacy lineage")
	}
	for _, a := range attempts {
		if a.AttemptNumber != m.AttemptNumber || a.State != "failed" || a.ArtifactOpusSHA256 == nil {
			continue
		}
		digest, err := fileSHA256(cp)
		if err != nil || digest != *a.ArtifactOpusSHA256 {
			return errors.New("legacy seal lineage mismatch")
		}
		meeting, opus := attemptMeetingPath(rt.cfg.WorkRoot, job, a.AttemptNumber), attemptOpusPath(rt.cfg.WorkRoot, job, a.AttemptNumber)
		for _, p := range []string{meeting, opus} {
			if err = validateArtifactTree(rt.cfg.WorkRoot, p); err != nil {
				return err
			}
		}
		if _, err = os.Stat(meeting); os.IsNotExist(err) {
			if err = copyDirectory(mp, meeting); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			// Existing attempt content must identify the same generation.
			am, ok, e := LoadMeetingBundleManifest(meeting)
			if e != nil || !ok || am.JobID != job || am.AttemptNumber != a.AttemptNumber {
				return errors.New("ambiguous legacy attempt intermediate")
			}
		}
		if _, err = os.Stat(opus); os.IsNotExist(err) {
			if err = os.MkdirAll(filepath.Dir(opus), 0755); err != nil {
				return err
			}
			if err = copyFile(cp, opus, 0600); err != nil {
				return err
			}
		} else if err != nil {
			return err
		} else {
			d, e := fileSHA256(opus)
			if e != nil || d != digest {
				return errors.New("legacy attempt seal mismatch")
			}
		}
		if err = syncArtifactTree(meeting); err != nil {
			return err
		}
		return syncArtifactTree(filepath.Dir(opus))
	}
	return errors.New("ambiguous legacy current archive; preserve for manual reconciliation")
}
