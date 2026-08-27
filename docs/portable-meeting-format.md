# Cassini Portable Meeting Format

Date: 2026-03-11

Status: Proposed v1

## Goal

Define one user-facing meeting file that is:

- one normal file
- directly playable as audio
- portable and easy to share
- rich enough for Cassini to reopen with transcript, speakers, and search

This format is the contract that both Cassini producers and Cassini consumers
should implement.

The `.opus` portable meeting file is the **one canonical, user-facing format**
and the only durable, published Cassini contract. The producer now emits
`org.cassini.portable-meeting/2` (v2); v1 remains readable for older files but
is no longer emitted. The intermediate `.meeting` bundle directory (with its
`cassini.json` and `manifest.json`) is transient build scratch, not a
deliverable — see [Why `.meeting` is not a contract](#why-meeting-is-not-a-contract).

## Decision

Cassini v1 portable meeting files should be:

- file extension: `.opus`
- container: Ogg
- audio codec: Opus
- embedded metadata: OpusTags comments
- embedded rich payload: UTF-8 JSON, `gzip` compressed, `base64url` encoded, split across numbered tags

Example filename:

```text
2026-03-11 Weekly Sync.opus
```

## Why This Shape

This format optimizes for the user workflow:

- move one file around
- play it in ordinary audio players
- open it in Cassini and get the transcript and meeting structure back

It also keeps the format legible to a casual observer because the file exposes:

- ordinary audio playback
- plain-text metadata keys
- explicit instructions for decoding the embedded payload

## Notation

This document defines:

- required behavior with `MUST`
- recommended behavior with `SHOULD`
- optional behavior with `MAY`

## File Identification

A file is a Cassini portable meeting file when all of the following are true:

1. the container is Ogg Opus
2. OpusTags contains `CASSINI_FORMAT=org.cassini.portable-meeting/2` (current) or
   `org.cassini.portable-meeting/1` (older files, still readable)
3. OpusTags contains a valid Cassini payload descriptor

Consumers MUST accept both versions. A consumer that accepts only `/1` rejects
every file Cassini writes today.

If `CASSINI_FORMAT` is absent, consumers MUST treat the file as plain audio.

## Audio Profile

### Required

Portable meeting files MUST use:

- Ogg container
- Opus audio codec
- sample rate field set to `48000`

### Recommended

Producers SHOULD write:

- mono audio for normal speech-first meeting archives
- stereo only when preserving stereo content is intentional
- one final continuous playable audio program

## Metadata Layout

The file MUST expose two metadata layers:

1. human-readable summary tags
2. structured Cassini payload tags

### Standard tags

Producers SHOULD write ordinary tags that generic tools can display:

- `TITLE`
- `DATE`
- `DESCRIPTION`
- `ENCODER`
- `LANGUAGE`

Recommended `DESCRIPTION` template:

```text
Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON.
```

### Cassini descriptor tags

The following tags are REQUIRED. The `CASSINI_FORMAT` and `CASSINI_PAYLOAD_SCHEMA`
values below are the v1 forms; a producer emitting v2 writes
`org.cassini.portable-meeting/2` and the `…-v2.schema.json` URL instead, and adds
the per-transcript descriptors described under
[v2 (Multi-Transcription)](#v2-multi-transcription).

- `CASSINI_FORMAT=org.cassini.portable-meeting/1`
- `CASSINI_PROFILE=ogg-opus`
- `CASSINI_PAYLOAD_MIME=application/vnd.cassini.portable-meeting+json`
- `CASSINI_PAYLOAD_ENCODING=base64url+gzip+utf8json`
- `CASSINI_PAYLOAD_SCHEMA=https://cassini.local/spec/cassini-portable-meeting-manifest-v1.schema.json`
- `CASSINI_PAYLOAD_CHUNK_COUNT=<decimal integer>`
- `CASSINI_PAYLOAD_SHA256=<lowercase hex sha256 of decompressed UTF-8 JSON bytes>`
- `CASSINI_PAYLOAD_RAW_BYTES=<decimal integer>`
- `CASSINI_PAYLOAD_GZIP_BYTES=<decimal integer>`
- `CASSINI_AUDIO_PCM_FORMAT=s16le`
- `CASSINI_AUDIO_SAMPLE_RATE=48000`
- `CASSINI_AUDIO_CHANNELS=<1 or 2>`
- `CASSINI_AUDIO_SAMPLE_COUNT=<decimal integer>`
- `CASSINI_AUDIO_DURATION_MS=<decimal integer>`
- `CASSINI_AUDIO_PCM_SHA256=<lowercase hex sha256 of decoded PCM bytes>`
- `CASSINI_AUDIO_MATCH_POLICY=exact-pcm`
- `CASSINI_DECODE_HINT=Concatenate CASSINI_PAYLOAD_000..N, base64url decode, gzip decompress, parse UTF-8 JSON.`

The following tags are RECOMMENDED summary tags:

- `CASSINI_MEETING_ID=<stable meeting id>`
- `CASSINI_CREATED_AT=<RFC3339 UTC timestamp>`
- `CASSINI_SPEAKER_COUNT=<decimal integer>`
- `CASSINI_WORD_COUNT=<decimal integer>`
- `CASSINI_TRANSCRIPT_LANGUAGE=<BCP-47 tag or simple language code>`
- `CASSINI_STT_BACKEND=<stt backend>`
- `CASSINI_STT_ENGINE=<stt engine>`
- `CASSINI_STT_MODEL=<stt model>`
- `CASSINI_STT_DEVICE=<stt device>`
- `CASSINI_READABLE_BACKEND=<cleanup backend>`
- `CASSINI_READABLE_ENGINE=<cleanup engine>`
- `CASSINI_READABLE_MODEL=<cleanup model>`
- `CASSINI_READABLE_SOURCE=<generated|embedded|disabled>`

The following tags are OPTIONAL, and mirror the origin fields in the `meeting`
object. Each is emitted only when the value is known — absent, never empty,
because an empty `CASSINI_ROOM_ID` would read as "this meeting has a room whose
id is the empty string" and every consumer would have to check both presence and
emptiness.

- `CASSINI_ROOM_ID=rm_<16 lowercase hex>`
- `CASSINI_ROOM_NAME=<display name>` — legacy, no longer written; see below
- `CASSINI_JOB_ID=<producer job id>`
- `CASSINI_ATTEMPT_NUMBER=<decimal integer, 1-based>`

They are a convenience mirror for readers that are neither Go nor Node: the
values are already in the embedded manifest, but reading them out of it means
concatenating N chunks, base64url-decoding, gunzipping and parsing JSON — which
is a program, while these fall out of one `ffprobe -show_entries format_tags`
call. **The manifest is the record and these are the copy.** A tool that edits
one MUST edit the other; a consumer that finds them disagreeing SHOULD believe
the manifest.

### Payload chunk tags

The encoded payload MUST be split into tags named:

```text
CASSINI_PAYLOAD_000
CASSINI_PAYLOAD_001
CASSINI_PAYLOAD_002
...
```

Rules:

- indexing starts at `000`
- indexes are zero-padded to three digits
- the number of chunks MUST match `CASSINI_PAYLOAD_CHUNK_COUNT`
- values are concatenated in numeric order with no separator
- the concatenated value is the full base64url text

Producers SHOULD limit each payload chunk value to at most `4096` characters.

This is not required by Ogg itself, but it keeps the metadata reasonably
inspectable in ordinary tools.

## Payload Encoding

The Cassini payload MUST be encoded as:

1. compact UTF-8 JSON bytes
2. `gzip` compressed
3. base64url encoded without line breaks
4. split across `CASSINI_PAYLOAD_NNN` tags

The reason for using `gzip` in v1 is:

- browser decompression is simpler and more standard
- the payload is still small enough for `gzip` to work well
- the encoding remains easy to decode from the shell

If Cassini later needs a denser archival profile, that should be a new profile
or new encoding value, not an undocumented variation.

## Embedded Manifest

The decompressed payload MUST be a JSON object conforming to:

- [`spec/cassini-portable-meeting-manifest-v1.schema.json`](../spec/cassini-portable-meeting-manifest-v1.schema.json)

The top-level fields are:

- `kind`
- `version`
- `profile`
- `meeting`
- `audio`
- `integrity`
- `speakers`
- `transcript`

Optional fields:

- `provenance`
- `readableTranscript`
- `chapters`
- `summary`
- `attachments`

## Required Manifest Semantics

### `meeting`

The `meeting` object MUST identify the meeting and provide user-facing summary
fields such as:

- title
- created time
- duration
- language

It MAY also carry where the meeting came from. All four are optional, and a
meeting with none of them is an ordinary state, not a broken one:

- `roomId` — the conversation the meeting was recorded in, as a **deterministic
  one-way derivation** of that room's identity, shaped `rm_<16 lowercase hex>`.
  Never the identity itself: for a Nextcloud Talk recording it derives from the
  conversation token, and for a public conversation that token is also the link
  that joins it — so publishing it alongside a recording would turn "may read a
  past recording" into "may join the live conversation". Producers MUST NOT
  write a raw room token into this file, in this field or any other.
- `roomName` — **LEGACY, read-only.** The room's display name frozen at record
  time. Producers stopped writing it: a display name is editable and a published
  recording is not, so honouring a rename would mean rewriting every artifact
  that room ever produced. The name at record time is still available as the
  `title`; the room's *current* name belongs wherever the producer keeps mutable
  metadata. Consumers MUST still read it, because files written before the
  change carry it.
- `jobId` — the producer job that made this artifact. Optional; a file packed by
  hand has none.
- `attemptNumber` — which attempt of that job, 1-based. Absent means unknown, so
  a consumer MUST treat a non-positive value as absent rather than as an attempt.

### `audio`

The `audio` object MUST describe the playable audio actually stored in the file:

- container
- codec
- sample rate
- channels
- duration
- sample count

### `integrity`

The `integrity` object is required so Cassini can detect when audio has been
edited but metadata survived.

Required v1 fields:

- `matchPolicy`
- `pcmSha256`
- `pcmFormat`
- `sampleRate`
- `channels`
- `sampleCount`
- `durationMs`

Optional v1 field:

- `containerSha256`

### `speakers`

The `speakers` array MUST provide stable speaker identifiers used by transcript
entries.

### `transcript`

The `transcript` object MUST be the canonical transcript representation.

V1 transcript entries are word-timed items:

- `speaker`
- `startMs`
- `endMs`
- `text`

The embedded transcript is the source of truth.

Derived views such as readable transcript and captions MUST be treated as
optional derived material.

### `provenance`

The optional `provenance` object SHOULD record which systems generated Cassini's
metadata layers so users can inspect a file and see what produced it.

V1 producers SHOULD populate:

- `speechToText`
- `readableCleanup`
- `displayTranscript`

Each processing step MAY include:

- `backend`
- `engine`
- `model`
- `device`
- `language`
- `baseUrl`
- `host`
- `source`
- `version`

## Integrity Rules

Consumers MUST behave as follows:

### 1. No Cassini metadata present

Treat the file as plain audio.

### 2. Cassini metadata present and audio matches

Open as a full Cassini portable meeting.

### 3. Cassini metadata present and audio does not match

Treat embedded meeting metadata as stale.

Consumers SHOULD surface a message equivalent to:

```text
This audio no longer matches the embedded Cassini transcript metadata.
Open as plain audio or import as a new meeting.
```

### 4. Cassini metadata present but payload is malformed

Treat the file as damaged Cassini metadata over valid audio.

Consumers SHOULD allow plain-audio fallback.

## Casual Inspection

A casual file observer SHOULD be able to infer the format with tools such as:

```bash
ffprobe -v error -show_entries format_tags:stream_tags -of json meeting.opus
```

Expected visible cues:

- `CASSINI_FORMAT`
- `CASSINI_PAYLOAD_ENCODING`
- `CASSINI_DECODE_HINT`
- speaker and word counts
- human title/date tags

Manual decode flow:

```bash
python3 - <<'PY'
import base64, gzip, json, subprocess
import sys

path = sys.argv[1]
probe = subprocess.check_output([
    "ffprobe", "-v", "error",
    "-show_entries", "format_tags:stream_tags",
    "-of", "json", path,
], text=True)
doc = json.loads(probe)
tags = {}
tags.update(doc.get("format", {}).get("tags", {}))
for stream in doc.get("streams", []):
    tags.update(stream.get("tags", {}))
count = int(tags["CASSINI_PAYLOAD_CHUNK_COUNT"])
blob = "".join(tags[f"CASSINI_PAYLOAD_{i:03d}"] for i in range(count))
raw = gzip.decompress(base64.urlsafe_b64decode(blob + "=" * (-len(blob) % 4)))
print(raw.decode("utf-8"))
PY
./meeting.opus
```

## Producer Requirements

Cassini producers MUST:

- produce valid Ogg Opus audio
- write all required Cassini descriptor tags
- embed a manifest conforming to the v1 schema
- compute `CASSINI_AUDIO_PCM_SHA256` from the decoded PCM actually represented by the file
- ensure summary tags and manifest agree

Cassini producers SHOULD:

- write compact JSON before compression
- keep only canonical and useful derived data
- avoid embedding large redundant indexes in v1

## Consumer Requirements

Cassini consumers MUST:

- accept plain `.opus` files with no Cassini metadata as normal audio imports
- parse and verify required Cassini tags
- reconstruct the payload from chunk tags
- verify payload hash
- verify audio integrity fields before trusting transcript data

Cassini consumers SHOULD:

- offer plain-audio fallback on integrity mismatch
- expose meeting title/date/speaker count even before transcript rendering

## Initial Embedded Payload Policy

V1 SHOULD embed:

- meeting summary metadata
- speaker table
- canonical word transcript
- optional readable transcript
- optional chapter markers

V1 SHOULD NOT embed:

- search indexes
- browser build products
- redundant captions when they can be regenerated

## Why Not Use A Single Binary Blob Tag Only

Because the format should be legible.

V1 intentionally requires:

- explicit encoding tags
- explicit integrity tags
- explicit chunk count
- explicit decode hint

That makes the metadata self-describing enough that a curious user can inspect
and decode it without reading Cassini source code first.

## Rejected Alternatives

### ZIP-like `.cassini` package

Rejected as the primary format because it is not directly playable in ordinary
audio players.

### MP4/M4A with custom payload

Rejected for v1 because Cassini already prefers Opus and because Ogg/OpusTags
is easier to inspect casually from the command line.

### WebM audio file

Rejected for v1 because it is less obviously "just an audio file" than `.opus`
for ordinary users.

### Raw JSON in tags with no compression

Rejected because it creates unnecessary tag bloat and offers no practical
advantage in Cassini's expected size range.

## Rollout Plan

### Phase 1: Read support

- add `cassini inspect meeting.opus`
- add Cassini metadata detection for `.opus`
- add integrity verification and plain-audio fallback

### Phase 2: Export support

- add `cassini pack <meeting bundle> --out meeting.opus`
- emit the v1 tags and embedded manifest

### Phase 3: Native record flow

- add `cassini record --out meeting.opus`
- add `cassini record --into ./Archive`

### Phase 4: Archive UX

- add `cassini browse ./Archive`
- add `cassini add file.opus ./Archive`
- add `cassini share file.opus --out ./shared`

## One-Sentence Summary

Cassini v1 portable meetings should be ordinary `.opus` audio files with
self-describing, `gzip`-compressed Cassini JSON embedded in OpusTags and guarded
by strict audio integrity checks.

---

# v2 (Multi-Transcription)

Date: 2026-05-12

Status: Spec landed. Producer and viewer support roll out behind a feature flag.

## What v2 changes

v2 lets a single `.opus` file carry **multiple transcripts of the same audio**
(e.g. parakeet + canary, or raw ASR + human-corrected) so the viewer can switch
or compare without producing duplicate files. The audio profile, integrity
rules, OpusTag transport, and decode steps from v1 stay the same. The only
structural change is in how transcript bodies are addressed.

## File identification

A v2 file MUST set:

- `CASSINI_FORMAT=org.cassini.portable-meeting/2`
- `CASSINI_PAYLOAD_SCHEMA=https://cassini.local/spec/cassini-portable-meeting-manifest-v2.schema.json`

All other v1 descriptor tags still apply.

## Manifest shape (v2)

The decompressed `CASSINI_PAYLOAD_*` body MUST conform to
[`spec/cassini-portable-meeting-manifest-v2.schema.json`](../spec/cassini-portable-meeting-manifest-v2.schema.json).
Notable differences from v1:

| v1 | v2 |
|---|---|
| `transcript: object` (single, required) | `transcripts: array` (1..N), required |
| `readableTranscript: object` (optional, single) | `readableTranscripts: array` (0..N), optional |
| `provenance.speechToText: ProcessingStep` | `provenance.speechToText: map<transcriptId, ProcessingStep>` |
| `provenance.readableCleanup: ProcessingStep` | `provenance.readableCleanup: map<transcriptId, ProcessingStep>` |
| transcript body inlined under `transcript.items` | each transcript body lives in its own OpusTag chunk set; the manifest entry holds a `payloadRef` |

The transcript `id` doubles as the provenance map key. A transcript entry with
`id: "canary"` resolves to `provenance.speechToText.canary`.

A transcript entry:

```jsonc
{
  "id":      "canary",                    // ^[a-z0-9][a-z0-9_-]{0,31}$, unique in file
  "role":    "raw-asr",                   // raw-asr | human-corrected | translation
  "default": true,                        // exactly one default per role
  "format":  "cassini.words.v1",
  "language": "en",
  "wordCount": 9224,
  "createdAtUtc": "2026-05-12T...",
  "payloadRef": {
    "prefix":     "CASSINI_TX_CANARY_PAYLOAD_",
    "chunkCount": 14,
    "sha256":     "...",                  // sha256 of decompressed body JSON
    "rawBytes":   718432,
    "gzipBytes":  221110,
    "mime":       "application/vnd.cassini.transcript-words+json",
    "encoding":   "base64url+gzip+utf8json"
  }
}
```

Readable transcripts use the same shape with `role` from `{readable-cleanup,
display}` and a required `sourceTranscriptId` pointing at the raw-ASR entry
they derive from.

## OpusTag layout (v2)

In addition to the v1 tags:

```text
CASSINI_FORMAT=org.cassini.portable-meeting/2
CASSINI_PAYLOAD_SCHEMA=https://cassini.local/spec/cassini-portable-meeting-manifest-v2.schema.json

# main manifest (index + provenance + meeting metadata; small)
CASSINI_PAYLOAD_*

# discoverable from ffprobe alone
CASSINI_TRANSCRIPT_IDS=<comma-separated ids>
CASSINI_TRANSCRIPT_DEFAULT=<id of default raw-asr transcript>

# one chunk set per transcript body; the UPPER_SNAKE form of the id replaces -
CASSINI_TX_<UPPER_ID>_PAYLOAD_MIME=application/vnd.cassini.transcript-words+json
CASSINI_TX_<UPPER_ID>_PAYLOAD_ENCODING=base64url+gzip+utf8json
CASSINI_TX_<UPPER_ID>_PAYLOAD_CHUNK_COUNT=<N>
CASSINI_TX_<UPPER_ID>_PAYLOAD_SHA256=<hex>
CASSINI_TX_<UPPER_ID>_PAYLOAD_RAW_BYTES=<N>
CASSINI_TX_<UPPER_ID>_PAYLOAD_GZIP_BYTES=<N>
CASSINI_TX_<UPPER_ID>_PAYLOAD_000..N=<chunks>
```

Each per-transcript chunk set follows the same base64url + gzip + UTF-8 JSON
encoding as v1's main payload. Bodies remain `cassini.words.v1` JSON unchanged
— v2 is purely a transport change for them.

## Producer rules (v2)

Producers writing v2 files MUST:

- Emit exactly one `default: true` transcript per role.
- Reject transcript ids that match reserved v1 descriptor names: `payload`,
  `format`, `audio`, `meeting`, `integrity`, `transcript`, `provenance`,
  `summary`, `attachments`.
- Compute `payloadRef.sha256` against the decompressed body JSON bytes
  (not the gzip or base64url forms).
- Include a `provenance.speechToText.<id>` entry for every transcript entry
  with `role: raw-asr`; same shape for cleanup-role entries under
  `readableCleanup`.

Producers MUST NOT write a top-level `transcript` field in v2 files. v1
consumers reading a v2 file SHOULD detect `version: 2` and surface a
"newer format" message rather than guessing at the index shape.

## Consumer rules (v2)

v2 consumers MUST:

- Detect `version: 2` and use the v2 loader path.
- For each transcript displayed, resolve the `payloadRef` to its OpusTag chunk
  set, concatenate, base64url-decode, gzip-decompress, parse as UTF-8 JSON,
  and verify the SHA-256 matches `payloadRef.sha256`.
- Honor the `default: true` flag when picking the initial transcript.
- Continue to read v1 (`version: 1`) files as a single-transcript virtual
  array (synthesized `raw-asr` entry from `transcript`, optional synthesized
  `readable-cleanup` from `readableTranscript`).

## Reserved transcript ids

The producer MUST reject any transcript id from this list (case-insensitive
after lowercasing) to avoid OpusTag namespace collisions:

```text
payload, format, audio, meeting, integrity, transcript, provenance,
summary, attachments
```

---

## Why `.meeting` is not a contract

The build pipeline still uses a `.meeting` bundle directory as an intermediate
working form. That directory carries two internal manifest files:

- `cassini.json` — the bundle envelope (`cassini.meeting.v1`)
- `manifest.json` — the artifact manifest (`cassini.meeting-artifact.v1`)

These are **build scratch, not a published format**. The only durable Cassini
deliverable is the `.opus` portable meeting file with its embedded
`org.cassini.portable-meeting/2` manifest. The `.meeting` bundle and its two
manifest schemas exist purely to stage the pack into `.opus`; they are
deliberately not documented as a consumer contract and are scheduled to be
retired once the build/publish flows no longer depend on them. Do not treat the
`.meeting` bundle, `cassini.json`, or the bundle `manifest.json` as a stable
interface.

---

## How the operator guarantees the file exists

The format above says what a portable meeting *is*. This section says what makes
the operator's copy of it trustworthy, because for a long time it was not: the
`.opus` was packed by a background task started after the recording had already
been handed off for publishing, so it could fail, be killed by a restart, or be
overtaken by a rerun writing the same path — and none of that failed the job.

Sealing is now a stage the job has to pass, and everything downstream is bound to
the artifact it produced:

```text
  build            attempt .meeting bundle
    │
  SEAL             cassini pack  ->  runs/<job>--attempt-NNN.seal/<jobID>.opus
    │                  │  pack verifies its own output: it re-decodes the packed
    │                  │  file and compares the PCM SHA-256 against the manifest
    │                  │  it embedded, so a zero exit means packed AND checked
    │                  ▼
    │              sha256(file)  ────────────┐  the sealed digest
    │                  │                     │
    │              promote (atomic rename)   │
    │                  ▼                     │
    │              current/<jobID>.opus      │
    │                                        │
  PUBLISH          re-check sha256 ──────────┤  before anything is spawned
    │                  ▼                     │
    │              cassini publish <sealed .opus>  (verified, then copied
    │                  ▼                      through byte for byte)
  DELIVER          re-check sha256 of the staged copy ──┘
                       ▼
                   commit asset, then catalog LAST
```

Three properties follow, and each is worth stating on its own:

- **A publish cannot exist without a seal.** The only transition into
  `publish/queued` writes the sealed artifact's path and digest in the same
  database transaction.
- **The artifact is immutable and attempt-scoped.** A rerun seals its own file;
  it never rewrites the one a queued publish is about to deliver.
  `current/<jobID>.opus` is a promotion of a sealed artifact — a hard link where
  the filesystem allows one — not an independent pack of the same meeting.
- **The file that reaches the viewer is the file that was sealed.** The digest is
  a SHA-256 of the container bytes. That is a different question from
  `integrity.pcmSha256`, which hashes decoded audio, survives a remux, and is
  what the meeting id is derived from; both are kept because they answer
  different questions — "is this the same recording?" and "is this the same
  file?".

