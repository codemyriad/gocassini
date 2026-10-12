-- People's edits to a meeting's speakers: "several people used this device",
-- "these two voices are the same person", and the names they typed.
--
-- One revisioned document per job is the desired state. It is applied to the
-- original transcript every time, never to the previous result, so a refine is
-- idempotent and removing an entry undoes it exactly. revision counts accepted
-- writes; applied_revision is the revision the published recording carries,
-- set only when a publish of an attempt that applied it succeeds, and
-- applied_doc_json is that revision's document: what a rerun replays, because
-- a newer revision that never applied (its split could not be diarized, say)
-- would fail the rerun the same way.
--
-- Keyed by job, not by Nextcloud file: a split needs the job's own per-stream
-- capture (current/<job>.run), so a meeting without a job cannot be split.
-- Not a foreign key, for the same reason ignored_recordings is not one: the
-- jobs table has been rebuilt by a migration before.
CREATE TABLE speaker_edits (
  job_id TEXT PRIMARY KEY NOT NULL,
  revision INTEGER NOT NULL DEFAULT 0,
  doc_json TEXT NOT NULL,
  applied_revision INTEGER NOT NULL DEFAULT 0,
  applied_doc_json TEXT,
  last_error TEXT,
  last_report_json TEXT,
  updated_by TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL
);

-- The diarizer's output for one participant of one job. Write-once: computed
-- the first time someone splits that participant and reused for ever after,
-- by every refine and every rerun, so voice ids never move between applies
-- and a rename sticks to the same person. Timestamps only — no voice data.
CREATE TABLE speaker_split_turns (
  job_id TEXT NOT NULL,
  speaker_id TEXT NOT NULL,
  turns_json TEXT NOT NULL,
  model_sha256 TEXT NOT NULL DEFAULT '',
  source_sha256 TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  PRIMARY KEY (job_id, speaker_id)
);

-- The edits document an attempt applies, frozen when it was queued: a refine
-- attempt's whole input, and what a rerun replays after its build. NULL for
-- every attempt that applies none. The report is what the CLI said it did,
-- held here until the attempt's publish succeeds.
ALTER TABLE job_attempts ADD COLUMN speaker_edits_json TEXT;
ALTER TABLE job_attempts ADD COLUMN speaker_edits_report_json TEXT;
