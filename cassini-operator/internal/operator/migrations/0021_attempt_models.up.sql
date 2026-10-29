-- Which models an attempt ran (D-816).
--
-- The built bundle's manifest names them: processing.transcription.model and
-- .revision for the speech model, provenance.speechToText.device for where it
-- ran, and processing.meetingSummary.model for the summary. They are copied
-- onto the attempt with its step outcomes, so the Operator can say what
-- produced a meeting without opening bundles. Empty for attempts built before
-- this, and for steps that did not run.
ALTER TABLE job_attempts ADD COLUMN transcription_model TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN transcription_revision TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN transcription_device TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN summary_model TEXT NOT NULL DEFAULT '';
