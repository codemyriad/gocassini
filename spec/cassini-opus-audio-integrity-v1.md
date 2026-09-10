# Cassini compressed Opus audio integrity v1

Portable meeting manifest v1 identifies its recording with
`integrity.matchPolicy = "exact-opus-audio-v1"` and a lowercase hexadecimal
SHA-256 in `integrity.opusAudioSha256`.

The digest covers the compressed Opus audio essence, not decoded PCM and not
the complete `.opus` file. Whole-file hashing cannot be embedded in the same
file without becoming self-referential: writing the digest changes OpusTags,
which changes the whole-file digest. The operator may still keep a separate
whole-file delivery digest outside the recording.

## Canonical byte stream

Hash these byte strings in order:

1. The ASCII domain separator `org.cassini.opus-packets/1`, followed by one
   zero byte.
2. The first logical packet (`OpusHead`) after validating it. Set bytes 12–15,
   the informational original input sample rate, to zero. Prefix the packet
   with byte `H` and its length as an unsigned little-endian 64-bit integer.
3. Every compressed audio packet in order. Prefix each packet with byte `A`
   and its length as an unsigned little-endian 64-bit integer.
4. A 17-byte trailer: byte `E`, the audio packet count as an unsigned
   little-endian 64-bit integer, and the normalized playable sample count as a
   second unsigned little-endian 64-bit integer.

The second logical packet (`OpusTags`) is excluded. Ogg page boundaries,
serial number, sequence number, lacing, CRC, and granule positions are also
excluded. FFmpeg can normalize granule positions during a metadata-only Ogg
stream copy even when OpusHead, every compressed packet, and decoded output are
unchanged. The parser still validates the Ogg structure and CRC before
accepting a stream.

Packet lengths preserve packet boundaries. OpusHead keeps playback-relevant
values including channels, pre-skip, output gain, channel mapping family, and
mapping table.

The parser sums packet durations at Opus's fixed 48 kHz clock, subtracts
pre-skip, and clamps that value to `finalGranule - preSkip` when the latter is
smaller. This normalized playable sample count is included in the digest. It
keeps a real end trim identity-relevant while tolerating muxer granules beyond
the compressed packet timeline. Manifest shape fields repeat the same count
and its derived duration even though the digest intentionally ignores raw Ogg
framing.

This profile accepts one non-chained Ogg logical stream containing one or two
Opus channels. It rejects malformed headers, CRC failures, sequence gaps,
invalid continuation, multiplexed or chained streams, truncated packets,
missing EOS, and invalid final granules.

Metadata-only remuxes must preserve the digest. Re-encoding audio normally
changes it even when the result sounds equivalent; content-derived meeting IDs
derive from this digest for the same reason.

## Three digests, three jobs

Three SHA-256 digests describe a Cassini recording. They answer different
questions, and none stands in for another.

| Digest | Covers | Changes when | Used for |
|---|---|---|---|
| **Seal** | the whole `.opus` the pipeline produced | never, for a given attempt | proving the sealed artifact is intact before it is delivered |
| **Audio** (`integrity.opusAudioSha256`) | the canonical byte stream above; OpusTags excluded | only when the audio itself changes | **identity** — "is this the same recording" — and the binding annotations are pinned to (`manifest.annotations.audioOpusSha256`) |
| **Container** | the whole file as it is now | every metadata rewrite: each annotation commit, each carry | saying which bytes something was read from (the archive's `OC-Checksum`, the annotations index). **Never identity, never compared with the seal** |

Writing annotations is a metadata-only rewrite: it replaces the manifest in
OpusTags, copies every audio packet unchanged, and verifies that its output's
audio digest equals its input's before anything is written. Annotations
therefore preserve the audio digest and move the container digest. It follows
that:

- A delivered copy whose container digest no longer equals the seal is neither
  corrupt nor a different recording. It has been annotated.
- Anything that compares two copies of a recording — an archive copy with a
  local one — compares audio digests. Equal container digests remain sufficient
  proof of sameness, never necessary proof.
- The seal digest is checked against the sealed file and nothing else. A
  republish delivers the sealed audio with the delivered copy's annotations
  carried into a copy of it (`cassini annotate carry`): the seal check proves
  the audio half, carry's own verification the annotations half.
