package operator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type remoteRetentionOperation struct {
	Name      string `json:"name"`
	Action    string `json:"action"`
	Source    string `json:"source"`
	FileID    int64  `json:"fileId"`
	InputETag string `json:"inputETag"`
	Revision  int    `json:"revision"`
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
		byName[name] = meetingLifecycle{Name: name, FileID: id, Path: ncRecordingsRoot + "/meetings/" + names[id], State: "active", Anchor: anchor.UTC().Format(time.RFC3339Nano), AnchorSource: "recording-completed"}
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
	s.rt.retention.mu.Lock()
	forever := s.rt.retention.settings.Nextcloud.Meetings.Forever
	s.rt.retention.mu.Unlock()
	if forever {
		pending, err := s.rt.store.pendingRemoteOperations(ctx)
		if err != nil || len(pending) == 0 {
			return err
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
		if effect.Action != "retire" {
			continue
		}
		if err := s.prepareRemoteRetention(ctx, m, "retire", settings.Revision); err != nil {
			failures = errors.Join(failures, fmt.Errorf("retire %s: %w", m.Name, err))
		}
	}
	return failures
}
func (s *annotationService) prepareRemoteRetention(ctx context.Context, m meetingLifecycle, action string, revision int) error {
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
		return err
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

func (s *annotationService) resumeRemoteRetention(ctx context.Context, op remoteRetentionOperation) error {
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
func (s *annotationService) finishRemoteRetention(ctx context.Context, op remoteRetentionOperation) error {
	op.InputETag = ""
	return s.rt.store.saveRemoteOperation(ctx, op, "completed")
}
func (s *annotationService) recoverRemoteRetentionAtStartup(ctx context.Context) error {
	pending, err := s.rt.store.pendingRemoteOperations(ctx)
	if err != nil || len(pending) == 0 {
		return err
	}
	if err := s.remoteRetentionCapability(ctx); err != nil {
		return err
	}
	return s.recoverRemoteRetention(ctx)
}
