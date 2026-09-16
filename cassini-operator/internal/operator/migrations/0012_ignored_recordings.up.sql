-- Recordings an administrator has asked not to be reminded about (D-769).
--
-- The list of everyone-readable recordings is derived: it is a PROPFIND of the
-- Team folder joined to the jobs table, recomputed on every read, so it needs
-- no state of its own and cannot go stale. That is the right shape for a list
-- of facts — but it means a recording that can never be narrowed stays on it
-- for ever, and a list with a permanent floor is one nobody reads.
--
-- Two cases never resolve on their own:
--
--   * a recording with no job row, or whose roster was never captured. Nothing
--     will ever be able to narrow it;
--   * a recording deliberately opened in Files, which is doing exactly what its
--     owner wants.
--
-- So this table is the one durable decision in the feature: not a cache, not
-- derived, and not reconstructible — which is why it is a table and not a
-- column on something disposable. Ids only. Removing a row un-ignores the
-- recording, and the panel offers that, so a mis-click is not permanent.
CREATE TABLE ignored_recordings (
  -- The meeting id, which is the job id and the .opus basename. Not a foreign
  -- key: a recording whose job row never existed is precisely one of the cases
  -- worth ignoring.
  meeting_id TEXT PRIMARY KEY NOT NULL,
  ignored_at TEXT NOT NULL
);
