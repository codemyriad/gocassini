ALTER TABLE capture_expectations RENAME TO capture_expectations_sessions;
CREATE TABLE capture_expectations (
 job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
 owner TEXT NOT NULL,
 call_start_ms INTEGER NOT NULL,
 call_end_ms INTEGER NOT NULL,
 status TEXT NOT NULL CHECK (status IN ('recording','uploading')),
 updated_at TEXT NOT NULL,
 PRIMARY KEY(job_id,owner,call_start_ms)
);
INSERT INTO capture_expectations SELECT job_id,owner,call_start_ms,MAX(call_end_ms),
 CASE WHEN MAX(status='uploading') THEN 'uploading' ELSE 'recording' END,MAX(updated_at)
 FROM capture_expectations_sessions GROUP BY job_id,owner,call_start_ms;
DROP TABLE capture_expectations_sessions;
