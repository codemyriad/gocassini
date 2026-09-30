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
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type remoteRetentionJournalError struct{ error }

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
	if err != nil {
		return remoteRetentionJournalError{err}
	}
	return nil
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
func (s *annotationService) retentionInventory(ctx context.Context) ([]meetingLifecycle, []remoteRetentionEffect, error) {
	existing, err := s.rt.store.retainedMeetings(ctx)
	if err != nil {
		return nil, nil, err
	}
	byName := map[string]meetingLifecycle{}
	for _, m := range existing {
		byName[m.Name] = m
	}
	jobs, err := s.rt.store.ListJobs(ctx)
	if err != nil {
		return nil, nil, err
	}
	names, err := s.exapp.ownerRecordingNames(ctx, s.client)
	if err != nil {
		return nil, nil, err
	}
	ids := map[string]int64{}
	for id, name := range names {
		logical := logicalMeetingName(name)
		if previous, ok := ids[logical]; ok && previous != id {
			return nil, nil, fmt.Errorf("ambiguous managed file identity")
		}
		ids[logical] = id
	}
	skipped := []remoteRetentionEffect{}
	for _, job := range jobs {
		name := job.ID + ".opus"
		if _, ok := byName[name]; ok {
			continue
		}
		id := ids[name]
		if id == 0 {
			continue
		}
		published, err := s.rt.store.HasSuccessfulPublish(ctx, job.ID)
		if err != nil {
			return nil, nil, err
		}
		if !published {
			continue
		}
		anchor := retentionAnchor(job.RecordFinishedAt)
		attempts, err := s.rt.store.ListJobAttempts(ctx, job.ID)
		if err != nil {
			return nil, nil, err
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
		byName[name] = meetingLifecycle{Name: name, FileID: id, Path: ncRecordingsRoot + "/meetings/" + names[id], Representation: "opus", State: "active", Anchor: anchor.UTC().Format(time.RFC3339Nano), AnchorSource: "recording-completed"}
	}
	meetings := []meetingLifecycle{}
	for name, m := range byName {
		meetings = append(meetings, m)
		delete(ids, name)
	}
	for name := range ids {
		skipped = append(skipped, remoteRetentionEffect{Name: name, Action: "skip", Reason: "Managed provenance or original recording date could not be established; file left unchanged."})
	}
	sort.Slice(meetings, func(i, j int) bool { return meetings[i].Name < meetings[j].Name })
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].Name < skipped[j].Name })
	return meetings, skipped, nil
}

func (s *annotationService) reconcileRetentionInventory(ctx context.Context) error {
	meetings, _, err := s.retentionInventory(ctx)
	if err != nil {
		return err
	}
	for _, m := range meetings {
		if _, ok, err := s.rt.store.meetingLifecycle(ctx, m.Name); err != nil {
			return err
		} else if ok {
			continue
		}
		if err := s.rt.store.adoptMeetingLifecycle(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

func (s *annotationService) runRemoteRetention(ctx context.Context, now time.Time) error {
	if !remoteRetentionImplemented {
		return nil
	}
	if s.rt.retention != nil {
		s.rt.retention.mu.Lock()
		forever := s.rt.retention.settings.Nextcloud.Recordings.Forever && s.rt.retention.settings.Nextcloud.Transcriptions.Forever
		disabled := s.rt.retention.loadErr != nil
		s.rt.retention.mu.Unlock()
		if disabled {
			return errRetentionUnavailable
		}
		if forever {
			pending, err := s.rt.store.pendingRemoteOperations(ctx)
			if err != nil || len(pending) == 0 {
				return err
			}
		}
	}
	if err := s.remoteRetentionCapability(ctx); err != nil {
		return err
	}
	recovery, err := s.recoverRemoteRetentionPass(ctx)
	if err != nil {
		return errors.Join(recovery.failures, err)
	}
	if err := s.reconcileRetentionInventory(ctx); err != nil {
		return errors.Join(recovery.failures, err)
	}
	s.rt.retention.mu.Lock()
	settings := s.rt.retention.settings
	s.rt.retention.mu.Unlock()
	meetings, err := s.rt.store.retainedMeetings(ctx)
	if err != nil {
		return errors.Join(recovery.failures, err)
	}
	failures := recovery.failures
	for _, m := range meetings {
		if err := ctx.Err(); err != nil {
			return errors.Join(failures, err)
		}
		if recovery.pending[m.Name] {
			continue
		}
		if m.State == "active" && m.Representation == "transcription" {
			state, e := s.exapp.davRetentionLeaf(ctx, s.client, m.Path)
			if e != nil {
				failures = errors.Join(failures, e)
				continue
			}
			if state.Exists && state.FileID == m.FileID {
				representation, e := s.exapp.davMeetingRepresentation(ctx, s.client, m.Path, state.ETag)
				if e != nil {
					failures = errors.Join(failures, e)
					continue
				}
				m.Representation = representation
			}
		}
		effect := evaluateRemoteRetention(m, settings.Nextcloud, now)
		// A restored retired file keeps its tombstone and is removed again only
		// when the original file identity can be verified.
		if effect.Action == "retired" {
			state, err := s.exapp.davRetentionLeaf(ctx, s.client, m.Path)
			if err != nil {
				failures = errors.Join(failures, err)
				continue
			}
			if !state.Exists {
				continue
			}
			if state.FileID != m.FileID {
				failures = errors.Join(failures, fmt.Errorf("restored identity changed: %s", m.Name))
				continue
			}
			effect.Action = "retire"
		}
		if effect.Action != "convert" && effect.Action != "retire" {
			continue
		}
		err = s.prepareRemoteRetention(ctx, m, effect.Action, settings.Revision)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("%s: %w", m.Name, err))
			var journalErr remoteRetentionJournalError
			if errors.As(err, &journalErr) {
				return failures
			}
		}
	}
	return failures
}

func (s *annotationService) prepareRemoteRetention(ctx context.Context, m meetingLifecycle, action string, revision int) error {
	if action == "retire" {
		return s.prepareWholeMeetingRetirement(ctx, m, action, revision)
	}
	if action != "convert" {
		return fmt.Errorf("unsupported retention action")
	}
	jobID := strings.TrimSuffix(m.Name, ".opus")
	unlock, ok := s.rt.store.tryLockArtifacts(jobID)
	if !ok {
		return nil
	}
	defer unlock()
	// Recovery owns unfinished intents, even when their lifecycle row became
	// active before final journal cleanup. Never replace them with new work.
	var pending bool
	if err := s.rt.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM remote_retention_operation WHERE name=? AND status!='completed')`, m.Name).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return nil
	}
	// Rerun admission uses this same lock. Read eligibility only after acquiring
	// it, and keep it through journaling and the remote mutation.
	job, err := s.rt.store.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job.Stage != "done" {
		return nil
	}
	// Reconcile ambiguous uploads, but capture ordinary pending edits directly
	// into JSON. Rewriting Opus first is unnecessary and can lose duplicate tags.
	if action == "convert" {
		store := s.rt.annotationReads()
		if store == nil {
			return fmt.Errorf("durable annotation store unavailable")
		}
		var flight sql.NullInt64
		var republish []byte
		err := store.db.QueryRowContext(ctx, `SELECT in_flight,republish_json FROM annotation_head WHERE opus_name=?`, m.Name).Scan(&flight, &republish)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if flight.Valid || republish != nil {
			if err := s.syncAnnotation(ctx, m.Name); err != nil {
				return err
			}
		}
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
	if !ok || current.FileID != m.FileID || current.Path != m.Path || (current.State != "active" && !(action == "retire" && current.State == "retired")) {
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
	if uint64(disk.Bavail)*uint64(disk.Bsize) < 2*uint64(state.Size)+2*(64<<20) {
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
		cmd := exec.CommandContext(ctx, s.bin, args...)
		cmd.Env = contextChildEnv(os.Environ())
		if output, err := cmd.CombinedOutput(); err != nil {
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
		var tx *sql.Tx
		tx, err = s.rt.store.db.BeginTx(ctx, nil)
		if err == nil {
			defer tx.Rollback()
			raw, e := json.Marshal(op)
			err = e
			if err == nil {
				_, err = tx.ExecContext(ctx, `INSERT INTO remote_retention_operation(name,operation_json,status,updated_at) VALUES(?,?,'prepared',?) ON CONFLICT(name) DO UPDATE SET operation_json=excluded.operation_json,status='prepared',last_error='',updated_at=excluded.updated_at`, op.Name, raw, nowUTCString())
			}
			state := "converting"
			if action == "retire" {
				state = "retiring"
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE meeting_lifecycle SET state=? WHERE name=?`, state, m.Name)
			}
			if err == nil {
				err = tx.Commit()
			}
			if err == nil {
				owned = true
			}
		}
	}
	s.rt.retention.mu.Unlock()
	mutation()
	if err != nil {
		return remoteRetentionJournalError{err}
	}
	return s.resumeRemoteRetention(ctx, op)
}

func (s *annotationService) prepareWholeMeetingRetirement(ctx context.Context, m meetingLifecycle, action string, revision int) error {
	if action != "retire" {
		return fmt.Errorf("unsupported retention action")
	}
	unlock, ok := s.rt.store.tryLockArtifacts(strings.TrimSuffix(m.Name, ".opus"))
	if !ok {
		return nil
	}
	defer unlock()
	var pending bool
	if err := s.rt.store.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM remote_retention_operation WHERE name=? AND status!='completed')`, m.Name).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return nil
	}
	job, err := s.rt.store.GetJob(ctx, strings.TrimSuffix(m.Name, ".opus"))
	if err != nil {
		return err
	}
	if job.Stage != "done" {
		return nil
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
	if !ok || (current.State != "active" && current.State != "retired") || current.FileID != m.FileID || current.Path != m.Path {
		return fmt.Errorf("meeting identity or state changed")
	}
	state, err := s.exapp.davRetentionLeaf(ctx, s.client, m.Path)
	if err != nil {
		return err
	}
	if !state.Exists || state.FileID != m.FileID {
		return fmt.Errorf("managed file identity changed or is missing")
	}
	op := remoteRetentionOperation{Name: m.Name, Action: "retire", Source: m.Path, FileID: m.FileID, InputETag: state.ETag, Revision: revision}
	// Serialize tombstone installation with annotation acceptance, and refuse a
	// stale settings snapshot before recording irreversible work.
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
	err = func() error {
		tx, err := s.rt.store.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback()
		raw, err := json.Marshal(op)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO remote_retention_operation(name,operation_json,status,updated_at) VALUES(?,?,'prepared',?) ON CONFLICT(name) DO UPDATE SET operation_json=excluded.operation_json,status='prepared',last_error='',updated_at=excluded.updated_at`, op.Name, raw, nowUTCString()); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE meeting_lifecycle SET state='retiring' WHERE name=?`, op.Name); err != nil {
			return err
		}
		return tx.Commit()
	}()
	s.rt.retention.mu.Unlock()
	mutation()
	if err != nil {
		return remoteRetentionJournalError{err}
	}
	return s.resumeRemoteRetention(ctx, op)
}

type remoteRetentionRecoveryResult struct {
	pending  map[string]bool
	failures error
}

// Startup reports all recovery errors. Sweeps can continue unrelated meetings
// after per-operation failures, but must stop on journal or cancellation errors.
func (s *annotationService) recoverRemoteRetention(ctx context.Context) error {
	result, err := s.recoverRemoteRetentionPass(ctx)
	return errors.Join(result.failures, err)
}

func (s *annotationService) recoverRemoteRetentionPass(ctx context.Context) (remoteRetentionRecoveryResult, error) {
	result := remoteRetentionRecoveryResult{pending: map[string]bool{}}
	operations, err := s.rt.store.pendingRemoteOperations(ctx)
	if err != nil {
		return result, err
	}
	for _, op := range operations {
		result.pending[op.Name] = true
	}
	for _, op := range operations {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		unlock, ok := s.rt.store.tryLockArtifacts(strings.TrimSuffix(op.Name, ".opus"))
		if !ok {
			continue
		}
		release, err := annotationWriteLocks.acquire(ctx, op.Name)
		if err != nil {
			unlock()
			return result, err
		}
		err = s.resumeRemoteRetention(ctx, op)
		release()
		unlock()
		if err != nil {
			var journalErr remoteRetentionJournalError
			if errors.As(err, &journalErr) {
				return result, err
			}
			result.failures = errors.Join(result.failures, fmt.Errorf("recover %s: %w", op.Name, err))
			if _, journalErr := s.rt.store.db.ExecContext(ctx, `UPDATE remote_retention_operation SET last_error=?,updated_at=? WHERE name=?`, "Remote identity or delivery could not be verified; recovery will retry.", nowUTCString(), op.Name); journalErr != nil {
				return result, fmt.Errorf("persist recovery failure for %s: %w", op.Name, journalErr)
			}
		} else {
			delete(result.pending, op.Name)
		}
	}
	return result, nil
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
	if op.Action == "retire" {
		return s.resumeWholeMeetingRetirement(ctx, op)
	}
	if op.Action != "convert" {
		return fmt.Errorf("unsupported retention intent")
	}
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
	if err := func() error {
		if op.Directory == "" {
			return nil
		}
		return os.RemoveAll(op.Directory)
	}(); err != nil {
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

// No network probe is needed for the default, never-used lifecycle. Pending
// journals are resumed only on a currently supported substrate.
func (s *annotationService) recoverRemoteRetentionAtStartup(ctx context.Context) error {
	if err := s.cleanupUnjournaledRetention(ctx); err != nil {
		return err
	}
	pending, err := s.rt.store.pendingRemoteOperations(ctx)
	if err != nil || len(pending) == 0 {
		return err
	}
	if !remoteRetentionImplemented {
		return remoteRetentionBlockedError()
	}
	if err = s.remoteRetentionCapability(ctx); err != nil {
		return err
	}
	return s.recoverRemoteRetention(ctx)
}

// A crash before journal commit leaves no remote intent, only private staging.
// Work-root ownership excludes a second operator; startup precedes remote work.
func (s *annotationService) cleanupUnjournaledRetention(ctx context.Context) error {
	pending, err := s.rt.store.pendingRemoteOperations(ctx)
	if err != nil {
		return err
	}
	keep := map[string]bool{}
	for _, op := range pending {
		keep[filepath.Clean(op.Directory)] = true
	}
	entries, err := os.ReadDir(s.rt.cfg.WorkRoot)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "remote-retention-") {
			continue
		}
		dir := filepath.Join(s.rt.cfg.WorkRoot, entry.Name())
		if keep[filepath.Clean(dir)] {
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}

func (s *annotationService) resumeWholeMeetingRetirement(ctx context.Context, op remoteRetentionOperation) error {
	if logicalMeetingName(op.Name) != op.Name || op.Name == "" || op.Action != "retire" {
		return fmt.Errorf("invalid retirement intent")
	}
	if err := retentionLeafPath(op.Source); err != nil {
		return err
	}
	m, ok, err := s.rt.store.meetingLifecycle(ctx, op.Name)
	if err != nil {
		return err
	}
	if !ok || m.FileID != op.FileID || m.Path != op.Source {
		return fmt.Errorf("retirement identity conflicts with lifecycle")
	}
	return s.resumeRemoteRetirement(ctx, op)
}
