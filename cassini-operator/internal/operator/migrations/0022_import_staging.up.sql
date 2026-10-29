ALTER TABLE import_jobs ADD COLUMN expected_bytes INTEGER NOT NULL DEFAULT 0;
ALTER TABLE import_jobs ADD COLUMN staging_state TEXT NOT NULL DEFAULT '';
ALTER TABLE import_jobs ADD COLUMN cleared_at TEXT NOT NULL DEFAULT '';
CREATE INDEX import_jobs_staged_path ON import_jobs(created_by, staged_path);
