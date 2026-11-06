-- Who a published meeting could not be shared with (D-885).
--
-- Publication shares the meeting with its room's audience. The person who
-- started the recording must receive it, or the publish fails; anyone else a
-- Nextcloud sharing policy refuses is skipped, and recorded here so the run
-- names them instead of the meeting silently not appearing for them.
--
-- One row per job, not per attempt: shares belong to the delivered file, which
-- a rerun keeps and does not re-share (audience edits stick), so a per-attempt
-- record would lose the warning on the next rerun while the people it names
-- still have no access. Each reconciliation — the first publish, or an
-- administrator's re-share — replaces the row wholesale.
--
-- status:        complete | partial | starter_refused
-- skipped_count: how many entries `skipped` holds, for the run list's flag
-- skipped:       JSON [{"type","id","reason"}]
CREATE TABLE job_share_outcomes (
  job_id        TEXT PRIMARY KEY REFERENCES jobs(id) ON DELETE CASCADE,
  status        TEXT NOT NULL,
  skipped_count INTEGER NOT NULL DEFAULT 0,
  skipped       TEXT NOT NULL DEFAULT '[]',
  updated_at    TEXT NOT NULL
);
