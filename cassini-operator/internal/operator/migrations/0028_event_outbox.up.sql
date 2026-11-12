-- Signed outbound lifecycle events (issue #474).
--
-- Events are written here before delivery, so a webhook receiver being down
-- or the operator restarting does not lose a recording's lifecycle. The
-- worker claims the oldest due row, posts it, and records success or failure.
-- At-least-once delivery: a crash between POST and mark-delivered redelivers,
-- which receivers deduplicate by the delivery id.
CREATE TABLE event_outbox (
  id              TEXT NOT NULL PRIMARY KEY,
  type            TEXT NOT NULL,
  payload         TEXT NOT NULL,
  created_at      TEXT NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT NOT NULL,
  delivered_at    TEXT,
  failed_at       TEXT,
  last_error      TEXT
);

CREATE INDEX event_outbox_due ON event_outbox (next_attempt_at)
  WHERE delivered_at IS NULL AND failed_at IS NULL;
