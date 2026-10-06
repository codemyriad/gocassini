CREATE TABLE meeting_format (
 job_id TEXT NOT NULL REFERENCES jobs(id),
 attempt_number INTEGER NOT NULL,
 format TEXT NOT NULL CHECK(format IN ('opus','json')),
 PRIMARY KEY(job_id,attempt_number)
);
