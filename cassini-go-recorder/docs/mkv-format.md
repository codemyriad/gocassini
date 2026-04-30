# MKV Format (Cassini Meeting MKV-v1)

## Scope

This document covers the Matroska (`.mkv`) format as Cassini uses it — the conventions Cassini puts on top of stock MKV to make a meeting recording self-describing.

- The general MKV/Matroska format: brief primer below; for the full spec see [matroska.org](https://www.matroska.org/technical/elements.html) and [EBML RFC 8794](https://datatracker.ietf.org/doc/rfc8794/).
- The capture-time formats that *feed* the MKV (`.rtplog`, session artifact): see [`formats.md`](formats.md).
- The *process* of building the MKV from session artifacts: see [`muxing.md`](muxing.md).
- Strategic direction for MKV as the primary artifact: see [`architecture-migration-status.md`](architecture-migration-status.md).

## Matroska in 90 seconds

- **Matroska** is an open container format (think MP4 but extensible). The audio-only WebM subset is a profile of Matroska.
- The byte-level encoding is **EBML** — a binary, length-prefixed, tagged tree. Conceptually similar to nested protobuf, but designed for streaming media.
- The top-level tree contains a few well-known elements:
  - `EBML` header (file magic, version, doctype `matroska`/`webm`)
  - `Segment` — the container for everything else
    - `SegmentInfo` — title, segment UID, duration, timestamp scale
    - `Tracks` — one entry per audio/video/subtitle/attachment track
    - `Cues` — seek index
    - `Tags` — free-form key/value metadata, scoped to the file or to specific tracks
    - `Attachments` — embedded files (any MIME type)
    - `Clusters` — the actual encoded media samples, time-ordered
- Two practical consequences for Cassini:
  1. **Tags carry arbitrary metadata.** Cassini exposes session-level and per-stream metadata as tags so the MKV is self-describing without a sidecar.
  2. **Attachments embed sidecar files.** Cassini attaches a JSON report (`cassini-report.v1.json`) inside the MKV.

Both mechanisms are stock Matroska — any MKV reader (mkvtoolnix, ffmpeg, VLC) can extract them; we don't invent a new container.

## The Cassini MKV-v1 contract

A *compliant Cassini meeting MKV* is a Matroska file that, in addition to its audio/video tracks, carries the following:

1. A **container-level tag** `cassini_format = "cassini-meeting-mkv-v1"` — the version sentinel.
2. A set of **container-level tags** identifying the session, recorder identity, and aggregate remux statistics.
3. A set of **per-stream (per-track) tags** identifying the originating logical track, packet stream, codec, and timeline adjustments.
4. An **attached file** named `cassini-report.v1.json` containing the full session metadata as JSON.

The constants live in [`pkg/core/remux/metadata.go:15-19`](../pkg/core/remux/metadata.go#L15-L19):

```go
MeetingFormatVersion = "cassini-meeting-mkv-v1"
embeddedReportSchema = "cassini.embedded-report.v1"
embeddedReportFile   = "cassini-report.v1.json"
```

If any of these are absent, the MKV predates V1 and should be upgraded via `cmd/gocassini-upgrade-mkv` — see "Legacy upgrade" below.

## Container-level tags

Built by [`containerMetadataEntries`](../pkg/core/remux/metadata.go#L159-L175). Each entry becomes one ffmpeg `-metadata KEY=VALUE` argument and lands as a Matroska `Tag` element scoped to the segment.

| Key | Source | Meaning |
|---|---|---|
| `title` | caller-supplied or `"Cassini Artifact Remux <session_id>"` | Human-readable title |
| `session_id` | `session.SessionID` | Stable session UUID matching `sessions/<session_id>/` |
| `cassini_format` | constant | Format version sentinel; always `cassini-meeting-mkv-v1` for V1 |
| `cassini_embedded_report` | constant | Filename of the attached JSON report (`cassini-report.v1.json`) |
| `session_started_at` | `session.StartedWallUTC` | Wall-clock RFC3339 start time |
| `participant_count` | `len(sess.Participants)` | Number of participants seen |
| `logical_track_count` | `len(sess.LogicalTracks)` | Number of distinct logical tracks |
| `packet_stream_count` | `len(sess.PacketStreams)` | Number of `.rtplog` segments that fed this MKV |
| `artifact_remux_segments` | `len(plans)` | Stream plans applied during remux |
| `artifact_remux_adjusted_streams` | `SummarizePlanAdjustments` | How many streams had a non-zero timeline adjustment |
| `artifact_remux_total_adjust_ns` | `SummarizePlanAdjustments` | Cumulative adjustment in nanoseconds |
| `artifact_remux_max_abs_adjust_ns` | `SummarizePlanAdjustments` | Largest absolute adjustment, useful for drift triage |

These are intended to make the MKV self-describing for **playback and inspection** without parsing the JSON attachment.

## Per-stream (per-track) tags

Built by [`streamMetadataEntries`](../pkg/core/remux/metadata.go#L177-L206). Each entry is attached via `-metadata:s:v:N` (video stream N) or `-metadata:s:a:N` (audio stream N) and lands as a Matroska `Tag` element scoped to the track.

| Key | Meaning |
|---|---|
| `title` | Participant name when known; otherwise `stream_id` |
| `ltid` | Logical track ID (stable across SSRC churn) |
| `stream_id` | Packet-stream segment ID (unique per `.rtplog`) |
| `kind` | `audio` or `video` |
| `codec` | `opus`, `vp8`, `vp9`, `h264`, `av1`, etc. |
| `timeline_adjust_ns` | Per-stream timeline adjustment applied during remux (for SR-aware drift correction) |
| `timeline_samples` | Number of timeline samples observed for this stream |
| `source_start_seconds` | Original receive-time start, in seconds |
| `offset_seconds` | `-itsoffset` value applied to this input during merge (zero if not adjusted) |
| `rtp_packets` | RTP packet count contributing to this segment |
| `pt` | RTP payload type |
| `participant_id`, `participant_name` | When known |
| `mid`, `rid` | WebRTC track identifiers |
| `clock_rate` | RTP clock rate (e.g. 48000 for Opus, 90000 for video) |

These let `gocassini-inspect`, `verify-av-drift.sh`, and human triage answer "which participant, which segment, which clock, which adjustment" directly from the MKV.

## The embedded JSON attachment

`cassini-report.v1.json` is attached to the MKV with `-attach <path>` and tagged `mimetype=application/json`. Its schema is `cassini.embedded-report.v1` (constant in [`metadata.go:17`](../pkg/core/remux/metadata.go#L17)).

Top-level shape (see [`embeddedReport`](../pkg/core/remux/metadata.go#L21-L26)):

```jsonc
{
  "schema": "cassini.embedded-report.v1",
  "generated_at": "2026-04-29T14:00:00.000Z",
  "session": {
    "version": 1,
    "session_id": "…",
    "started_wall_utc": "…",
    "platform": {
      "name": "nextcloud-talk",
      "deployment": "…",
      "recorder_identity": { … }
    },
    "participants": [ … ],
    "logical_tracks": [ { "ltid", "kind", "source", "participant_id", "mid", "rid" } ],
    "packet_streams": [ { "stream_id", "ltid", "primary_ssrc", "csrc", "codec", "clock_rate", "pt", "fmtp_snapshot" } ]
  },
  "artifact_remux": {
    "used": true,
    "segments": N,
    "adjusted_streams": N,
    "total_adjust_ns": N,
    "max_abs_adjust_ns": N,
    "mean_adjust_ns_per_stream": N,
    "embedded_report_file": "cassini-report.v1.json",
    "portable_meeting_format": "cassini-meeting-mkv-v1",
    "stream_plans": [ … ]
  }
}
```

This JSON is a *richer* superset of the container tags. The container tags are summary breadcrumbs; the JSON is the full structured truth. The two are intentionally redundant: tags survive any tool that re-muxes carelessly, but the JSON is more reliable to parse.

To extract:

```bash
# List all attachments in an MKV
mkvmerge -i meeting.mkv

# Extract the report by attachment number
mkvextract attachments meeting.mkv 1:cassini-report.v1.json

# Or via ffmpeg
ffmpeg -dump_attachment:t cassini-report.v1.json -i meeting.mkv -y -f null /dev/null
```

## How the MKV is built

Two distinct code paths produce a Cassini MKV-v1; both end with the same ffmpeg invocation pattern.

### 1. Fresh remux from session artifacts

[`pkg/core/remux/artifact.go:266-334`](../pkg/core/remux/artifact.go#L266-L334) — `mergeSegments`.

Inputs: the `streams/*.rtplog` segments that have already been depacketized into elementary streams (Opus + VP8/VP9/H264/AV1).

Sketch of the ffmpeg invocation:

```bash
ffmpeg -y -v error \
  [-itsoffset <secs>] -i <segment-1.opus|.ivf|.h264> \
  [-itsoffset <secs>] -i <segment-2…> \
  -map 0:a:0 -metadata:s:a:0 title=<participant> -metadata:s:a:0 ltid=<…> … \
  -map 1:v:0 -metadata:s:v:0 title=<participant> -metadata:s:v:0 ltid=<…> … \
  -c copy \
  -metadata title=<…> -metadata session_id=<…> -metadata cassini_format=cassini-meeting-mkv-v1 … \
  -attach cassini-report.v1.json \
    -metadata:s:t:0 mimetype=application/json \
    -metadata:s:t:0 filename=cassini-report.v1.json \
  meeting.mkv
```

Key choices:

- `-c copy` — no re-encoding. Track payloads land bit-identical to the elementary streams.
- `-itsoffset` per input — shifts a stream's start time without re-encoding (used for SR-aware timeline adjustments).
- `-copyts` — used in upstream single-track composition (see `composeSingleTrackMKV`) so sparse packet timelines aren't flattened. Not used here for the multitrack merge.
- The attachment is added with `-metadata:s:t:0 mimetype=…` and `filename=…` because attachments are streams in ffmpeg's model.

### 2. Legacy MKV upgrade

[`pkg/core/remux/upgrade.go:126-147`](../pkg/core/remux/upgrade.go#L126-L147) — `cmd/gocassini-upgrade-mkv`.

Used when an older MKV exists alongside a legacy `<meeting>.mkv.json` recorder report but no `sessions/<id>/` directory. The upgrader:

1. Loads the legacy report.
2. Reconstructs a `session.Session` from it.
3. Runs the same `containerMetadataEntries` / `streamMetadataEntries` / `writeEmbeddedReportFile` path.
4. Calls ffmpeg to copy the existing audio/video streams unchanged while injecting the V1 metadata and attachment.

Sketch:

```bash
ffmpeg -y -v error -i legacy.mkv \
  -map 0:0 -map 0:1 … \
  -c copy \
  -metadata title=<…> -metadata cassini_format=cassini-meeting-mkv-v1 … \
  -metadata:s:a:0 title=<…> ltid=<…> … \
  -attach cassini-report.v1.json \
    -metadata:s:t:0 mimetype=application/json \
    -metadata:s:t:0 filename=cassini-report.v1.json \
  legacy.v1.mkv
```

Output filename defaults to `<input-without-ext>.v1.mkv`.

## What does *not* belong in the MKV

Per [`architecture-migration-status.md:75-78`](architecture-migration-status.md#L75-L78), Cassini deliberately keeps host-local and privacy-sensitive details *out* of embedded metadata when they don't belong in a portable artifact. Concrete examples that are intentionally not embedded as tags:

- Bot credentials, OAuth tokens, signaling URLs
- Local filesystem paths from the recorder host
- The recorder's IP or container identity beyond `recorder_identity` in the session JSON
- Caller-side identifiers used by the OCS API for room control

If you find yourself adding something to `containerMetadataEntries` or to the embedded report, ask: *would I be comfortable emailing this MKV to a customer?* If no, it doesn't belong here.

## Inspecting a Cassini MKV

Quick layered inspection:

```bash
# Container-level + per-track tags
mkvinfo meeting.mkv | less

# Or via ffprobe
ffprobe -hide_banner -show_format -show_streams -show_chapters meeting.mkv

# Cassini's view (parses tags + attachment, surfaces drift/churn)
go run ./cmd/gocassini-inspect meeting.mkv

# Drift verification (strict; for controlled bot scenarios)
./harness/bin/verify-av-drift.sh --input meeting.mkv
```

`gocassini-inspect` is the canonical Cassini-aware tool: it knows the V1 schema and reports per-stream churn, validation issues, and timeline deltas in a single pass.

## Where the MKV sits in the pipeline

```text
                                         (this doc covers ↓)
recorder runtime  →  session artifact  →  multitrack MKV  →  cassini build  →  meeting bundle  →  viewer / portable .opus
   (live)          (sessions/<id>/)      (meeting.mkv)       (transcribe)     (manifest+sidecars)    (consumption)
```

- **Producer:** [`internal/talk/recorder.go`](../internal/talk/recorder.go) writes the session artifact, then [`pkg/core/remux/artifact.go`](../pkg/core/remux/artifact.go) composes the MKV at session end.
- **Consumer:** [`internal/transcribe/transcribe.go`](../internal/transcribe/transcribe.go) takes the MKV as input to `BuildMeetingArtifact` and produces the meeting bundle (see [`transcription-pipeline.md`](transcription-pipeline.md)).
- **Standalone uses:** any tool that can read MKV (VLC, mpv, web players via WebM-compatible elementary streams). The V1 metadata makes generic players show participant names per track without external context.

## See also

- [`formats.md`](formats.md) — `.rtplog` format that feeds the MKV
- [`muxing.md`](muxing.md) — the remux process and offset planning
- [`transcription-pipeline.md`](transcription-pipeline.md) — what consumes the MKV
- [`architecture-migration-status.md`](architecture-migration-status.md) — strategic direction for the MKV as primary artifact
- [`pkg/core/remux/metadata.go`](../pkg/core/remux/metadata.go) — the canonical source for tag/attachment schema
