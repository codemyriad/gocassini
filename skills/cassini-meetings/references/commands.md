# Cassini meetings command reference

Read this reference when ordinary room/date selection is insufficient, when
machine-readable fields matter, or when fetching a portable recording.

## CLI and connection

Use `cassini` from `PATH`, or `./bin/cassini` from a Cassini checkout.
`meetings context` requires `ffprobe`; `rooms`, `list`, and `fetch` do not.

| Environment variable | Equivalent flag | Default |
|---|---|---|
| `CASSINI_NC_URL` | `--nextcloud-url` | required |
| `CASSINI_NC_USER` | `--user` | required |
| `CASSINI_NC_APP_PASSWORD` | `--app-password` | required |
| `CASSINI_NC_APP_ID` | `--app-id` | `gocassini` |

Keep the app password in the environment, not a flag. A flag wins over its
environment variable. Never use `--insecure` except for an explicitly approved
local test harness.

## List rooms

```bash
cassini meetings rooms
cassini meetings rooms --json
```

The text form contains one row per room:

```text
rooms=2 caller=alice source=nextcloud-files
room=rm_9f2a1c3d4e5b6a70 name=Weekly Sync meetings=12 latest=2026-08-11 10:32 earliest=2026-05-05 10:30
```

Use JSON for selection. Each entry in `.rooms` has:

- `room`: the exact opaque selector accepted by `list --room`
- `roomId` and `roomName`: source metadata; either may be absent
- `meetings`, `latest`, and `earliest`: count and wall-clock labels

The document also has `unattributed`, the count of meetings with no selectable
room, and `skipped`, the count of unusable catalog entries.

Rooms are derived only from recordings the account may already read; this is
not a directory of all Nextcloud Talk conversations. Copy `room` exactly. It is
not a Talk token, is not searchable by substring, and cannot be reconstructed
from the display name.

Two rows may have the same display name when old and new recordings identify
one apparent conversation differently. The data cannot prove they are the same.
Query both when the request spans them, explain the split, and leave any merge
to an administrator.

## List meetings

```bash
cassini meetings list
cassini meetings list --room rm_9f2a1c3d4e5b6a70
cassini meetings list --from 2026-08-01 --to 2026-08-31
cassini meetings list --room rm_9f2a1c3d4e5b6a70 --from 2026-08-01 --json
```

Results are newest first. The filters are optional and combine with AND.
`--room` matches the exact opaque room selector. `--from` and `--to` accept:

- `YYYY-MM-DD`
- `YYYY-MM-DD HH:MM`
- `YYYY-MM-DD HH:MM:SS`

These values come from `dateLabel` and carry no timezone. Do not add one. A bare
date covers the entire day at either boundary. Backwards ranges are usage errors
rather than empty successful results. An unparseable meeting date is excluded
from every dated range; list without date filters when such a meeting could
matter.

Use `--json` for all programmatic selection. Human-readable `title=` and
`room=` values are unquoted and unescaped. Newlines are stripped, but embedded
text such as `meeting=X id=admin` can fool a `key=value` parser.

The JSON document includes:

- `.meetings[]`: the visible matches; use each entry's `id`
- `.filter`: the active filters
- `.excluded`: entries intentionally removed by those filters
- `.excludedUndated`: entries omitted because their dates could not be parsed
- `.skipped`: malformed catalog entries dropped before selection

`excluded` is expected when filtering. Nonzero `skipped` means the result is
incomplete. Report the applied slice when answering from filtered results.

In JSON, an absent or empty `.audioPath` identifies a recording from before the
portable single-file format; the text form renders this as `fetchable=no`. It
cannot be used with `context` or `fetch`; do not retry.

## Retrieve context

```bash
cassini meetings context <meeting-id>
cassini meetings context <meeting-id> --out <private-path>
cassini meetings context <meeting-id> --json
```

Markdown contains identity, duration, a generated summary when present, and a
speaker-attributed transcript. JSON uses schema
`cassini.meetings.context.v1` and exposes per-segment timings for approximate
citations. A context bundle is source material, not an edited transcript.

`_No summary was generated for this meeting._` means the deployment had no
summarizer configured. It is not a retrieval error.

## Fetch the portable recording

```bash
cassini meetings fetch <meeting-id> --out "./Meeting.opus"
cassini inspect "./Meeting.opus"
```

The downloaded `.opus` contains the audio and embedded transcript and summary.
The CLI stages downloads before moving them into place, rejects empty replies,
and creates the destination readable only by its owner. Preserve that privacy
unless the user explicitly requests broader access.

## Access provenance

Room and meeting listings name a `source` in their text and JSON forms:

- `nextcloud-files`: Nextcloud Files supplied the bytes as the calling user;
  per-caller permissions were applied.
- `unknown` or `unrecognised`: the response may come from a development or
  differently configured operator; per-caller permissions are not established.

Warn on any source other than `nextcloud-files`. All retrieval commands also
warn on stderr when the source is unexpected; do not discard that warning.
