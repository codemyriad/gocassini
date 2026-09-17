# Replay diagnostics

Run against a private output directory produced by `replay-stt-meetings.py`:

```sh
python3 harness/bin/summarize-stt-meetings.py \
  --output-dir /private/replay-production \
  --published-root /private/published \
  --output /private/replay-audit.json
```

The optional published root contains `<meeting-id>.meeting/transcript.words.v1.json`.
Only completed meetings are audited. The report records maximum decoded-track
coverage against expected meeting duration, empty tracks, timestamp errors,
case/punctuation-normalized adjacent word repeats, and longest same-word runs.
A possible source truncation means maximum decoded duration falls more than
one second below 98% of expected duration; shorter individual participant tracks
are normal and do not trigger this meeting-level diagnostic.

Published comparisons join speaker labels to MKV participant names or titles;
`labelMatched=false` requires manual mapping before comparing windows. The eight
largest absolute word-count changes per label are 30-second **audit candidates**.
Published output may use different models and includes attribution/merging that
isolated-track replay omits. Counts, empty tracks and repetitions are not accuracy
scores: normal disfluencies, joining/leaving and bleed all require audio review.
The output contains private speaker labels and is written with mode 0600.
