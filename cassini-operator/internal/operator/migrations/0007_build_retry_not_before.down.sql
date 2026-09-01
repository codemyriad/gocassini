DROP INDEX IF EXISTS jobs_build_queue_order;
DROP INDEX IF EXISTS jobs_build_retry_eligible;
ALTER TABLE job_attempts DROP COLUMN build_deferral_count;
ALTER TABLE jobs DROP COLUMN build_deferral_count;
ALTER TABLE job_attempts DROP COLUMN build_retry_not_before;
ALTER TABLE jobs DROP COLUMN build_retry_not_before;
