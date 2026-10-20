-- Older workers requeue every unknown state; keep unconfirmed uploads out.
UPDATE import_jobs SET state='failed', error='Upload tracking was rolled back. Add the file again if its upload was incomplete.' WHERE state IN ('uploading','upload_failed','staged');
DROP INDEX import_jobs_staged_path;
ALTER TABLE import_jobs DROP COLUMN cleared_at;
ALTER TABLE import_jobs DROP COLUMN staging_state;
ALTER TABLE import_jobs DROP COLUMN expected_bytes;
