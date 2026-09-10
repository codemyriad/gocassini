ALTER TABLE capture_expectations RENAME TO capture_expectations_legacy;
CREATE TABLE capture_expectations (
 job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 owner TEXT NOT NULL,
 call_start_ms INTEGER NOT NULL,
 call_end_ms INTEGER NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('recording','uploading')),
 updated_at TEXT NOT NULL,
 capture_id TEXT NOT NULL DEFAULT '',
 session_id TEXT NOT NULL DEFAULT '',
 PRIMARY KEY(job_id,owner,call_start_ms,capture_id)
);
INSERT INTO capture_expectations(job_id,owner,call_start_ms,call_end_ms,status,updated_at)
 SELECT job_id,owner,call_start_ms,call_end_ms,status,updated_at FROM capture_expectations_legacy;
DROP TABLE capture_expectations_legacy;
