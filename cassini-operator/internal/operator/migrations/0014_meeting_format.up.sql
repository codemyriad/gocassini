CREATE TABLE meeting_format (
 job_id TEXT NOT NULL REFERENCES jobs(id),
 attempt_number INTEGER NOT NULL,
 format TEXT NOT NULL CHECK(format IN ('opus','json')),
 PRIMARY KEY(job_id,attempt_number)
);

-- Upgrade existing jobs without changing the file identity of queued, failed,
-- or partially delivered attempts. Only newly admitted meetings opt into JSON.
INSERT INTO meeting_format(job_id, attempt_number, format)
SELECT job_id, attempt_number, 'opus' FROM job_attempts;
