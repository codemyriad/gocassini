-- The share audience chosen when a recording started (#472).
--
-- A named mode, not a boolean, so another audience can be added without a
-- migration. "room" is the historical/default audience; "attendees" shares only
-- with people Talk reported in the call. The mode is frozen on the job so a
-- settings change while a recording is in progress cannot alter its captures,
-- publication, re-share or label.
--
-- audience_notice is for the run's warning when attendee mode had no saved
-- roster and only the starter received the recording (#472 review).
ALTER TABLE jobs ADD COLUMN audience_mode TEXT NOT NULL DEFAULT 'room';
ALTER TABLE jobs ADD COLUMN audience_notice TEXT;
