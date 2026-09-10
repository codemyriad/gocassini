-- Durable per-browser announcements, bound to the server's recording job.
CREATE TABLE capture_expectations (
  job_id TEXT NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
  owner TEXT NOT NULL,
  call_start_ms INTEGER NOT NULL,
  call_end_ms INTEGER NOT NULL,
  status TEXT NOT NULL CHECK (status IN ('recording', 'uploading')),
  updated_at TEXT NOT NULL,
  PRIMARY KEY (job_id, owner, call_start_ms)
);
