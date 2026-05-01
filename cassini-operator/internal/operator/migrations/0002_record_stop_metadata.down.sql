DROP INDEX IF EXISTS jobs_created_desc;
CREATE TABLE jobs_legacy (
  id TEXT PRIMARY KEY NOT NULL,
  provider TEXT NOT NULL,
  request_json TEXT NOT NULL,
  stage TEXT NOT NULL,
  state TEXT NOT NULL,
  artifact_run_path TEXT,
  artifact_meeting_path TEXT,
  artifact_site_path TEXT,
  error TEXT,
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
  completed_at TEXT
);
INSERT INTO jobs_legacy (
  id, provider, request_json, stage, state,
  artifact_run_path, artifact_meeting_path, artifact_site_path, error,
  created_at, updated_at,
  record_queued_at, record_started_at, record_finished_at,
  build_queued_at, build_started_at, build_finished_at,
  publish_queued_at, publish_started_at, publish_finished_at,
  interrupted_at, completed_at
)
SELECT
  id, provider, request_json, stage, state,
  artifact_run_path, artifact_meeting_path, artifact_site_path, error,
  created_at, updated_at,
  record_queued_at, record_started_at, record_finished_at,
  build_queued_at, build_started_at, build_finished_at,
  publish_queued_at, publish_started_at, publish_finished_at,
  interrupted_at, completed_at
FROM jobs;
DROP TABLE jobs;
ALTER TABLE jobs_legacy RENAME TO jobs;
CREATE INDEX IF NOT EXISTS jobs_created_desc ON jobs(created_at DESC, id DESC);
