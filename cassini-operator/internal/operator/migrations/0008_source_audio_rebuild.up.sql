-- Rebuilding a meeting when a participant's capture arrives after its build
-- (D-698).
--
-- The operator queues the build the instant Talk reports the recording stopped,
-- and the browser starts its upload on that same signal. On any link slower
-- than the build itself the audio therefore lands after the transcript is
-- already made -- and a slow link is the entire reason this feature exists, so
-- the audio most worth having is the audio most likely to be late. Observed on
-- the demo: the build started at 19:51:03 and the upload landed at 19:51:04,
-- and the transcript was made from the recorded track alone.
--
-- Waiting before building cannot fix that. A wait tries to bound something the
-- server does not control, and the participants it would skip are exactly the
-- ones it was built for. Noticing afterwards needs no prediction, so the schema
-- below is what noticing costs.

-- Two counters rather than a boolean flag.
--
-- Every accepted upload that actually changed the bytes on disk bumps
-- source_audio_upload_seq. A build stamps source_audio_built_seq with the value
-- it read when it CLAIMED its work, and only once it has succeeded. A gap
-- between them means audio arrived that no build has seen, which is the rebuild
-- trigger.
--
-- A flag cannot express that. It has to be cleared somewhere, and every place
-- to clear it either loses an upload that landed while the build was running or
-- re-runs for ever. Counters also coalesce a wave of uploads into a single
-- rebuild with no extra bookkeeping, and a rebuild that was interrupted is
-- simply still owed on the next pass rather than needing a recovery path of its
-- own.
ALTER TABLE jobs ADD COLUMN source_audio_upload_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN source_audio_built_seq INTEGER NOT NULL DEFAULT 0;

-- When the newest upload was attributed to this job. The dispatcher waits for a
-- quiet period after it before rebuilding, so a room where four participants
-- all finish uploading within a minute of each other produces one rebuild
-- rather than four. The counters alone coalesce only what arrived before the
-- first rebuild started.
ALTER TABLE jobs ADD COLUMN source_audio_upload_at TEXT;

-- The digest of the capture set the last successful build actually consumed:
-- owner, call start, segment names and segment sizes, hashed. It answers the
-- one question the counters cannot -- "would this rebuild read anything
-- different from the last one" -- and it is what makes a redundant rebuild a
-- logged no-op instead of a republished, byte-identical meeting. It also
-- catches the case where the retention sweep removed the capture between the
-- upload and the rebuild, which would otherwise republish a meeting with LESS
-- in it than the one it replaced.
ALTER TABLE jobs ADD COLUMN source_audio_built_digest TEXT;

-- How many rebuilds this job has already had on the back of a late upload.
-- Bounded (see maxSourceAudioRebuilds): a participant retrying an upload the
-- build then cannot place must not be able to re-transcribe a meeting for ever.
-- Administrator reruns do not count against it -- only rebuilds this mechanism
-- scheduled.
ALTER TABLE jobs ADD COLUMN source_audio_rebuild_count INTEGER NOT NULL DEFAULT 0;

-- Resolving an upload to its recording needs a lookup by Talk room token, which
-- lives only inside the talk_binding blob. An expression index rather than a
-- new column: a column would store the same value twice and need a backfill,
-- and a row with no binding is simply absent from this index rather than an
-- error, which is what a job that never had a room should be.
CREATE INDEX jobs_talk_room_token
  ON jobs(json_extract(talk_binding, '$.room_token'));

-- The dispatcher's scan runs every fifteen seconds against a table that holds
-- every job this installation ever ran, while the rows it wants are almost
-- always none. A partial index keeps that scan proportional to the debt rather
-- than to the history.
CREATE INDEX jobs_source_audio_rebuild_pending
  ON jobs(updated_at)
  WHERE source_audio_upload_seq > source_audio_built_seq;
