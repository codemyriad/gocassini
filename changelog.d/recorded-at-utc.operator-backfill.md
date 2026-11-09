### Changed
- Meetings recorded before this release now carry the instant they started.
  On upgrade the ExApp works it out from what it kept of each recording (the
  recording's own timeline, then the recorder's start, then the job), adds
  `recordedAtUtc` to the meeting list, and rewrites each delivered `.opus` in
  place so the file carries it too. Shares, marks and file ids are kept.
  Imported meetings are left as they are.
- The meeting list, search hits, the rooms list and MCP results carry
  `recordedAtUtc` and order meetings by it. `dateLabel` is unchanged for older
  clients.
- `published/meetings-list` and the MCP `list` tool accept `from`/`to` as an
  instant with its offset (`2026-08-11T14:30:05+02:00`). Dates without a zone
  match what they matched before.
