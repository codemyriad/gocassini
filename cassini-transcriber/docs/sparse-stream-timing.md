# Sparse Stream Timing

## Problem

Cassini meeting MKVs can contain audio streams whose packet timestamps are sparse across the full meeting timeline.

That means a stream may:

- start late,
- contain large internal gaps,
- end early,
- still belong on the full meeting clock.

Example failure modes observed in real meetings:

- one speaker's first audio packet can arrive many minutes after meeting start,
- another speaker can have hundreds of seconds of packet-time gap inside one stream,
- `ffprobe` stream `duration` can describe the meeting span while the summed packet audio is much shorter.

## Consequence

A naive decode like:

```bash
ffmpeg -i meeting.mkv -map 0:7 track.wav
```

produces compact PCM. It keeps only decoded samples and drops sparse packet-time gaps.

If that compact WAV is then treated as if it still lives on the original meeting timeline, the pipeline becomes wrong in two ways:

- transcript words are shifted earlier than they happened,
- meeting-level silence compression is planned against the wrong time ranges.

This is enough to produce impossible outputs such as a speaker appearing in the transcript before their first packet exists in the MKV.

## Required Model

The pipeline must distinguish:

- `packet timeline`: timestamps carried by the MKV stream packets,
- `sample timeline`: the compact decoded PCM after gaps are removed.

The transcriber must operate on a PCM representation that preserves the packet timeline.

## Working Decode Strategy

For analysis and chunk extraction, decode each speaker stream into a gap-preserving PCM timeline:

```bash
ffmpeg -i meeting.mkv -map 0:<stream> \
  -af aresample=async=1:first_pts=0 \
  -ac 1 -ar 16000 -c:a pcm_s16le track.wav
```

In practice this inserts silence for packet-time gaps and shifts the stream onto a meeting-relative clock starting at `0`.

Observed behavior on real fixtures:

- late-joining speakers keep their late start in the decoded WAV,
- large internal packet gaps remain large silent spans,
- VAD on the decoded WAV lines up with actual packet-time positions.

## Pipeline Rules

1. Never assume extracted PCM is timeline-faithful unless gaps are preserved explicitly.
2. Build speech activity from gap-preserving per-speaker audio.
3. Extract transcription chunks from that same gap-preserving audio so chunk times are already on the meeting clock.
4. Build the mixed master audio from timeline-faithful tracks or an equivalent gap-preserving mix path.
5. Treat the audio timeline, not the container's nominal format duration, as the source of truth for transcript alignment.

## Validation

When debugging a suspicious artifact, verify all of the following:

1. compare each stream's first packet time to the earliest transcript word for that speaker,
2. compare packet span to decoded WAV duration,
3. look for large packet gaps with `ffprobe -show_packets`,
4. confirm VAD on the gap-preserving WAV starts where packet activity starts.

If those checks disagree, the transcript is not credible.
