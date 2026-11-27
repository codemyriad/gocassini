-- Who an outbox row is owed to, and when its last error was written (#488).
--
-- receiver_id: the key of the receiver URL a row was queued for; it is only
-- ever sent there. Rows from before have ''; the operator tags them with the
-- stored URL's key at startup, the URL 0.4.0 queued them for.
--
-- event_outbox_owed replaces event_outbox_due: the worker and the delivery
-- summary always ask for one receiver's rows.
ALTER TABLE event_outbox ADD COLUMN receiver_id TEXT NOT NULL DEFAULT '';
ALTER TABLE event_outbox ADD COLUMN last_error_at TEXT;
DROP INDEX IF EXISTS event_outbox_due;
CREATE INDEX event_outbox_owed ON event_outbox (receiver_id, next_attempt_at)
  WHERE delivered_at IS NULL AND failed_at IS NULL;
CREATE INDEX event_outbox_receiver ON event_outbox (receiver_id);

-- One-time cleanup of what 0.4.0 left: a delivered row is not also given up,
-- and a finished row keeps no payload.
UPDATE event_outbox SET failed_at = NULL WHERE delivered_at IS NOT NULL;
UPDATE event_outbox SET payload = '' WHERE delivered_at IS NOT NULL OR failed_at IS NOT NULL;
