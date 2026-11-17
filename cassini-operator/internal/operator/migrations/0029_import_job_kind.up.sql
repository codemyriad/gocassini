-- What an import row produces (D-828 follow-up: audio meeting upload).
--
-- "meeting" is the original portable-meeting import: the uploaded file already
-- carries its audio, transcript and summary. "audio" is the alternate creation
-- path: a raw recording is transcribed and summarised here before delivery.
-- The kind is fixed when the upload is registered, like its sharing, so a
-- submit can never reinterpret bytes already staged.
ALTER TABLE import_jobs ADD COLUMN kind TEXT NOT NULL DEFAULT 'meeting';
