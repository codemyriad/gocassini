-- What an attempt's transcription and summary steps did (D-854, D-814).
--
-- A meeting can publish with no transcript (the model crashed, ran out of
-- memory at the retry ceiling, was never installed) or with no summary
-- (switched off, not configured, the model call failed), and the run still
-- read "done". The outcome is in the built bundle's manifest
-- (processing.transcription, processing.meetingSummary); these columns copy it
-- onto the attempt once it is built, so the Operator can show it without
-- opening bundles. transcription_detail is what the operator saw itself and
-- the bundle cannot say: the tail of a crashed build, or why transcription was
-- dropped at the resource ceiling. All empty for attempts built before this.
ALTER TABLE job_attempts ADD COLUMN transcription_status TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN transcription_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN transcription_detail TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN summary_status TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN summary_reason TEXT NOT NULL DEFAULT '';
