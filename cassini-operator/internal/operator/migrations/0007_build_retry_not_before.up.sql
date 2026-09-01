ALTER TABLE jobs ADD COLUMN build_retry_not_before TEXT;
ALTER TABLE job_attempts ADD COLUMN build_retry_not_before TEXT;
ALTER TABLE jobs ADD COLUMN build_deferral_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE job_attempts ADD COLUMN build_deferral_count INTEGER NOT NULL DEFAULT 0;

-- Older releases wrote RFC3339Nano with variable-width fractional seconds.
-- Normalize every timestamp used for lexical ordering before new fixed-width
-- writes mix with pre-upgrade RFC3339Nano values. Values are UTC (`Z`); these
-- expressions preserve all fractional digits while right-padding to nine.
UPDATE jobs
SET created_at = CASE
      WHEN substr(created_at, 20, 1) = '.' THEN
        substr(created_at, 1, 20) ||
        substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(created_at, 1, 19) || '.000000000Z'
    END,
    build_queued_at = CASE
      WHEN build_queued_at IS NULL THEN NULL
      WHEN substr(build_queued_at, 20, 1) = '.' THEN
        substr(build_queued_at, 1, 20) ||
        substr(substr(build_queued_at, 21, length(build_queued_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(build_queued_at, 1, 19) || '.000000000Z'
    END,
    seal_queued_at = CASE
      WHEN seal_queued_at IS NULL THEN NULL
      WHEN substr(seal_queued_at, 20, 1) = '.' THEN
        substr(seal_queued_at, 1, 20) ||
        substr(substr(seal_queued_at, 21, length(seal_queued_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(seal_queued_at, 1, 19) || '.000000000Z'
    END,
    publish_queued_at = CASE
      WHEN publish_queued_at IS NULL THEN NULL
      WHEN substr(publish_queued_at, 20, 1) = '.' THEN
        substr(publish_queued_at, 1, 20) ||
        substr(substr(publish_queued_at, 21, length(publish_queued_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(publish_queued_at, 1, 19) || '.000000000Z'
    END;

UPDATE job_attempts
SET created_at = CASE
      WHEN substr(created_at, 20, 1) = '.' THEN
        substr(created_at, 1, 20) ||
        substr(substr(created_at, 21, length(created_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(created_at, 1, 19) || '.000000000Z'
    END,
    build_queued_at = CASE
      WHEN build_queued_at IS NULL THEN NULL
      WHEN substr(build_queued_at, 20, 1) = '.' THEN
        substr(build_queued_at, 1, 20) ||
        substr(substr(build_queued_at, 21, length(build_queued_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(build_queued_at, 1, 19) || '.000000000Z'
    END,
    seal_queued_at = CASE
      WHEN seal_queued_at IS NULL THEN NULL
      WHEN substr(seal_queued_at, 20, 1) = '.' THEN
        substr(seal_queued_at, 1, 20) ||
        substr(substr(seal_queued_at, 21, length(seal_queued_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(seal_queued_at, 1, 19) || '.000000000Z'
    END,
    publish_queued_at = CASE
      WHEN publish_queued_at IS NULL THEN NULL
      WHEN substr(publish_queued_at, 20, 1) = '.' THEN
        substr(publish_queued_at, 1, 20) ||
        substr(substr(publish_queued_at, 21, length(publish_queued_at) - 21) || '000000000', 1, 9) || 'Z'
      ELSE substr(publish_queued_at, 1, 19) || '.000000000Z'
    END;

-- Retry eligibility is filtered after this FIFO scan. Putting the nullable
-- retry deadline before build_queued_at prevents SQLite from satisfying the
-- ORDER BY and creates a temporary B-tree on every dispatcher pass.
CREATE INDEX jobs_build_queue_order
  ON jobs(stage, state, build_queued_at, id);
