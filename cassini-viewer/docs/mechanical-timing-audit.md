# Mechanical Timing Audit

Use this workflow to decide whether a cleaned-token click target is actually correct.

The core idea is simple:

1. take the timestamp assigned to the cleaned token,
2. extract a short audio clip around that timestamp,
3. transcribe only that clip,
4. compare what is heard to the token's nearby transcript context.

If the clip transcription lands in the right neighborhood, the click target is probably correct.
If it lands on a different phrase, the timing is wrong even if the UI token looks plausible.

## Why this matters

Repeated words and long cleaned passages can make alignment bugs hard to see by inspecting JSON alone.
This audit checks the final user-visible behavior directly: "when I click this word, what audio do I hear?"

## Tool

The viewer package includes:

- [audit-portable-token-audio.mjs](/home/silvio/dev/gocassini/cassini-viewer/scripts/audit-portable-token-audio.mjs)

It reads a portable `.opus`, finds a target token inside a passage, extracts a clip around that token,
transcribes it with the OpenAI audio API, and prints:

- whether the token is timed at all,
- the clicked token timing,
- the nearby token context,
- nearby transcript items,
- the clip transcription,
- whether the clip includes the target word,
- a simple context-overlap score.

## Prerequisites

- `ffmpeg`
- `ffprobe`
- `OPENAI_API_KEY`

On this machine the API key is available in `fish`, so the most reliable invocation is:

```bash
fish -lc 'cd /home/silvio/dev/gocassini/cassini-viewer && npm run audit:portable-token -- ...'
```

## Example

Audit the March 18 `three` click target:

```bash
fish -lc 'cd /home/silvio/dev/gocassini/cassini-viewer && npm run audit:portable-token -- \
  --audio ./exports/viewer-demo/meetings/daily-meeting-2026-03-18--12:30.opus \
  --snippet "two or two or three days of work a month" \
  --word three'
```

Good output looks like:

- `heard_contains_target: yes`
- `heard_context_overlap: high`
- clip text near `two or three days of work a month`

Also acceptable:

- `token_is_timed: no`
- for rewritten edge words with no exact anchor on one side
- this means the viewer is intentionally avoiding fake word precision

Bad output looks like:

- `heard_contains_target: no`
- `heard_context_overlap: low or zero`
- clip text from a different phrase, for example `almost completely automates it`

## How to use it during timing work

Run this audit:

1. before changing alignment logic, to capture the failure,
2. after rebuilding the affected portable or bundle,
3. against the exact file the UI serves.

For local viewer development, the dev server serves files from:

- [exports/viewer-demo](/home/silvio/dev/gocassini/cassini-viewer/exports/viewer-demo)

So the audit should point at the `.opus` file in `exports/viewer-demo/meetings/` if you are validating `http://localhost:5173/`.

## Interpretation

This is not a perfect speech recognizer oracle. Short clips can paraphrase slightly.
What matters is whether the clip transcription is in the right local phrase neighborhood.

Examples:

- `three` vs `few` can still be acceptable if the rest of the clip matches `days of work a month`
- `three` landing on `automates it for them` is not acceptable
- `publish` or `can` becoming untimed is acceptable if the alternative was seeking into unrelated speech

Treat this audit as the required double-check whenever cleaned-token alignment or seek behavior changes.
