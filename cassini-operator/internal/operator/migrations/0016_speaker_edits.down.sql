ALTER TABLE job_attempts DROP COLUMN speaker_edits_report_json;
ALTER TABLE job_attempts DROP COLUMN speaker_edits_json;
DROP TABLE IF EXISTS speaker_split_turns;
DROP TABLE IF EXISTS speaker_edits;
