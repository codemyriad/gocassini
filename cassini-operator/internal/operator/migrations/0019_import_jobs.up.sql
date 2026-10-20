-- Import jobs are their own table rather than rows on `jobs`, for the same
-- reason insight runs are (0009/D-720): a job is a recording moving through
-- record -> build -> seal -> publish, and both machines that drive that table
-- would mishandle an import. The import worker owns its own restart recovery.
--
-- One row per file; a batch (one submit from the Import tab) shares a batch_id
-- but has no state of its own — files are independent. `id` is a ULID minted at
-- enqueue time and doubles as the delivered meeting stem, the same symmetry
-- recordings have with jobs.id, and it is what the retention provenance gate
-- joins on. Rows are never deleted: a done row is the proof a managed meeting
-- was deliberately imported (D-828), which retention needs after the lifecycle
-- ledger or the metadata index is rebuilt.
CREATE TABLE import_jobs (
  id         TEXT PRIMARY KEY,
  batch_id   TEXT NOT NULL,
  created_by TEXT NOT NULL,
  source_name TEXT NOT NULL,
  staged_path TEXT NOT NULL,
  format     TEXT NOT NULL DEFAULT '',
  state      TEXT NOT NULL,
  phase      TEXT NOT NULL DEFAULT '',
  error      TEXT NOT NULL DEFAULT '',
  -- The sharing the importer asked for: {"share": bool, "audience": [{type,id}]}.
  -- The importer is always the share starter and is not repeated here.
  audience_json TEXT NOT NULL DEFAULT '{}',
  -- Provenance read out of the file at validation. The file itself stays the
  -- authoritative record; these are the join keys an operator can query.
  content_id TEXT NOT NULL DEFAULT '',
  source_room_id TEXT NOT NULL DEFAULT '',
  meeting_created_at_utc TEXT NOT NULL DEFAULT '',
  meeting_recorded_at_local TEXT NOT NULL DEFAULT '',
  -- Outcome.
  delivered_name TEXT NOT NULL DEFAULT '',
  file_id    INTEGER NOT NULL DEFAULT 0,
  imported_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX import_jobs_by_creator ON import_jobs(created_by, created_at DESC, id DESC);
CREATE INDEX import_jobs_by_state ON import_jobs(state);
