ALTER TABLE jobs ADD COLUMN stop_reason TEXT;
ALTER TABLE jobs ADD COLUMN stop_requested_at TEXT;
ALTER TABLE jobs ADD COLUMN stop_signal_sent_at TEXT;
ALTER TABLE jobs ADD COLUMN record_exit_code INTEGER;
ALTER TABLE jobs ADD COLUMN record_stop_detail TEXT;
