# Cassini Portable Meeting Format

Date: 2026-09-02

Status: published, version 1

The published specification at <https://cassini-format.codemyriad.io/> is the
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
CASSINI_PAYLOAD_SCHEMA=https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json
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

Transcript bodies are not stored inline. `transcripts` indexes raw,
human-corrected, and translated bodies. `readableTranscripts` optionally
indexes display bodies (and, in files written before it was withdrawn, cleanup
bodies).

Example descriptor:

```json
{
  "id": "raw-asr",
  "role": "raw-asr",
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
contains no whitespace; paragraph text belongs in a readable or display body.

Display entries retain their native JSON document: `transcript.display.v1` with
`blocks`. The entry's `format`, role, and MIME identify which body it carries;
all body kinds use the same chunk and integrity mechanism below.

The `readable-cleanup` role is **withdrawn**. It was written by an LLM cleanup
step that no longer exists, and no producer emits one. The role stays in the v1
schema, marked deprecated, so a file written before the withdrawal is still a
valid v1 document. A reader **skips** such an entry and reads the file's raw
transcript as normal; it must not reject the file, because the audio and the
word transcript are unaffected by a body nobody writes any more. The same
applies to `provenance.readableCleanup`.

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

`raw-asr` and `scripted` entries come directly from the recording and do not
set `sourceTranscriptId`. `human-corrected` and `translation` entries require
it. Display entries also require it and name the words transcript they came
from. When switching words transcripts, consumers should
use only a derived entry whose source id matches the newly selected entry.

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

## Build bundles are not portable contracts

The pipeline also uses `.meeting` directories containing `cassini.json`,
`manifest.json`, audio, and intermediate transcript artifacts. Those
directories are transient build inputs. The `.opus` file described here is the
single durable, shareable meeting artifact.
