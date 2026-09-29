# Portable transcription documents

`cassini.transcription.v1` is a self-contained JSON representation of a meeting
whose active audio has been removed. The usual filename is
`<meeting>.cassini.transcription.json`; readers detect content rather than relying
on its filename or HTTP MIME type. The schema lives in
`spec/cassini-transcription-v1.schema.json`.

```
validated Opus -> complete current metadata -> retained JSON
                 + exact transcript bytes    + original audio identity
                 + comments and attachments  + annotation checkpoint
```

`media.state` is authoritative for playback. The original manifest's audio fields
are historical provenance, never a fallback audio source. The manifest preserves
unknown fields; each referenced transcript body is stored as standard base64 with
its exact decoded-byte SHA-256, MIME type and length. Duplicate extra comments
remain separate array entries. Readers verify all payloads, including languages
that are not currently selected. Documents and total decoded payloads are bounded
to 64 MiB; the Go decoder also rejects duplicate keys and excessive nesting.

The original audio digest continues to bind time-range annotations. JSON hashes
and ETags describe storage revisions; they are not new audio identities. A
checkpoint records the exact saved annotation token/revision. It cannot recover
edits acknowledged by an installation but not yet persisted to Nextcloud. A
foreign installation must not interpret its numeric snapshot IDs as local IDs.
Annotation rewrites preserve unknown envelope/manifest members and payload bytes.

Explicit local extraction, without mutating the input:

```sh
./bin/cassini extract transcription \
  --out /tmp/example.cassini.transcription.json \
  --age-anchor 2026-06-01T12:00:00Z \
  --anchor-source recording-completed \
  example.opus
./bin/cassini inspect /tmp/example.cassini.transcription.json
./bin/cassini inspect --transcript /tmp/example.cassini.transcription.json
```

The output path must not already exist. Extraction validates the complete Opus
stream and prints a payload preservation inventory. Unsupported binary
attachments, unreferenced payload chunks, unsupported annotations and unknown
OpusTags extensions block extraction rather than silently losing data. The
current implementation accepts text/JSON attachments; other attachment types
need explicit classification before conversion can support them.

No historical Nextcloud versions are imported or purged. Existing embedded
history remains in the current manifest. Independently saved downloads and
Nextcloud recovery history follow their own retention policies.
