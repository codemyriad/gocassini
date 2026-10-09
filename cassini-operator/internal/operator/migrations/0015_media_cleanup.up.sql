-- Created with admission. The terminal job row activates this obligation in the
-- same commit as success/failure; no best-effort queue handoff is required.
CREATE TABLE media_cleanup (
 job_id TEXT PRIMARY KEY REFERENCES jobs(id),
 status TEXT NOT NULL DEFAULT 'waiting',
 last_error TEXT NOT NULL DEFAULT '',
 completed_at TEXT NOT NULL DEFAULT '',
 failures INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0
);
