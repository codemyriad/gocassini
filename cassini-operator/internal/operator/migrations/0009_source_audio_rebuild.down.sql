DROP INDEX IF EXISTS jobs_source_audio_rebuild_pending;
DROP INDEX IF EXISTS jobs_talk_room_token;
ALTER TABLE jobs DROP COLUMN source_audio_rebuild_count;
ALTER TABLE jobs DROP COLUMN source_audio_built_digest;
ALTER TABLE jobs DROP COLUMN source_audio_upload_at;
ALTER TABLE jobs DROP COLUMN source_audio_built_seq;
ALTER TABLE jobs DROP COLUMN source_audio_upload_seq;
