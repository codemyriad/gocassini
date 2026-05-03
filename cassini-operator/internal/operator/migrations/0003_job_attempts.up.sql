ALTER TABLE jobs ADD COLUMN current_attempt_number INTEGER NOT NULL DEFAULT 1;
ALTER TABLE jobs ADD COLUMN rerun_count INTEGER NOT NULL DEFAULT 0;

CREATE TABLE job_attempts (
  job_id TEXT NOT NULL,
  attempt_number INTEGER NOT NULL,
  trigger_kind TEXT NOT NULL,
  request_json TEXT NOT NULL,
  stage TEXT NOT NULL,
  state TEXT NOT NULL,
  artifact_run_path TEXT,
  artifact_meeting_path TEXT,
  artifact_site_path TEXT,
  error TEXT,
  stop_reason TEXT,
  stop_requested_at TEXT,
  stop_signal_sent_at TEXT,
  record_exit_code INTEGER,
  record_stop_detail TEXT,
  record_log_path TEXT,
  build_log_path TEXT,
  publish_log_path TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  record_queued_at TEXT,
  record_started_at TEXT,
  record_finished_at TEXT,
  build_queued_at TEXT,
  build_started_at TEXT,
  build_finished_at TEXT,
  publish_queued_at TEXT,
  publish_started_at TEXT,
  publish_finished_at TEXT,
  interrupted_at TEXT,
  completed_at TEXT,
  PRIMARY KEY (job_id, attempt_number),
  FOREIGN KEY (job_id) REFERENCES jobs(id) ON DELETE CASCADE
);

CREATE INDEX job_attempts_job_attempt_desc
  ON job_attempts(job_id, attempt_number DESC);

CREATE INDEX job_attempts_created_desc
  ON job_attempts(created_at DESC, job_id, attempt_number DESC);

INSERT INTO job_attempts (
  job_id, attempt_number, trigger_kind, request_json,
  stage, state,
  artifact_run_path, artifact_meeting_path, artifact_site_path,
  error, stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
  created_at, updated_at,
  record_queued_at, record_started_at, record_finished_at,
  build_queued_at, build_started_at, build_finished_at,
  publish_queued_at, publish_started_at, publish_finished_at,
  interrupted_at, completed_at
)
SELECT
  id, 1, 'initial', request_json,
  stage, state,
  artifact_run_path, artifact_meeting_path, artifact_site_path,
  error, stop_reason, stop_requested_at, stop_signal_sent_at, record_exit_code, record_stop_detail,
  created_at, updated_at,
  record_queued_at, record_started_at, record_finished_at,
  build_queued_at, build_started_at, build_finished_at,
  publish_queued_at, publish_started_at, publish_finished_at,
  interrupted_at, completed_at
FROM jobs;
