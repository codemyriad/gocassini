DROP INDEX IF EXISTS event_outbox_receiver;
DROP INDEX IF EXISTS event_outbox_owed;
CREATE INDEX event_outbox_due ON event_outbox (next_attempt_at)
  WHERE delivered_at IS NULL AND failed_at IS NULL;
ALTER TABLE event_outbox DROP COLUMN last_error_at;
ALTER TABLE event_outbox DROP COLUMN receiver_id;
