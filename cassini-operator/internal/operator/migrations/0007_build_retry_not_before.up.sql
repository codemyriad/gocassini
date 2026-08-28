ALTER TABLE jobs ADD COLUMN build_retry_not_before TEXT;
ALTER TABLE job_attempts ADD COLUMN build_retry_not_before TEXT;

CREATE INDEX jobs_build_retry_eligible
  ON jobs(stage, state, build_retry_not_before, build_queued_at);
