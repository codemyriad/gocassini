package operator

import (
	"context"
	"fmt"
	"strings"
)

// The seal stage sits between build and publish (D-583).
//
// Packing the portable `.opus` used to be a detached goroutine started *after*
// the job was handed to the publish worker. That made the artifact Cassini calls
// canonical optional (a pack could fail, be killed at shutdown, or be overtaken
// by another attempt's pack writing the same mutable path) and forced the
// publish worker to prefer the `.meeting` bundle instead.
//
//	build/running ──MarkSealQueued──▶ seal/queued ──ClaimSealRunning──▶ seal/running
//	                                      ▲                                  │
//	              requeue dispatcher ─────┘                MarkSealSucceeded │ MarkSealFailed
//	              (survives restart)                                         ▼
//	                                                  publish/queued    done/failed
//
// The two writes that matter:
//
//   - MarkSealSucceeded is the *only* way a job becomes publish/queued, and it
//     records the sealed artifact's path and digest in the same transaction. A
//     publish therefore cannot exist without a sealed `.opus` behind it.
//   - The attempt row carries the attempt-scoped immutable path
//     (runs/<job>--attempt-NNN.opus); the jobs row carries the canonical
//     promotion (current/<job>.opus). That is the same split every other stage
//     uses, and it is what stops two attempts from claiming one artifact.

// MarkSealQueued hands a built job to the seal worker. The meeting paths travel
// with it because packing reads the `.meeting` bundle, and because a restart
// rebuilds the seal task from these columns.
func (s *Store) MarkSealQueued(ctx context.Context, id, jobArtifactMeetingPath, attemptArtifactMeetingPath, queuedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin seal queued update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, seal_queued_at = ?, completed_at = NULL, error = NULL
WHERE id = ?`, "seal", "queued", jobArtifactMeetingPath, queuedAt, queuedAt, queuedAt, id); err != nil {
		return fmt.Errorf("update seal queued: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_meeting_path = ?, updated_at = ?, build_finished_at = ?, seal_queued_at = ?, completed_at = NULL, error = NULL
WHERE job_id = ? AND attempt_number = ?`, "seal", "queued", attemptArtifactMeetingPath, queuedAt, queuedAt, queuedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt seal queued: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seal queued update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

// ClaimSealRunning transitions a job from seal/queued to seal/running,
// returning false (without error) when the job is not claimable — a duplicate
// delivery (direct enqueue plus a requeue-dispatcher re-scan) or a state change
// since queueing. The conditional UPDATE is the claim; SQLite serializes it, so
// exactly one worker wins. Same shape as ClaimBuildRunning (D-367).
func (s *Store) ClaimSealRunning(ctx context.Context, id, startedAt string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin seal running update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
UPDATE jobs
SET state = ?, updated_at = ?, seal_started_at = ?
WHERE id = ? AND stage = ? AND state = ?`, "running", startedAt, startedAt, id, "seal", "queued")
	if err != nil {
		return false, fmt.Errorf("update seal running: %w", err)
	}
	claimed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("seal running rows affected: %w", err)
	}
	if claimed == 0 {
		return false, nil
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, updated_at = ?, seal_started_at = ?
WHERE job_id = ? AND attempt_number = ?`, "seal", "running", startedAt, startedAt, id, attemptNumber); err != nil {
		return false, fmt.Errorf("update attempt seal running: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit seal running update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return true, nil
}

// ListQueuedSealTasks returns the durable seal backlog: jobs whose row says
// seal/queued with a meeting bundle to pack. The requeue dispatcher feeds these
// to the seal worker when the in-memory channel dropped them (full queue) or
// never saw them (operator restart) (D-367).
//
// The attempt's own meeting path is what gets packed — sealing must produce the
// artifact of *this* attempt, not of whatever currently sits in current/.
func (s *Store) ListQueuedSealTasks(ctx context.Context) ([]sealTask, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT jobs.id, jobs.current_attempt_number, job_attempts.artifact_meeting_path
FROM jobs
JOIN job_attempts
  ON job_attempts.job_id = jobs.id
 AND job_attempts.attempt_number = jobs.current_attempt_number
WHERE jobs.stage = 'seal' AND jobs.state = 'queued' AND job_attempts.artifact_meeting_path IS NOT NULL
ORDER BY jobs.seal_queued_at ASC, jobs.id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query queued seal jobs: %w", err)
	}
	defer rows.Close()

	var tasks []sealTask
	for rows.Next() {
		var task sealTask
		if err := rows.Scan(&task.JobID, &task.AttemptNumber, &task.ArtifactMeetingPath); err != nil {
			return nil, fmt.Errorf("scan queued seal job: %w", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate queued seal jobs: %w", err)
	}
	return tasks, nil
}

// MarkSealSucceeded records the sealed artifact and queues the publish in one
// transaction. Splitting them would allow a crash to leave a publish/queued row
// with no recorded artifact, which is precisely the state D-583 exists to make
// unreachable.
func (s *Store) MarkSealSucceeded(ctx context.Context, id, jobArtifactOpusPath, attemptArtifactOpusPath, opusSHA256, finishedAt string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin seal success update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, artifact_opus_path = ?, artifact_opus_sha256 = ?, updated_at = ?, seal_finished_at = ?, publish_queued_at = ?, completed_at = NULL, error = NULL
WHERE id = ?`, "publish", "queued", jobArtifactOpusPath, opusSHA256, finishedAt, finishedAt, finishedAt, id); err != nil {
		return fmt.Errorf("update seal success: %w", err)
	}
	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_opus_path = ?, artifact_opus_sha256 = ?, updated_at = ?, seal_finished_at = ?, publish_queued_at = ?, completed_at = NULL, error = NULL
WHERE job_id = ? AND attempt_number = ?`, "publish", "queued", attemptArtifactOpusPath, opusSHA256, finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt seal success: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seal success update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}

// MarkSealFailed ends the job at done/failed. A rerun is the retry path, as it
// is for a failed build or publish; the run bundle in current/ is what it
// rebuilds from, so nothing is lost.
func (s *Store) MarkSealFailed(ctx context.Context, id, attemptArtifactOpusPath, errText, finishedAt string) error {
	attemptArtifactOpusPath = strings.TrimSpace(attemptArtifactOpusPath)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin seal failure update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	attemptNumber, err := currentAttemptNumberTx(ctx, tx, id)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?, error = ?, updated_at = ?, seal_finished_at = ?, completed_at = ?
WHERE id = ?`, "done", "failed", strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id); err != nil {
		return fmt.Errorf("update seal failure: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE job_attempts
SET stage = ?, state = ?, artifact_opus_path = ?, error = ?, updated_at = ?, seal_finished_at = ?, completed_at = ?
WHERE job_id = ? AND attempt_number = ?`, "done", "failed", nullableString(attemptArtifactOpusPath), strings.TrimSpace(errText), finishedAt, finishedAt, finishedAt, id, attemptNumber); err != nil {
		return fmt.Errorf("update attempt seal failure: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit seal failure update: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", id, attemptNumber)
	return nil
}
