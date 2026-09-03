-- Insight runs are their own pair of tables rather than a fifth stage on `jobs`
-- (D-720). A job is a recording moving through record -> build -> seal ->
-- publish, and both of the machines that drive it would mishandle an insight:
-- SetAttemptStageLogPath rejects any stage outside that four, and
-- MarkIncompleteJobsInterrupted would stamp a queued insight `interrupted` on
-- every operator restart. Neither touches these tables.

CREATE TABLE insight_runs (
  id TEXT PRIMARY KEY NOT NULL,
  created_by TEXT NOT NULL,
  status TEXT NOT NULL,
  workflow_id TEXT NOT NULL,
  workflow_version TEXT NOT NULL,
  workflow_sha256 TEXT NOT NULL,
  -- JSON arrays, in the order the request named them. The room ids are stored
  -- beside the meeting ids because an insight surfaces under every room it drew
  -- from (D-721), and rooms cannot be re-derived from a meeting id alone once a
  -- recording has been moved or removed.
  meeting_ids TEXT NOT NULL,
  room_ids TEXT NOT NULL,
  question TEXT NOT NULL DEFAULT '',
  -- What the last attempt actually resolved and produced, never what the
  -- request asked for: a retry re-resolves provider and model from current
  -- settings, so a stored provider is a record, not an input (D-720 §4).
  provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  document_path TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  attempt_number INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

-- The list surface is always "this caller's runs, newest first"; an insight is
-- never listed across callers, because the model call is made with the
-- instance's key over content only that caller could read.
CREATE INDEX insight_runs_created_by_created_desc
  ON insight_runs(created_by, created_at DESC, id DESC);

-- One row per attempt, echoing job_attempts: the run row is the current state,
-- these are the history. Workflow, sources and question are deliberately absent
-- — a retry holds all three fixed, so they belong to the run and only the
-- resolved endpoint and its outcome vary per attempt.
CREATE TABLE insight_run_attempts (
  run_id TEXT NOT NULL,
  attempt_number INTEGER NOT NULL,
  status TEXT NOT NULL,
  provider TEXT NOT NULL DEFAULT '',
  model TEXT NOT NULL DEFAULT '',
  document_path TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  finished_at TEXT,
  PRIMARY KEY (run_id, attempt_number),
  FOREIGN KEY (run_id) REFERENCES insight_runs(id) ON DELETE CASCADE
);

CREATE INDEX insight_run_attempts_run_attempt_desc
  ON insight_run_attempts(run_id, attempt_number DESC);
