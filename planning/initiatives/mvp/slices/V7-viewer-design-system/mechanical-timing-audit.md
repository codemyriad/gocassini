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

## Ground Truth

The ground truth for timing is the source ASR transcript:

- `transcript.words.v1`
- or the equivalent source words reconstructed from a portable manifest

Those source word timestamps are the only authoritative word-level timing we have.
Everything in the cleaned display transcript must be judged against them.

The cleaned transcript is allowed to reword, merge, split, or paraphrase text, but that does **not**
create new ground-truth word timings. If a cleaned token cannot be defended against the ASR words,
it must stay untimed.

## Programmatic Acceptance Rules

Treat these as the invariants for cleaned-token timing:

1. A cleaned token may be `alignment=source` only when it maps back to one or more ASR source words.
2. A cleaned token may be `alignment=interpolated` only when it sits between two exact source anchors **and**
   the cleaned run has lexical overlap with the ASR words in that source gap.
3. A fully rewritten block must stay untimed at the word level.
4. A pure insertion with no overlapping ASR gap words must stay untimed.
5. A synonym substitution with no lexical overlap to the ASR gap must stay untimed.
6. If there is doubt, prefer `token_is_timed: no` over fake precision.

This is the practical rule: cleaned-word timing is progressive enhancement, not a promise.
When we cannot prove a word timestamp from ASR evidence, we fall back to passage timing.

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

If the portable file does not embed `displayTranscript`, the tool now materializes the display transcript first.
That matters because many regressions happen in the runtime reconstruction path rather than in the packaged file itself.

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
- for rewritten words whose timing cannot be defended from the ASR source words
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
4. on at least one known-good source-aligned token in the same meeting,
5. on at least one previously bad rewritten token that should now be untimed.

For local viewer development, the dev server serves files from:

- [exports/viewer-demo](/home/silvio/dev/gocassini/cassini-viewer/exports/viewer-demo)

So the audit should point at the `.opus` file in `exports/viewer-demo/meetings/` if you are validating `http://localhost:5173/`.

## Regression Checklist

Any change to cleaned display timing is incomplete until all of these are true:

1. Unit tests cover the failure shape in both:
   - [portable.test.ts](/home/silvio/dev/gocassini/cassini-viewer/src/viewer/portable.test.ts)
   - [export-static-meetings.test.ts](/home/silvio/dev/gocassini/cassini-viewer/scripts/export-static-meetings.test.ts)
2. The mechanical audit reproduces the pre-fix failure on the bad artifact.
3. After the fix, the bad token becomes either:
   - correctly timed into the right phrase, or
   - intentionally untimed
4. A nearby exact-source token in the same meeting still audits correctly.
5. The check is run against the exact `.opus` file shipped to the UI.

Do not merge timing work on the strength of JSON inspection alone.
The required proof is:

- ASR-grounded alignment invariants in tests
- mechanical clip audit on the shipped artifact

## Interpretation

This is not a perfect speech recognizer oracle. Short clips can paraphrase slightly.
What matters is whether the clip transcription is in the right local phrase neighborhood.

Examples:

- `three` vs `few` can still be acceptable if the rest of the clip matches `days of work a month`
- `three` landing on `automates it for them` is not acceptable
- `publish` or `can` becoming untimed is acceptable if the alternative was seeking into unrelated speech

Treat this audit as the required double-check whenever cleaned-token alignment or seek behavior changes.
