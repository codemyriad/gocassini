-- Send every seal row back to the stage the previous build understands. A
-- `seal/queued` row was `publish/queued` before the up-migration; a seal that
-- failed or was interrupted is downgraded to the same shape so the old publish
-- worker (which resolves its own input from current/) can carry it.
UPDATE job_attempts
SET stage = 'publish',
    state = 'queued',
    publish_queued_at = COALESCE(seal_queued_at, updated_at)
WHERE stage = 'seal';

UPDATE jobs
SET stage = 'publish',
    state = 'queued',
    publish_queued_at = COALESCE(seal_queued_at, updated_at)
WHERE stage = 'seal';

ALTER TABLE job_attempts DROP COLUMN seal_log_path;
ALTER TABLE job_attempts DROP COLUMN artifact_opus_sha256;
ALTER TABLE job_attempts DROP COLUMN artifact_opus_path;
ALTER TABLE job_attempts DROP COLUMN seal_finished_at;
ALTER TABLE job_attempts DROP COLUMN seal_started_at;
ALTER TABLE job_attempts DROP COLUMN seal_queued_at;

ALTER TABLE jobs DROP COLUMN artifact_opus_sha256;
ALTER TABLE jobs DROP COLUMN artifact_opus_path;
ALTER TABLE jobs DROP COLUMN seal_finished_at;
ALTER TABLE jobs DROP COLUMN seal_started_at;
ALTER TABLE jobs DROP COLUMN seal_queued_at;
