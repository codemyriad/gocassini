-- What a speaker-edit attempt does with the meeting's summary (D-814).
--
-- Rewriting the summary for the edited speakers costs a call to the summary
-- model, so it happens only when someone allowed to ask for it (an
-- administrator or a moderator of the meeting's room) did. The choice is made
-- at save time and belongs to that save's attempt: 'rewrite', or '' for keep,
-- which leaves the summary as it was and reports it out of date.
ALTER TABLE job_attempts ADD COLUMN summary_mode TEXT NOT NULL DEFAULT '';
