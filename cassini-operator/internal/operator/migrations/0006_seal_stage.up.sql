-- The seal stage (D-583). Packing the portable `.opus` used to be a detached,
-- best-effort goroutine started after publish was already queued; it is now a
-- stage of the job, and its success is what makes a job publishable.
ALTER TABLE jobs ADD COLUMN seal_queued_at TEXT;
ALTER TABLE jobs ADD COLUMN seal_started_at TEXT;
ALTER TABLE jobs ADD COLUMN seal_finished_at TEXT;
ALTER TABLE jobs ADD COLUMN artifact_opus_path TEXT;
ALTER TABLE jobs ADD COLUMN artifact_opus_sha256 TEXT;

ALTER TABLE job_attempts ADD COLUMN seal_queued_at TEXT;
ALTER TABLE job_attempts ADD COLUMN seal_started_at TEXT;
ALTER TABLE job_attempts ADD COLUMN seal_finished_at TEXT;
ALTER TABLE job_attempts ADD COLUMN artifact_opus_path TEXT;
ALTER TABLE job_attempts ADD COLUMN artifact_opus_sha256 TEXT;
ALTER TABLE job_attempts ADD COLUMN seal_log_path TEXT;

-- Upgrade path. A row queued for publish by the previous build has no sealed
-- `.opus`, and the publish worker after this change refuses to run without one.
-- Send those rows back one stage instead of stranding them: their `.meeting`
-- bundle is still on disk, so the seal worker packs it and the job continues.
-- Only queued rows move; a publish already running is left alone (the startup
-- sweep marks it interrupted, as it always has).
UPDATE jobs
SET stage = 'seal',
    state = 'queued',
    seal_queued_at = COALESCE(publish_queued_at, updated_at),
    publish_queued_at = NULL
WHERE stage = 'publish' AND state = 'queued';

UPDATE job_attempts
SET stage = 'seal',
    state = 'queued',
    seal_queued_at = COALESCE(publish_queued_at, updated_at),
    publish_queued_at = NULL
WHERE stage = 'publish' AND state = 'queued';
