-- When an attempt's summary was written (D-899).
--
-- The summary is written inside the build, which the operator times only as a
-- whole. The bundle's processing.meetingSummary brackets the model call
-- (startedAt, finishedAt), and a speaker-edit replay's report brackets a
-- rewrite; either is copied onto the attempt, so the run can show the
-- summary's own time and a build speed without it. Empty when no summary was
-- written by this attempt, and for attempts from before this.
ALTER TABLE job_attempts ADD COLUMN summary_started_at TEXT NOT NULL DEFAULT '';
ALTER TABLE job_attempts ADD COLUMN summary_finished_at TEXT NOT NULL DEFAULT '';
