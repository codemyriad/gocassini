package operator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var ErrJobNotEligibleForRerun = errors.New("job is not eligible for rerun")

type JobAttempt struct {
	JobID               string  `json:"job_id"`
	AttemptNumber       int     `json:"attempt_number"`
	TriggerKind         string  `json:"trigger_kind"`
	RequestJSON         string  `json:"request_json"`
	Stage               string  `json:"stage"`
	State               string  `json:"state"`
	ArtifactRunPath     *string `json:"artifact_run_path"`
	ArtifactMeetingPath *string `json:"artifact_meeting_path"`
	ArtifactSitePath    *string `json:"artifact_site_path"`
	Error               *string `json:"error"`
	StopReason          *string `json:"stop_reason"`
	StopRequestedAt     *string `json:"stop_requested_at"`
	StopSignalSentAt    *string `json:"stop_signal_sent_at"`
	RecordExitCode      *int    `json:"record_exit_code"`
	RecordStopDetail    *string `json:"record_stop_detail"`
	RecordLogPath       *string `json:"record_log_path"`
	BuildLogPath        *string `json:"build_log_path"`
	PublishLogPath      *string `json:"publish_log_path"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
	RecordQueuedAt      *string `json:"record_queued_at"`
	RecordStartedAt     *string `json:"record_started_at"`
	RecordFinishedAt    *string `json:"record_finished_at"`
	BuildQueuedAt       *string `json:"build_queued_at"`
	BuildStartedAt      *string `json:"build_started_at"`
	BuildFinishedAt     *string `json:"build_finished_at"`
	PublishQueuedAt     *string `json:"publish_queued_at"`
	PublishStartedAt    *string `json:"publish_started_at"`
	PublishFinishedAt   *string `json:"publish_finished_at"`
	InterruptedAt       *string `json:"interrupted_at"`
	CompletedAt         *string `json:"completed_at"`
}

func insertInitialAttemptTx(tx *sql.Tx, job Job) error {
	_, err := tx.Exec(`
INSERT INTO job_attempts (
  job_id, attempt_number, trigger_kind, request_json,
  stage, state,
  created_at, updated_at, record_queued_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		1,
		"initial",
		job.RequestJSON,
		job.Stage,
		job.State,
		job.CreatedAt,
		job.UpdatedAt,
		stringOrNil(job.RecordQueuedAt),
	)
	if err != nil {
		return fmt.Errorf("insert initial attempt: %w", err)
	}
	return nil
}

func currentAttemptNumberTx(ctx context.Context, tx *sql.Tx, jobID string) (int, error) {
	var attemptNumber int
	if err := tx.QueryRowContext(ctx, `
SELECT current_attempt_number
FROM jobs
WHERE id = ?`, jobID).Scan(&attemptNumber); err != nil {
		return 0, fmt.Errorf("query current attempt number: %w", err)
	}
	return attemptNumber, nil
}

func (s *Store) SetAttemptStageLogPath(ctx context.Context, jobID string, attemptNumber int, stage, path string) error {
	var column string
	switch stage {
	case "record":
		column = "record_log_path"
	case "build":
		column = "build_log_path"
	case "publish":
		column = "publish_log_path"
	default:
		return fmt.Errorf("unknown log stage %q", stage)
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf(`
UPDATE job_attempts
SET %s = ?, updated_at = ?
WHERE job_id = ? AND attempt_number = ?`, column), path, nowUTCString(), jobID, attemptNumber); err != nil {
		return fmt.Errorf("update attempt %s log path: %w", stage, err)
	}
	s.emitStateChange(ctx, "attempt.updated", jobID, attemptNumber)
	return nil
}

func (s *Store) QueueRerunAttempt(ctx context.Context, job Job, queuedAt string) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, fmt.Errorf("begin rerun attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var state string
	var requestJSON string
	var currentAttemptNumber int
	var artifactRunPath sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT state, request_json, current_attempt_number, artifact_run_path
FROM jobs
WHERE id = ?`, job.ID).Scan(&state, &requestJSON, &currentAttemptNumber, &artifactRunPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, err
		}
		return Job{}, fmt.Errorf("load job for rerun: %w", err)
	}
	if state != "failed" && state != "succeeded" && state != "interrupted" {
		return Job{}, ErrJobNotEligibleForRerun
	}

	readyRunPath := strings.TrimSpace(artifactRunPath.String)
	if readyRunPath == "" {
		return Job{}, ErrJobNotEligibleForRerun
	}
	manifest, err := readRunManifest(filepath.Join(readyRunPath, "cassini.json"))
	if err != nil || manifest.State != bundleStateReady || manifest.Stage != "ready" {
		return Job{}, ErrJobNotEligibleForRerun
	}

	nextAttemptNumber := currentAttemptNumber + 1
	if _, err := tx.ExecContext(ctx, `
INSERT INTO job_attempts (
  job_id, attempt_number, trigger_kind, request_json,
  stage, state,
  artifact_run_path,
  created_at, updated_at, build_queued_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		job.ID,
		nextAttemptNumber,
		"rerun",
		requestJSON,
		"build",
		"queued",
		readyRunPath,
		queuedAt,
		queuedAt,
		queuedAt,
	); err != nil {
		return Job{}, fmt.Errorf("insert rerun attempt: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
UPDATE jobs
SET stage = ?, state = ?,
    current_attempt_number = ?, rerun_count = ?,
    artifact_run_path = ?,
    error = NULL,
    updated_at = ?,
    build_queued_at = ?, build_started_at = NULL, build_finished_at = NULL,
    publish_queued_at = NULL, publish_started_at = NULL, publish_finished_at = NULL,
    interrupted_at = NULL, completed_at = NULL
WHERE id = ?`,
		"build", "queued",
		nextAttemptNumber, nextAttemptNumber-1,
		readyRunPath,
		queuedAt,
		queuedAt,
		job.ID,
	); err != nil {
		return Job{}, fmt.Errorf("update job summary for rerun: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Job{}, fmt.Errorf("commit rerun attempt: %w", err)
	}
	s.emitStateChange(ctx, "job.updated", job.ID, nextAttemptNumber)
	return s.GetJob(ctx, job.ID)
}

func (s *Store) ListJobAttempts(ctx context.Context, jobID string) ([]JobAttempt, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT job_id, attempt_number, trigger_kind, request_json,
       stage, state,
       artifact_run_path, artifact_meeting_path, artifact_site_path,
       error, stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
       record_log_path, build_log_path, publish_log_path,
       created_at, updated_at,
       record_queued_at, record_started_at, record_finished_at,
       build_queued_at, build_started_at, build_finished_at,
       publish_queued_at, publish_started_at, publish_finished_at,
       interrupted_at, completed_at
FROM job_attempts
WHERE job_id = ?
ORDER BY attempt_number DESC`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query job attempts: %w", err)
	}
	defer rows.Close()

	var attempts []JobAttempt
	for rows.Next() {
		attempt, err := scanJobAttempt(rows)
		if err != nil {
			return nil, err
		}
		attempts = append(attempts, attempt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate job attempts: %w", err)
	}
	if attempts == nil {
		attempts = []JobAttempt{}
	}
	return attempts, nil
}

func scanJobAttempt(scanner rowScanner) (JobAttempt, error) {
	var attempt JobAttempt
	var artifactRunPath sql.NullString
	var artifactMeetingPath sql.NullString
	var artifactSitePath sql.NullString
	var attemptError sql.NullString
	var stopReason sql.NullString
	var stopRequestedAt sql.NullString
	var stopSignalSentAt sql.NullString
	var recordExitCode sql.NullInt64
	var recordStopDetail sql.NullString
	var recordLogPath sql.NullString
	var buildLogPath sql.NullString
	var publishLogPath sql.NullString
	var recordQueuedAt sql.NullString
	var recordStartedAt sql.NullString
	var recordFinishedAt sql.NullString
	var buildQueuedAt sql.NullString
	var buildStartedAt sql.NullString
	var buildFinishedAt sql.NullString
	var publishQueuedAt sql.NullString
	var publishStartedAt sql.NullString
	var publishFinishedAt sql.NullString
	var interruptedAt sql.NullString
	var completedAt sql.NullString

	err := scanner.Scan(
		&attempt.JobID,
		&attempt.AttemptNumber,
		&attempt.TriggerKind,
		&attempt.RequestJSON,
		&attempt.Stage,
		&attempt.State,
		&artifactRunPath,
		&artifactMeetingPath,
		&artifactSitePath,
		&attemptError,
		&stopReason,
		&stopRequestedAt,
		&stopSignalSentAt,
		&recordExitCode,
		&recordStopDetail,
		&recordLogPath,
		&buildLogPath,
		&publishLogPath,
		&attempt.CreatedAt,
		&attempt.UpdatedAt,
		&recordQueuedAt,
		&recordStartedAt,
		&recordFinishedAt,
		&buildQueuedAt,
		&buildStartedAt,
		&buildFinishedAt,
		&publishQueuedAt,
		&publishStartedAt,
		&publishFinishedAt,
		&interruptedAt,
		&completedAt,
	)
	if err != nil {
		return JobAttempt{}, fmt.Errorf("scan job attempt: %w", err)
	}

	attempt.ArtifactRunPath = nullableStringPtr(artifactRunPath)
	attempt.ArtifactMeetingPath = nullableStringPtr(artifactMeetingPath)
	attempt.ArtifactSitePath = nullableStringPtr(artifactSitePath)
	attempt.Error = nullableStringPtr(attemptError)
	attempt.StopReason = nullableStringPtr(stopReason)
	attempt.StopRequestedAt = nullableStringPtr(stopRequestedAt)
	attempt.StopSignalSentAt = nullableStringPtr(stopSignalSentAt)
	attempt.RecordExitCode = nullableIntPtr(recordExitCode)
	attempt.RecordStopDetail = nullableStringPtr(recordStopDetail)
	attempt.RecordLogPath = nullableStringPtr(recordLogPath)
	attempt.BuildLogPath = nullableStringPtr(buildLogPath)
	attempt.PublishLogPath = nullableStringPtr(publishLogPath)
	attempt.RecordQueuedAt = nullableStringPtr(recordQueuedAt)
	attempt.RecordStartedAt = nullableStringPtr(recordStartedAt)
	attempt.RecordFinishedAt = nullableStringPtr(recordFinishedAt)
	attempt.BuildQueuedAt = nullableStringPtr(buildQueuedAt)
	attempt.BuildStartedAt = nullableStringPtr(buildStartedAt)
	attempt.BuildFinishedAt = nullableStringPtr(buildFinishedAt)
	attempt.PublishQueuedAt = nullableStringPtr(publishQueuedAt)
	attempt.PublishStartedAt = nullableStringPtr(publishStartedAt)
	attempt.PublishFinishedAt = nullableStringPtr(publishFinishedAt)
	attempt.InterruptedAt = nullableStringPtr(interruptedAt)
	attempt.CompletedAt = nullableStringPtr(completedAt)
	return attempt, nil
}
