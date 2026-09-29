package operator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type remoteRetentionOperation struct {
	Name        string `json:"name"`
	Action      string `json:"action"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	FileID      int64  `json:"fileId"`
	InputETag   string `json:"inputETag"`
	InputSHA    string `json:"inputSha"`
	OutputSHA   string `json:"outputSha"`
	Directory   string `json:"directory"`
	Desired     int64  `json:"desired"`
	Revision    int    `json:"revision"`
	DocumentID  string `json:"documentId"`
}

func (s *Store) saveRemoteOperation(ctx context.Context, op remoteRetentionOperation, status string) error {
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO remote_retention_operation(name,operation_json,status,updated_at) VALUES(?,?,?,?) ON CONFLICT(name) DO UPDATE SET operation_json=excluded.operation_json,status=excluded.status,last_error='',updated_at=excluded.updated_at`, op.Name, raw, status, nowUTCString())
	return err
}
func (s *Store) pendingRemoteOperations(ctx context.Context) ([]remoteRetentionOperation, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT operation_json FROM remote_retention_operation WHERE status!='completed' ORDER BY updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []remoteRetentionOperation{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var op remoteRetentionOperation
		if err := json.Unmarshal(raw, &op); err != nil {
			return nil, err
		}
		result = append(result, op)
	}
	return result, rows.Err()
}

// Reconcile only identities already tied to successful Cassini publication.
// Unknown Files entries are not adopted based on a filename alone.
func (s *annotationService) reconcileRetentionInventory(ctx context.Context) error {
	jobs, err := s.rt.store.ListJobs(ctx)
	if err != nil {
		return err
	}
	names, err := s.exapp.ownerRecordingNames(ctx, s.client)
	if err != nil {
		return err
	}
	ids := map[string]int64{}
	for id, name := range names {
		logical := logicalMeetingName(name)
		if previous, ok := ids[logical]; ok && previous != id {
			return fmt.Errorf("ambiguous managed file identity")
		}
		ids[logical] = id
	}
	for _, job := range jobs {
		name := job.ID + ".opus"
		if _, ok, err := s.rt.store.meetingLifecycle(ctx, name); err != nil {
			return err
		} else if ok {
			continue
		}
		id := ids[name]
		if id == 0 {
			continue
		}
		published, err := s.rt.store.HasSuccessfulPublish(ctx, job.ID)
		if err != nil {
			return err
		}
		if !published {
			continue
		}
		anchor := retentionAnchor(job.RecordFinishedAt)
		source := "recording-completed"
		// A rerun must not reset age. The earliest valid capture completion is used.
		attempts, err := s.rt.store.ListJobAttempts(ctx, job.ID)
		if err != nil {
			return err
		}
		for _, attempt := range attempts {
			date := retentionAnchor(attempt.RecordFinishedAt)
			if !date.IsZero() && (anchor.IsZero() || date.Before(anchor)) {
				anchor = date
			}
		}
		if anchor.IsZero() {
			continue
		}
		if err := s.rt.store.adoptMeetingLifecycle(ctx, meetingLifecycle{Name: name, FileID: id, Path: ncRecordingsRoot + "/meetings/" + names[id], Representation: "opus", State: "active", Anchor: anchor.UTC().Format(time.RFC3339Nano), AnchorSource: source}); err != nil {
			return err
		}
	}
	return nil
}

func (s *annotationService) runRemoteRetention(ctx context.Context, now time.Time) error {
	if err := s.recoverRemoteRetention(ctx); err != nil {
		return err
	}
	if !remoteRetentionImplemented {
		return nil
	}
	if err := s.reconcileRetentionInventory(ctx); err != nil {
		return err
	}
	s.rt.retention.mu.Lock()
	settings := s.rt.retention.settings
	s.rt.retention.mu.Unlock()
	meetings, err := s.rt.store.retainedMeetings(ctx)
	if err != nil {
		return err
	}
	var failures error
	for _, m := range meetings {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		effect := evaluateRemoteRetention(m, settings.Nextcloud, now)
		if effect.Action == "retired" {
			anchor, _ := time.Parse(time.RFC3339Nano, m.Anchor)
			if settings.Nextcloud.Transcriptions.due(anchor, now) {
				state, e := s.exapp.davRetentionLeaf(ctx, s.client, m.Path)
				if e != nil {
					failures = errors.Join(failures, e)
					continue
				}
				if state.Exists {
					effect.Action = "retire"
				}
			}
		}
		if effect.Action != "convert" && effect.Action != "retire" {
			continue
		}
		jobID := strings.TrimSuffix(m.Name, ".opus")
		job, err := s.rt.store.GetJob(ctx, jobID)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		if job.Stage != "done" {
			continue
		}
		unlock, ok := s.rt.store.tryLockArtifacts(jobID)
		if !ok {
			continue
		}
		err = s.prepareRemoteRetention(ctx, m, effect.Action, settings.Revision)
		unlock()
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("%s: %w", m.Name, err))
		}
	}
	return failures
}

func (s *annotationService) prepareRemoteRetention(ctx context.Context, m meetingLifecycle, action string, revision int) error {
	// Finish any old write-behind attempt before taking the upload reservation.
	if err := s.syncAnnotation(ctx, m.Name); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	release, err := annotationWriteLocks.acquire(ctx, m.Name)
	if err != nil {
		return err
	}
	defer release()
	current, ok, err := s.rt.store.meetingLifecycle(ctx, m.Name)
	if err != nil {
		return err
	}
	if !ok || (current.State != "active" && !(action == "retire" && current.State == "retired")) {
		return fmt.Errorf("meeting is not active")
	}
	state, err := s.exapp.davRetentionLeaf(ctx, s.client, m.Path)
	if err != nil {
		return err
	}
	if !state.Exists || state.FileID != m.FileID {
		return fmt.Errorf("managed file identity changed or is missing")
	}
	if state.Size > maxAnnotateRecordingBytes {
		return fmt.Errorf("meeting exceeds staging limit")
	}
	var disk unix.Statfs_t
	if err := unix.Statfs(s.rt.cfg.WorkRoot, &disk); err != nil {
		return err
	}
	if uint64(disk.Bavail)*uint64(disk.Bsize) < uint64(state.Size)+2*(64<<20) {
		return fmt.Errorf("insufficient retention staging space")
	}
	dir, err := os.MkdirTemp(s.rt.cfg.WorkRoot, "remote-retention-")
	if err != nil {
		return err
	}
	owned := false
	defer func() {
		if !owned {
			os.RemoveAll(dir)
		}
	}()
	in := filepath.Join(dir, "source.opus")
	inputSHA, _, _, err := s.exapp.davDownloadFile(ctx, retentionDAVClient(s.client), ncRecordingsOwner, m.Path, in, maxAnnotateRecordingBytes, state.ETag)
	if err != nil {
		return err
	}
	op := remoteRetentionOperation{Name: m.Name, Action: action, Source: m.Path, Destination: ncRecordingsRoot + "/meetings/" + strings.TrimSuffix(m.Name, ".opus") + transcriptionSuffix, FileID: m.FileID, InputETag: state.ETag, InputSHA: inputSHA, Directory: dir, Revision: revision, DocumentID: m.DocumentID}
	if action == "convert" {
		remote, err := runAnnotateShow(ctx, s.bin, in)
		if err != nil || remote.Unsupported {
			return fmt.Errorf("cannot verify source annotations: %w", err)
		}
		store := s.rt.annotationReads()
		if store == nil {
			return fmt.Errorf("durable annotation store unavailable")
		}
		unlock, err := annotationMutationLocks.acquire(ctx, store.path)
		if err != nil {
			return err
		}
		var desired, confirmed int64
		var flight sql.NullInt64
		var republish []byte
		err = store.db.QueryRowContext(ctx, `SELECT desired,confirmed,in_flight,republish_json FROM annotation_head WHERE opus_name=?`, m.Name).Scan(&desired, &confirmed, &flight, &republish)
		var captured annotateResult
		var token string
		if err == nil {
			previous, e := s.snapshot(ctx, confirmed)
			if e != nil {
				err = e
			} else if previous.AudioOpusSHA256 != remote.AudioOpusSHA256 || !sameAnnotationDocument(previous.Annotations, remote.Annotations) || flight.Valid || republish != nil {
				err = fmt.Errorf("remote annotations or in-flight delivery conflict")
			}
			if err == nil {
				captured, err = s.snapshot(ctx, desired)
			}
			if err == nil {
				var generation string
				err = store.db.QueryRowContext(ctx, `SELECT value FROM annotations_meta WHERE key='generation'`).Scan(&generation)
				token = fmt.Sprintf("%s:%d", generation, desired)
			}
		} else if errors.Is(err, sql.ErrNoRows) {
			err = nil
			captured = remote
		}
		unlock()
		if err != nil {
			return err
		}
		op.Desired = desired
		args := []string{"extract", "transcription", "--out", filepath.Join(dir, "output.json"), "--age-anchor", m.Anchor, "--anchor-source", m.AnchorSource, "--policy-revision", fmt.Sprint(revision), "--annotation-revision", fmt.Sprint(captured.Revision), "--state-token", token}
		if m.DocumentID != "" {
			args = append(args, "--document-id", m.DocumentID)
		}
		if len(captured.Annotations) > 0 && string(captured.Annotations) != "null" {
			a := filepath.Join(dir, "annotations.json")
			if err := os.WriteFile(a, captured.Annotations, 0600); err != nil {
				return err
			}
			args = append(args, "--annotations-file", a)
		}
		args = append(args, in)
		if output, err := exec.CommandContext(ctx, s.bin, args...).CombinedOutput(); err != nil {
			return fmt.Errorf("extract retained meeting: %w: %.1024s", err, output)
		}
		if err := os.Remove(filepath.Join(dir, "annotations.json")); err != nil && !os.IsNotExist(err) {
			return err
		}
		out := filepath.Join(dir, "output.json")
		op.OutputSHA, err = fileSHA256(out)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			return err
		}
		var identity struct {
			Identity struct {
				DocumentID string `json:"documentId"`
			} `json:"identity"`
		}
		if err = json.Unmarshal(raw, &identity); err != nil {
			return err
		}
		op.DocumentID = identity.Identity.DocumentID
	}
	// Intent is serialized with settings updates and mutation acceptance. Once
	// durable, recovery finishes this exact operation even after policy changes.
	store := s.rt.annotationReads()
	if store == nil {
		return fmt.Errorf("annotation store unavailable")
	}
	mutation, err := annotationMutationLocks.acquire(ctx, store.path)
	if err != nil {
		return err
	}
	s.rt.retention.mu.Lock()
	if s.rt.retention.settings.Revision != revision {
		s.rt.retention.mu.Unlock()
		mutation()
		return fmt.Errorf("retention settings changed during preparation")
	}
	for _, directory := range []string{dir, s.rt.cfg.WorkRoot} {
		f, e := os.Open(directory)
		if e != nil {
			err = e
			break
		}
		e = f.Sync()
		f.Close()
		if e != nil {
			err = e
			break
		}
	}
	if err == nil {
		err = s.rt.store.saveRemoteOperation(ctx, op, "prepared")
	}
	if err == nil {
		owned = true
		state := "converting"
		if action == "retire" {
			state = "retiring"
		}
		_, err = s.rt.store.db.ExecContext(ctx, `UPDATE meeting_lifecycle SET state=? WHERE name=?`, state, m.Name)
	}
	s.rt.retention.mu.Unlock()
	mutation()
	if err != nil {
		return err
	}
	return s.resumeRemoteRetention(ctx, op)
}

func (s *annotationService) recoverRemoteRetention(ctx context.Context) error {
	operations, err := s.rt.store.pendingRemoteOperations(ctx)
	if err != nil {
		return err
	}
	var failures error
	for _, op := range operations {
		release, err := annotationWriteLocks.acquire(ctx, op.Name)
		if err != nil {
			return err
		}
		err = s.resumeRemoteRetention(ctx, op)
		release()
		if err != nil {
			_, _ = s.rt.store.db.ExecContext(ctx, `UPDATE remote_retention_operation SET last_error=?,updated_at=? WHERE name=?`, "Remote identity or delivery could not be verified; recovery will retry.", nowUTCString(), op.Name)
			failures = errors.Join(failures, fmt.Errorf("recover %s: %w", op.Name, err))
		}
	}
	return failures
}

func (s *annotationService) observeRetention(ctx context.Context, op remoteRetentionOperation, rel string) (ncLeafState, string, error) {
	state, err := s.exapp.davRetentionLeaf(ctx, s.client, rel)
	if err != nil || !state.Exists {
		return state, "", err
	}
	if state.FileID != op.FileID {
		return state, "", fmt.Errorf("unexpected live file identity")
	}
	digest, _, _, err := s.exapp.davDownloadFile(ctx, retentionDAVClient(s.client), ncRecordingsOwner, rel, filepath.Join(op.Directory, "verify"), maxAnnotateRecordingBytes, state.ETag)
	return state, digest, err
}
func (s *annotationService) resumeRemoteRetention(ctx context.Context, op remoteRetentionOperation) error {
	if path.Base(op.Name) != op.Name || filepath.Dir(op.Directory) != filepath.Clean(s.rt.cfg.WorkRoot) || !strings.HasPrefix(filepath.Base(op.Directory), "remote-retention-") {
		return fmt.Errorf("invalid retention journal staging ownership")
	}
	if err := os.MkdirAll(op.Directory, 0700); err != nil {
		return err
	}
	if op.Action == "retire" {
		return s.resumeRemoteRetirement(ctx, op)
	}
	source, digest, err := s.observeRetention(ctx, op, op.Source)
	if err != nil {
		return err
	}
	if source.Exists {
		if digest == op.InputSHA {
			if source.ETag != op.InputETag {
				return fmt.Errorf("source ETag changed")
			}
			output := filepath.Join(op.Directory, "output.json")
			sha, err := fileSHA256(output)
			if err != nil || sha != op.OutputSHA {
				return fmt.Errorf("prepared output missing or changed")
			}
			if err := s.exapp.davRetentionPut(ctx, s.client, op.Source, output, source.ETag); err != nil {
				return err
			}
			source, digest, err = s.observeRetention(ctx, op, op.Source)
			if err != nil {
				return err
			}
		}
		if digest != op.OutputSHA {
			return fmt.Errorf("source bytes conflict with journal")
		}
		if err := s.rt.store.saveRemoteOperation(ctx, op, "put-verified"); err != nil {
			return err
		}
		// Audio staging is no longer needed to recover once exact JSON is verified.
		if err := os.Remove(filepath.Join(op.Directory, "source.opus")); err != nil && !os.IsNotExist(err) {
			return err
		}
		if op.Source != op.Destination {
			dest, err := s.exapp.davRetentionLeaf(ctx, s.client, op.Destination)
			if err != nil {
				return err
			}
			if dest.Exists {
				return fmt.Errorf("retention destination occupied")
			}
			if err := s.exapp.davRetentionMutation(ctx, s.client, "MOVE", op.Source, op.Destination, source.ETag); err != nil {
				return err
			}
		}
	}
	dest, sha, err := s.observeRetention(ctx, op, op.Destination)
	if err != nil {
		return err
	}
	if !dest.Exists || sha != op.OutputSHA {
		return fmt.Errorf("retained destination could not be verified")
	}
	if _, err = s.rt.store.db.ExecContext(ctx, `UPDATE meeting_lifecycle SET document_path=?,representation='transcription',state='active',document_id=? WHERE name=? AND file_id=?`, op.Destination, op.DocumentID, op.Name, op.FileID); err != nil {
		return err
	}
	if op.Desired > 0 {
		if err := s.confirmAnnotation(ctx, op.Name, op.Desired); err != nil {
			return err
		}
	}
	if s.exapp.sharePaths != nil {
		s.exapp.sharePaths.mu.Lock()
		s.exapp.sharePaths.entries = nil
		s.exapp.sharePaths.ownerExpires = time.Time{}
		s.exapp.sharePaths.mu.Unlock()
	}
	return s.finishRemoteRetention(ctx, op)
}
func (s *annotationService) finishRemoteRetention(ctx context.Context, op remoteRetentionOperation) error {
	if err := os.RemoveAll(op.Directory); err != nil {
		return err
	}
	// Completed audits contain no content or filesystem staging inventories.
	op.Directory = ""
	op.InputETag = ""
	op.InputSHA = ""
	op.OutputSHA = ""
	op.Desired = 0
	return s.rt.store.saveRemoteOperation(ctx, op, "completed")
}
