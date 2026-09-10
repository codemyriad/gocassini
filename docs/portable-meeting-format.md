# Cassini Portable Meeting Format

Date: 2026-09-02

Status: published, version 1

The published specification at <https://format.gocassini.com/> is the
portable meeting contract. A portable meeting is one normal `.opus` file that
plays in ordinary audio software and also carries the meeting transcript,
speaker roster, provenance, summary metadata, and attachments.

## Wire identity

A conforming file has all of these properties:

- Ogg container with one Opus audio stream
- `CASSINI_FORMAT=org.cassini.portable-meeting/1`
- a main manifest with `kind="cassini-portable-meeting"`, `version=1`, and
  `profile="ogg-opus"`
- at least one entry in `transcripts`
- `integrity.matchPolicy="exact-opus-audio-v1"`

If `CASSINI_FORMAT` is absent, consumers should treat the file as ordinary
audio. If the format tag, manifest version, profile, transcript layout, or
integrity policy is unsupported, consumers must stop and report that clearly;
they must not guess at another shape.

The JSON Schema is
[`spec/cassini-portable-meeting-manifest-v1.schema.json`](../spec/cassini-portable-meeting-manifest-v1.schema.json).

## Metadata layout

The OpusTags block contains ordinary player-facing metadata and Cassini's
structured payload descriptors.

Recommended ordinary tags:

- `TITLE`
- `DATE`
- `DESCRIPTION`
- `ENCODER`
- `LANGUAGE`

Recommended description:

```text
Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON.
```

Required main-payload tags:

```text
CASSINI_FORMAT=org.cassini.portable-meeting/1
CASSINI_PROFILE=ogg-opus
CASSINI_PAYLOAD_MIME=application/vnd.cassini.portable-meeting+json
CASSINI_PAYLOAD_ENCODING=base64url+gzip+utf8json
CASSINI_PAYLOAD_SCHEMA=https://format.gocassini.com/schema/cassini-portable-meeting-manifest-v1.schema.json
CASSINI_PAYLOAD_CHUNK_COUNT=<N>
CASSINI_PAYLOAD_SHA256=<sha256 of decompressed JSON bytes>
CASSINI_PAYLOAD_RAW_BYTES=<decompressed byte count>
CASSINI_PAYLOAD_GZIP_BYTES=<compressed byte count>
CASSINI_PAYLOAD_000=<first base64url chunk>
...
```

Required audio-integrity tags:

```text
CASSINI_AUDIO_MATCH_POLICY=exact-opus-audio-v1
CASSINI_AUDIO_OPUS_SHA256=<canonical compressed Opus digest>
CASSINI_AUDIO_SAMPLE_RATE=48000
CASSINI_AUDIO_CHANNELS=<1 or 2>
CASSINI_AUDIO_SAMPLE_COUNT=<playable sample count>
CASSINI_AUDIO_DURATION_MS=<duration in milliseconds>
```

The full audio digest algorithm is defined in
[`spec/cassini-opus-audio-integrity-v1.md`](../spec/cassini-opus-audio-integrity-v1.md).

Useful summary mirrors include `CASSINI_MEETING_ID`,
`CASSINI_CREATED_AT`, `CASSINI_SPEAKER_COUNT`,
`CASSINI_TRANSCRIPT_DEFAULT`, and `CASSINI_TRANSCRIPT_IDS`. Origin and
processing mirrors such as `CASSINI_ROOM_ID`, `CASSINI_JOB_ID`,
`CASSINI_ATTEMPT_NUMBER`, and `CASSINI_STT_*` are optional. The manifest is
authoritative whenever a mirror disagrees with it.

## Encoding and chunking

The main manifest and every transcript body use the same pipeline:

1. Serialize UTF-8 JSON.
2. Compute SHA-256 over those exact JSON bytes.
3. Compress with gzip.
4. Encode with unpadded RFC 4648 base64url.
5. Split the encoded text into numbered chunks, normally no larger than 4096
   characters.

Chunks start at `000`, are zero-padded to three digits, and are concatenated
in numeric order with no separator. Readers verify the declared chunk count,
byte counts, and SHA-256 before using decoded content.

## Main manifest

The main payload is an index. Its required top-level fields are:

```json
{
  "kind": "cassini-portable-meeting",
  "version": 1,
  "profile": "ogg-opus",
  "meeting": {},
  "audio": {},
  "integrity": {},
  "speakers": [],
  "transcripts": []
}
```

The optional top-level members are `readableTranscripts`, `provenance`,
`summary`, `attachments`, and `annotations` (see
[Annotations](#annotations-tags-and-marks)).

Transcript bodies are not stored inline. `transcripts` indexes word transcripts;
`readableTranscripts` optionally indexes display bodies. Word entries have no
origin role or derivation link. Older `role` and `sourceTranscriptId` members
on word entries are ignored.

Example descriptor:

```json
{
  "id": "raw-asr",
  "default": true,
  "format": "cassini.words.v1",
  "language": "en",
  "wordCount": 9224,
  "createdAtUtc": "2026-09-02T10:15:00Z",
  "payloadRef": {
    "prefix": "CASSINI_TX_RAW_ASR_PAYLOAD_",
    "chunkCount": 14,
    "sha256": "<64 lowercase hex characters>",
    "rawBytes": 718432,
    "gzipBytes": 221110,
    "mime": "application/vnd.cassini.transcript-words+json",
    "encoding": "base64url+gzip+utf8json"
  }
}
```

Transcript ids match `^[a-z0-9][a-z0-9-]{0,31}$`. Producers normally form the tag prefix
by uppercasing the id and replacing hyphens with underscores. For example,
`raw-asr` maps to `CASSINI_TX_RAW_ASR_PAYLOAD_`.

Words transcript bodies have this shape:

```json
{
  "format": "cassini.words.v1",
  "language": "en",
  "wordCount": 2,
  "items": [
    { "speaker": "spk_1", "startMs": 0, "endMs": 320, "text": "Hello" },
    { "speaker": "spk_1", "startMs": 340, "endMs": 710, "text": "world" }
  ]
}
```

Each `items[]` entry is exactly one timed word. Its `text` is non-empty and
contains no whitespace; paragraph text belongs in a display body.

Display entries retain their native JSON document: `transcript.display.v1` with
`blocks`. The entry's `format`, role, and MIME identify which body it carries;
all body kinds use the same chunk and integrity mechanism below.

Readers skip unrecognised readable roles, including the withdrawn
`readable-cleanup` entries in older files. The schema no longer defines that
role or `provenance.readableCleanup`; both are ignored as unknown metadata.
The audio and word transcripts remain usable.

The body tags repeat the descriptor metadata:

```text
CASSINI_TX_RAW_ASR_PAYLOAD_MIME=application/vnd.cassini.transcript-words+json
CASSINI_TX_RAW_ASR_PAYLOAD_ENCODING=base64url+gzip+utf8json
CASSINI_TX_RAW_ASR_PAYLOAD_CHUNK_COUNT=<N>
CASSINI_TX_RAW_ASR_PAYLOAD_SHA256=<sha256>
CASSINI_TX_RAW_ASR_PAYLOAD_RAW_BYTES=<bytes>
CASSINI_TX_RAW_ASR_PAYLOAD_GZIP_BYTES=<bytes>
CASSINI_TX_RAW_ASR_PAYLOAD_000=<first chunk>
...
```

The descriptor in the manifest is authoritative. Readers may warn when the
mirrored tags disagree, but must use the descriptor's prefix, chunk count,
digest, and byte counts.

## Default selection and provenance

For the raw transcript shown first, readers select the entry marked
`default=true`, or the first words entry when none is marked. The
`CASSINI_TRANSCRIPT_DEFAULT` tag is a discoverability copy: a disagreement
should be reported, but the manifest still wins.

Display entries require `role: "display"` and a `sourceTranscriptId` naming a
words transcript. When selecting or switching words transcripts, first filter
displays by that source id, then prefer `default: true`, then array order. If
none matches, show the words without a display. Cassini retains `raw-asr` as
its single-transcript id for compatibility; ids are opaque and imply no role.

The reference schema also omits unused `chapters` and the duplicate meeting
hints `summary`, `language`, and `roomName`. The supported top-level summary
and attachments, entry/body language, and `meeting.roomId` remain. Older files
with the retired members still open because readers ignore unknown metadata.

A speech-to-text step may carry a `hints` record describing the decoder biasing
that ran. It is absent when the pass ran unbiased; `applied: false` with a
`reason` means a vocabulary was configured but could not be used, which is a
different thing from no vocabulary at all and must not be reported as success.

Processing provenance is keyed by transcript id:

```json
{
  "provenance": {
    "speechToText": {
      "raw-asr": {
        "backend": "local-asr",
        "engine": "asr-engine",
        "model": "meeting-model",
        "device": "cpu",
        "language": "en"
      }
    }
  }
}
```

## Audio identity

The canonical SHA-256 covers playback-relevant `OpusHead` fields, compressed
audio packets with their boundaries, and normalized playable sample count. It
excludes OpusTags and raw Ogg framing, so a metadata-only remux can update the
manifest without creating a circular digest. Re-encoding audio normally creates
a different identity.

Readers must fail closed on a missing digest, unknown policy, manifest/tag
disagreement, malformed Ogg stream, digest mismatch, or audio-shape mismatch.

## Annotations: tags and marks

A manifest may carry an optional top-level `annotations` member: the tags people
and agents have put on the meeting, and on stretches of it. A **tag** is a label
such as `hiring`. A **mark** is one use of a tag, on the whole meeting or on a
time range. Marks live inside the file so that a recording stays complete when
it leaves the app: downloaded, shared, or handed to someone with no Nextcloud.

```json
"annotations": {
  "format": "cassini.annotations.v1",
  "revision": 4,
  "audioOpusSha256": "8e1f7499c6d5fba88c3bd9b69ecd3de1b07ae0cff65152c942c5e99062d01cbc",
  "tagNamespace": "urn:uuid:07e4eab6-4f5f-4cc4-8913-46432c5cd726",
  "tags": [
    { "id": "tag_k3v9q2m7x4d8w1pz", "label": "hiring" }
  ],
  "items": [
    { "id": "mk_01J9ZB6Q4H7T2N8K3M5P0R1S2V", "tagId": "tag_k3v9q2m7x4d8w1pz",
      "target": { "kind": "meeting" },
      "createdAtUtc": "2026-09-10T11:23:54Z",
      "actor": { "kind": "person", "id": "alice" },
      "operationId": "op_01J9ZB6Q4H7T2N8K3M5P0R1S2W" },
    { "id": "mk_01J9ZB6Q4H7T2N8K3M5P0R1S2X", "tagId": "tag_k3v9q2m7x4d8w1pz",
      "target": { "kind": "time-range", "startMs": 869000, "endMs": 884000 },
      "createdAtUtc": "2026-09-10T11:24:10Z",
      "actor": { "kind": "agent", "id": "alice" },
      "operationId": "op_01J9ZB6Q4H7T2N8K3M5P0R1S2Y" }
  ]
}
```

### Reader tolerance

The member is optional, and absent means no marks. It carries its own
`format`. A reader that does not recognise the format ignores the whole member.
A reader that recognises it but cannot read it shows no marks. In both cases
the audio and transcripts stay usable: no reader refuses a recording because of
its annotations. A tool that rewrites other metadata carries the member through
unchanged, including a format it cannot read.

### Rules

- **`revision`** is at least 1 and goes up by one for each committed batch. It
  is informational only. Concurrent writers are serialised by a conditional
  write on the file, not by this counter.
- **`tagNamespace`** is `urn:uuid:<lowercase uuid>`. It is set on a file's first
  write and never changed. The same tag across recordings is the same
  `(tagNamespace, id)` pair, and one installation uses one namespace.
- **Ids** are unique within the document and match
  `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`. Every `items[].tagId` names a tag in the
  same document. Tag ids are random rather than time-ordered, so an id reused
  across recordings reveals nothing about when, or whether, someone else used
  that label.
- **Labels** are 1–64 characters, already trimmed, with no control characters.
  A writer matching a label to an existing tag trims it and ignores case.
- **`actor.id`** is the authenticated Nextcloud user who made the mark. It is
  never a value taken from a request. **`actor.kind`** is `person` or `agent`
  today, and the caller declares it: it is attribution for display and undo,
  not an access control. Readers tolerate kinds they do not know.
- **`operationId`** groups the marks one batch added, so a whole run (usually an
  agent's) can be undone in one step.
- **`createdAtUtc`** is an RFC 3339 time in UTC, ending in `Z`.

### Targets

| `target.kind` | Shape | Means |
|---|---|---|
| `meeting` | `{"kind": "meeting"}`, with no times | The whole meeting |
| `time-range` | `{"kind": "time-range", "startMs": …, "endMs": …}` | The half-open range `[startMs, endMs)` |

Time ranges are integers in elapsed playback milliseconds after Opus pre-skip,
which is the clock the word timings use. They must satisfy
`0 ≤ startMs < endMs ≤ audio.durationMs`. Negative, empty and out-of-bounds
ranges are invalid. A range covers all speech in it: marking one speaker's turn
does not exclude someone talking over them. Marks are anchored to time, never to
a segment id or a text offset, so they survive re-transcription.

A whole-meeting mark is its own kind rather than `[0, durationMs)`. "This
meeting is about hiring" and "this range, which happens to span the whole
recording, is about hiring" are different claims.

### Binding: resolved and unresolved marks

`audioOpusSha256` is the `integrity.opusAudioSha256` of the audio the marks were
made against. That digest excludes OpusTags (see [Audio identity](#audio-identity)),
so adding a mark rewrites the file without changing the audio digest.

- When `annotations.audioOpusSha256` equals `integrity.opusAudioSha256`, the
  marks are **resolved**.
- When the two differ, the marks are **unresolved**: they were made against
  different audio, for example before a re-run changed it. A reader must not
  draw their time ranges against this audio. Unresolved marks are kept, not
  moved, and a writer refuses to add new marks beside them.

### Limits

A recording carries at most 200 tags and 2,000 marks. A thousand marks measure
about 52.6 KiB, so the document stays inline in the main payload.

### Canonical order

Writers emit tags ordered by label (ignoring case), then id. Marks with meeting
targets come first, then the rest ordered by `startMs`, `endMs` and id. Two
writers producing the same marks therefore produce the same bytes, and
successive revisions diff readably. Readers must not depend on the order.

### Privacy

Marks travel with the file. A recording shared outside Nextcloud carries who
marked what and when, as the Nextcloud user id of each mark's author, and the
tag labels people chose. The file already carries speaker names. Before sharing
a marked recording, treat it as carrying the ids of the people who marked it.

## Producer and reader commands

Pack a meeting bundle:

```bash
cassini pack ./meeting.meeting --out ./meeting.opus
```

Inspect and verify a portable meeting:

```bash
cassini inspect ./meeting.opus
```

Extract the default words transcript:

```bash
cassini inspect --transcript ./meeting.opus > transcript.words.v1.json
```

Retag identity metadata without re-encoding audio:

```bash
cassini retag ./meeting.opus --out ./meeting-retagged.opus \
  --room-id rm_9f2a1c3d4e5b6a70
```

Retagging must validate the input contract, preserve every transcript chunk
set and extension field, rebuild the main-payload digest and descriptors, copy
the Opus stream, and verify the staged output before replacing anything.

`cassini inspect` prints one `annotations` line when a file carries marks: the
revision, tag and mark counts, and whether the marks are resolved against this
audio. A format it does not read is reported as `status=unsupported-format`.

### Tags and marks

`cassini annotate` reads and writes the [annotations](#annotations-tags-and-marks)
member without re-encoding the audio:

```bash
cassini annotate show  ./meeting.opus [--json]
cassini annotate apply ./meeting.opus --ops ./ops.json --actor-id alice \
  [--actor-kind person|agent] [--operation-id <id>] [--expect-revision N] \
  [--tag-namespace urn:uuid:<uuid>] [--out ./marked.opus] [--json]
cassini annotate carry ./delivered.opus ./sealed.opus --out ./staged.opus [--json]
```

`apply` reads a `{"ops": […]}` document from the file named by `--ops`, or from
stdin with `--ops -`. It applies the ops in order, as one batch and one rewrite:

| Op | Shape | Effect |
|---|---|---|
| `mark` | `{"op": "mark", "tag": {"id"?, "label"}, "target": {…}}` | Finds the tag by id if one is given, otherwise by label (trimmed, ignoring case) within the file, otherwise mints one. Then adds the mark. A mark identical to an existing one (same tag, same target) is a no-op, so a retry cannot duplicate it |
| `unmark` | `{"op": "unmark", "itemId"}` | Removes one mark. An unknown id is a no-op and is reported in `notFound` |
| `unmark-tag` | `{"op": "unmark-tag", "tagId", "target"?}` | Removes every mark of that tag in this file, or only those with that target |
| `undo-operation` | `{"op": "undo-operation", "operationId"}` | Removes every mark stamped with that operation id: the bulk undo of an agent run |
| `relabel` | `{"op": "relabel", "tagId", "label"}` | Renames the tag in this file only |

After the ops run, tags left with no marks are dropped. New marks are stamped
with `createdAtUtc`, the actor (`--actor-id`, with `--actor-kind`), and the
operation id: `--operation-id` if given, otherwise a minted one. A file's first
write sets its binding to the file's own audio digest and its namespace to
`--tag-namespace`. `--expect-revision` refuses the batch unless the document is
at that revision.

Nothing is written unless all of these hold:

- the output's audio digest equals the input's;
- the manifest without `annotations` is unchanged;
- the written `annotations` equal the intended document in canonical order;
- the document validates.

`carry` is for republishing. It reads the marks off the delivered copy and
writes them into a copy of the sealed file at `--out`. The sealed file itself is
never modified. If both files have the same audio digest, the marks carry over
resolved. If the audio differs, they carry over with their original binding and
are reported `resolved: false`. Annotations already in the sealed file are
replaced.

| Exit | Means |
|---|---|
| `0` | Done |
| `1` | Runtime failure |
| `2` | Usage error |
| `3` | `--expect-revision` did not match |
| `4` | The ops are invalid, or the document they would produce fails validation |
| `5` | The document is unresolved; `apply` will not add marks against audio they were not made for |

With `--json`, all three subcommands print one result document:

```json
{
  "format": "cassini.annotate.result.v1",
  "annotations": { … },
  "revision": 5,
  "operationId": "op_…",
  "added": ["mk_…"], "removed": ["mk_…"], "notFound": [],
  "carried": 0,
  "resolved": true,
  "audioOpusSha256": "…",
  "containerSha256": "…"
}
```

`annotations` is the document the file now carries, or `null`.
`containerSha256` is the digest of the file as written. It identifies these
exact bytes and changes with every mark. It is never the recording's identity,
which is the audio digest.

## Build bundles are not portable contracts

The pipeline also uses `.meeting` directories containing `cassini.json`,
`manifest.json`, audio, and intermediate transcript artifacts. Those
directories are transient build inputs. The `.opus` file described here is the
single durable, shareable meeting artifact.
