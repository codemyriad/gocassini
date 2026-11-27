ALTER TABLE jobs DROP COLUMN summary_sent_queued_at;
ALTER TABLE jobs DROP COLUMN summary_sent_reason;
ALTER TABLE jobs DROP COLUMN summary_sent_host;

DROP INDEX IF EXISTS event_outbox_job;
ALTER TABLE event_outbox DROP COLUMN notice_reason;
ALTER TABLE event_outbox DROP COLUMN notice_host;
ALTER TABLE event_outbox DROP COLUMN job_id;
