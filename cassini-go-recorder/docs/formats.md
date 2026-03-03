# Packet Log Formats

## Invariant

The source-of-truth output must stay append-only and semantic-lossless.

- no packet drops in recorder path
- monotonically increasing receive timestamps
- stable identifiers that allow offline remuxing

## On-disk session layout (target)

The target is `sessions/<session_id>/` with:

- `session.json`: indexed metadata and event pointers
- `sdp/`: SDP snapshots (`000_offer.sdp`, `001_answer.sdp`, ...)
- `streams/<stream_id>.rtplog`: raw RTP/RTCP packets with monotonic receive timestamps
- `streams/<stream_id>.idx`: optional index (`recvMonoNS + fileOffset`)
- `events.ndjson`: append-only event stream (`join`, `stream_opened`, etc.)
- `artifacts/remux/...`: optional derivatives

## rtplog format

This is the preferred stream truth container for future iterations:

- magic: `RTPL0\0\0\0`
- version: `u16`
- flags: `u16`
- header length: `u32`
- header JSON (`StreamHeader`)
- variable-length packet records

Each packet record:

- recvMonoNS: `u64`
- kind: `u8` (`1` RTP, `2` RTCP)
- wireLen: `u32`
- wireBytes: `wireLen`
- crc32: optional `u32`

## Current migration status

- current code still uses `internal/cassette` for `.csr` capture
- new `pkg/core/store` is available and includes a schema-compatible writer/reader
- full migration to multi-file session layout is staged
