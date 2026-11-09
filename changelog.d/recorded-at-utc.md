### Added

- Talk recordings now record exactly when they started. The meeting file gains `meeting.recordedAtUtc` (mirrored as the `CASSINI_RECORDED_AT_UTC` tag), its `DATE` tag holds that start instead of the time the file was built, and `catalog.json`, the meeting list, search hits, the rooms list and MCP results carry `recordedAtUtc`. `dateLabel` is unchanged for older clients.
- `cassini retag --recorded-at-utc <RFC 3339 UTC>` adds the recording start to an existing file without changing its meeting id.

### Changed

- Meeting times are shown in your local time zone: the viewer's meeting list, meeting header, search, insight names and exported transcripts, and the CLI's `list`, `rooms`, `search`, `meetings context` and `inspect --meeting-times`. Sorting, month headings and date filters follow the time shown. Meetings that carry only the older date label are shown as before.
- Past ExApp recordings are corrected on upgrade. The ExApp works out each one's start from what it kept (the recording's own timeline, then the recorder's start, then the job), adds it to the meeting list, and rewrites each delivered `.opus` in place so the file carries it too. Shares, marks and file ids are kept. Imported and cron-era meetings are left as they are.
- `cassini meetings list --from/--to` are read in your local time zone. `published/meetings-list` and the MCP `list` tool accept `from`/`to` as an instant with its offset (`2026-08-11T14:30:05+02:00`); dates without a zone match what they matched before.
- Talk recordings no longer write `recordedAtLocal`. The recorder never knew the room's time zone, so the value was UTC digits presented as local time.
