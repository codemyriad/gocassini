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

## Gate 2: labelled review set

**Result: English measured and validated; Italian too thin to validate.** The labels led to three changes:

- **Prompts:** recipe r2 embeds passages as documents and definitions as queries, with EmbeddingGemma's retrieval prompts.
- **Scorer:** `T` = 0.68 and `M` = 0.06, with every supporting word counting.
- **Controls:** the control texts are now in seven languages.

On English meetings, suggestions are right about 3 times in 4 (precision 0.75) and find about 3 in 5 of the topics a meeting is about (recall 0.60). The scorer before Gate 2 had precision 0.82 and recall 0.45.

### Corpus and labels

- **Meetings:** 50 meetings from 5 rooms (37, 5, 3, 3 and 2 meetings), 199,196 words in all. Of these, 48 are in English and 2 in Italian. They are the 13 meetings with transcripts outside the largest room, plus 37 drawn at random from it.
- **Topics:** 16 written definitions. These are Gate 1's 12 and 4 more for subjects the smaller rooms discuss. Every topic was labelled on every meeting: 800 (meeting, topic) pairs, so no band weighting is needed.
- **Labellers:** two independent model labellers of different model families read each transcript in full. Neither saw a score, and each answered whether the meeting has a real discussion of the topic (roughly a minute or more, a decision, or work assigned), not a passing mention.
  - They agreed on 709 of 800 pairs (88.6%, Cohen's kappa 0.76), with near-identical positive rates (0.378 and 0.379).
  - A third model reader settled the 91 disagreements, for tuning only. The precision and recall below count undisputed pairs, as `topics-eval --labels` does.
  - The labels are not people's labels. A spot check by people is the obvious next step.
- **Positives:** 310 of the 800 pairs are about their topic.

### What was tried

All numbers are for English meetings. Settings were chosen by 5-fold cross-validation grouped by meeting, and the held-out numbers are what the choice achieved on meetings it never saw. A Python port of the scorer, checked to decide exactly like the Go one on all 1,840 pairs of a larger 115-meeting corpus (the 50 labelled meetings and 65 more from the largest room), made the sweeps fast.

| Change | Result |
|---|---|
| `T` and `M` alone, recipe r1 | A trade-off, not a gain. At a precision floor of 0.75, held out: precision 0.74, recall 0.52. |
| Control texts of everyday talk (greetings, status rounds, scheduling, small talk) | No better than the two prototype controls. |
| Per-topic offsets from each topic's own score distribution (z-scores, percentiles) | No better. Topics do differ in level, but only labels can tell where to cut. |
| Margin over the other library topics instead of the controls | Slightly better at most; within noise. |
| Prompts: retrieval (passages as documents, definitions as queries) | Best at every precision level; held-out recall 0.60 against 0.52 at precision 0.74. |
| Prompts: classification | Much worse (recall 0.22 at precision 0.75). |
| Prompts: sentence similarity | Close to retrieval at precision 0.75, worse at 0.8. |
| Joining spans by passage cosine, and counting only spans of 25 words or more | Both lose topics; every fold turned both off. |
| The short-strong rule, rescaled to r2 | Fires rarely and moves nothing. It is kept at 0.76 / 0.10 / 40 words for short, unmistakable discussions. |
| S8's per-topic gates, simulated on review verdicts from the training meetings | No topic reached 20 reviewed suggestions with 5 rejections in 50 meetings. They matter only after months of use. |

### Final settings (recipe r2, `ScorerVersion` 2), measured by `topics-eval --labels`

| Gate (`T`/`M`) | English precision | English recall | Italian found |
|---|---|---|---|
| 0.64 / 0.04 | 0.52 | 0.92 | 3 of 7 |
| 0.66 / 0.05 | 0.64 | 0.82 | 3 of 7 |
| **0.68 / 0.06** | **0.75** | **0.60** | **3 of 7, none wrong** |
| 0.70 / 0.07 | 0.82 | 0.42 | 2 of 7 |
| 0.72 / 0.08 | 0.89 | 0.22 | 1 of 7 |

Before Gate 2 (recipe r1, 0.85 / 0.05): English precision 0.82, recall 0.45; Italian 0 of 7.

The gate chosen is a judgement. Every suggestion is reviewed by a person, so we chose one wrong suggestion in four in exchange for finding a third more of the topics. 0.70 / 0.07 keeps the old precision with the old recall.

### Languages

Text scores higher against text in its own language. With English definitions, Italian meetings got nothing before Gate 2. Italian definitions with only English controls matched almost any Italian talk: 7 of 8 found, but 15 wrong. Italian controls brought that to 6 found and 2 wrong. These two counts use the settled labels (8 Italian positives); the gate table counts undisputed pairs only (7).

The controls are therefore shipped in seven languages, and the margin takes the best of all of them. That left every English decision unchanged.

The docs ask for definitions in the language the meetings are held in. Two Italian meetings cannot validate Italian, so only English is in `topicsValidatedLanguages`.

### Proposals

The clustering join was set on the largest room's meetings by looking at the groups it formed:
- at 0.80 the room collapsed into three groups;
- at 0.84 a theme that recurred over fifteen meetings split apart;
- 0.83 keeps it.

The nearest-topic hint (0.78) and the identity test (0.90) were mapped from r1 by percentile.

### Not covered

- The labels come from models, not people.
- Gate 1 compared int8 with fp32 on recipe r1, and it was not re-run on r2. Gate 1 was needed because the prototype's thresholds were set on fp32 vectors. The r2 thresholds were set directly on the int8 vectors the product computes.
- One organisation, and nearly all of it in English.

### Re-running it

1. On the server, embed the meetings with `cassini topics embed` into a folder, and the library's definitions with every control in `topicControls` with `cassini topics embed-texts`.
2. Label every topic on every sampled meeting, two labellers each, as JSON lines: `{"meeting": "<file stem>", "topic": "<id>", "labeller": "<name>", "inTopic": true}`.
3. Run `cassini-operator topics-eval --vectors <dir> --definitions <file> --labels <file> --sweep --out <report>` and copy the aggregates here. Per-meeting rows stay off the repository.

### Per-topic gates

In production, a topic with at least 20 review verdicts on its current definition, 5 of them rejections, all from meetings with vectors in the current space, gets `T` and `M` fitted from a grid (0.60 to 0.80 and 0 to 0.12, in steps of 0.01, around the global 0.68/0.06). The fit is kept only when its leave-one-meeting-out accuracy beats the global gate's accuracy on the same verdicts. Direct confirms (a meeting that already carried the tag) are left out: rejections exist only where a suggestion was made, so direct confirms below the gate would pull every fit looser. Rejections copied by a merge are left out for the opposite reason: the merged topic's confirms are not copied. A fit is tied to the definition, the vector space and the global scorer key; a change to any of them voids it. `cassini-operator topics-export` gives the verdict rows for checking this offline.

