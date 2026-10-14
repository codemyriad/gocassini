# Automatic topic suggestions: evaluation

Date: 2026-10-14 · Parent: [README](README.md), "Evaluation plan"

This page holds aggregate numbers only. The corpus, its transcripts and every per-meeting output stay off the repository.

## Gate 1: int8 re-validation

**Result: passed.** At the prototype's gate (`T` = 0.85, `M` = 0.05), the Go scorer on int8 vectors agrees with the fp32 prototype on 657 of 660 (meeting, topic) decisions: 99.55%, against a target of 97%. `ScorerVersion = 1` keeps `T` = 0.85 and `M` = 0.05.

### Corpus

- 60 recordings of one recurring internal meeting, all English: 33.1 hours (8 to 75 minutes, median 33) and 254,876 words.
- 12 topics with written definitions (T1..T12), plus the two built-in controls.
- As in the prototype, the first 5 meetings drafted the vocabulary and are not counted. That leaves 55 meetings × 12 topics = 660 decisions.
- The reference is the prototype's 163 fp32 assignments at 0.85/0.05.

### Method

1. Each transcript was written as a `--words` file, in the prototype's word order (see "Equal times" in the README's Chunking section).
2. `cassini topics embed --words <file> --threads 4` embedded each meeting with the shipped int8 model on ONNX Runtime 1.27.1.
3. `cassini topics embed-texts` embedded the 12 definitions and the 2 controls.
4. `cassini-operator topics-eval --vectors <dir> --definitions <file> --reference <file> --sweep` scored them.
5. The same command, run on the prototype's own fp32 vectors converted to the same file format, checks the Go scorer port separately from the model.

### Passages

- Go passages equal the Python passages in all 60 meetings: 5,461 passages, the same start, end, word count and boundary rule for each.
- Boundary rules: 3,118 sentence end, 1,258 pause, 976 speaker change, 49 word cap, 60 end.
- The token cap never fired: the longest passage is 155 tokens (median 62, 99th percentile 133).

### Scorer port

On the prototype's fp32 vectors, the Go scorer reproduces it exactly:

- 163 assignments, 6 of them through the short-strong rule;
- 660 of 660 decisions agree;
- the sweep matches too: 345, 163, 65 and 20 assignments at the four gates below.

### Vectors

| Measure | Value |
|---|---|
| Passage cosine, int8 vs fp32 | min 0.99967 · 1st percentile 0.99993 · median 0.99996 |
| Definition cosine, int8 vs fp32 | min 0.99996 |
| Score delta, int8 − fp32 (5,461 passages × 12 topics) | mean +0.0001 · sd 0.0004 · 1st/99th percentile −0.0007/+0.0011 · max \|Δ\| 0.0021 |
| Score delta where the fp32 score is ≥ 0.80 (13,490 pairs) | mean +0.0002 · sd 0.0004 · 1st/99th percentile −0.0007/+0.0011 |
| Margin delta (score minus best control) | mean +0.0001 · sd 0.0004 · 1st/99th percentile −0.0008/+0.0013 |

### Decisions at each gate

| Gate (`T`/`M`) | fp32 | int8 | Both | int8 only | fp32 only | Agreement |
|---|---|---|---|---|---|---|
| 0.83 / 0.04 | 345 | 343 | 342 | 1 | 3 | 99.39% |
| **0.85 / 0.05** | **163** | **164** | **162** | **2** | **1** | **99.55%** |
| 0.87 / 0.06 | 65 | 65 | 65 | 0 | 0 | 100% |
| 0.89 / 0.08 | 20 | 21 | 20 | 1 | 0 | 99.85% |

The gate is still the dominant choice: moving from 0.83/0.04 to 0.85/0.05 halves the assignments. Quantization moves a handful of decisions.

### The three flips at 0.85 / 0.05

Each one is a single passage scoring within about 0.001 of the 0.85 threshold, on opposite sides in fp32 and int8. That passage joins or splits a span, and so moves the meeting across the 80-word rule.

| Flip | fp32 | int8 |
|---|---|---|
| 1 | not suggested: one span of 65 words (min score 0.8551) | suggested: 154 words in two spans (min score 0.8501) |
| 2 | suggested: 104 words in two spans (min score 0.8500) | not suggested: one span of 49 words (min score 0.8517) |
| 3 | not suggested: 53 words in two spans | suggested: 109 words in three spans (min score 0.8506) |

None comes from a systematic shift: the score delta is centred on zero with a standard deviation of 0.0004. These are threshold-edge cases, and fp32 would flip them just as easily on a slightly different transcript. No threshold was changed.

### Cost

- 340 s of wall time for the 60 meetings at 4 threads: 5.7 s per meeting, model load included.
- The host was shared and busy (load average 4 to 6 on 8 CPUs).
- Batching is on: at most 16 rows and 1,024 padded tokens per batch (README, "Embedding runtime").

### Not covered

- One room and one language: language validation and precision belong to Gate 2.
- Gate 1 fed the chunker words in the prototype's order. In production, words with equal times keep their transcript order, which differs in 3 of the 60 meetings; those three were not scored that way.
- The language detector reported English for all 60 meetings. That is not a test of the detector on other languages: its tests use public multilingual text.
- Gate 1 says int8 decides like fp32. It does not say the decisions are right.
