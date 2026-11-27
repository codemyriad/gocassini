-- Where a meeting's summary was sent (#532; docs/events.md "What people see").
-- A summary.completed row records its job, the receiver's host and the
-- admin's reason when queued; a 2xx copies them onto the job.
ALTER TABLE event_outbox ADD COLUMN job_id TEXT NOT NULL DEFAULT '';
ALTER TABLE event_outbox ADD COLUMN notice_host TEXT;
ALTER TABLE event_outbox ADD COLUMN notice_reason TEXT;
CREATE INDEX event_outbox_job ON event_outbox (job_id) WHERE job_id <> '';

ALTER TABLE jobs ADD COLUMN summary_sent_host TEXT;
ALTER TABLE jobs ADD COLUMN summary_sent_reason TEXT;
ALTER TABLE jobs ADD COLUMN summary_sent_queued_at TEXT;
