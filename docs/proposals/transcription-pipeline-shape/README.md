# Proposal: take the speaker decision out of the decoder

Date: 2026-08-29
Status: Draft for discussion — prototype measured against ground truth, numbers reproducible

## TL;DR

The D-684 chain fixed a dozen defects in transcription timing and attribution. Almost
every one of them sits on a seam created by **one architectural choice**: a word's
speaker is *which decoder pass produced it*. `ProbeMKV` turns each stream's `participant_id`
tag into a `SpeakerID` (falling back to `participant_name`, then to the title for legacy
MKVs), one ASR pass runs per track, and the pass index is the speaker for the rest of the
artifact's life.

The fix is one stage: **after decoding, re-check every word against the per-track energy
on the shared timeline** — how far the speaker's own microphone sat above its own noise
floor, versus every other microphone above theirs. A word whose owner was decisively
quieter than someone else is crosstalk, not speech.

How much is "decisively" is not a constant. It is estimated per meeting from that
meeting's own distribution, because where crosstalk lands is a property of the room and
the gain staging, not of speech.

| corpus (exact ground truth) | status quo | proposed | |
|---|---|---|---|
| isolated tracks | 0.079 | **0.079** | cpWER — the estimator declines to act, so no change |
| −35 dB crosstalk | 0.274 | **0.160** | **−42% relative** |
| two people on one mic | 0.245 | **0.174** | second person becomes representable at all |

On a real 51-minute Cassini meeting the acoustic evidence contradicts **84 words** the
status quo publishes as speech — 1.2% — including eleven occurrences of the exact
Ivan/chima pattern D-683 documents, found without being told to look for it.

It also puts a floor under the failure. As crosstalk worsens from −55 dB to −25 dB the
status quo's cpWER blows up **18×** (0.078 → 1.411); re-attributed, it stays bounded
(0.078 → 0.223). Today there is nothing holding that curve down.

Two things this is *not*: it does not improve WER (D-577 already established that
readability is a post-processing problem), and it does not save money (per-track
decoding stays).

## Three hypotheses, two of them wrong

This document keeps the rejected variants because each was plausible, and the reasons
they failed are the load-bearing evidence for the one that worked.

### Rejected: one ASR pass over the mix

The obvious simplification — decode the mix once, attribute from energy. One clock, one
decoder, and the whole merged-fallback branch disappears.

It **loses 42% of the words relative to per-track decoding**: 374 words against the 646
per-track decoding recovers (the scorer-normalised reference is 682; the fixture's raw
whitespace count is 669 — hyphenated forms split during normalisation). Not a tuning problem — fixed 15-second windows are worse still (250
words), the mix is clean (−20.3 dBFS, peak 0.855), and the fixture's **24.5%** overlapped
speech is close to the **19.7%** measured on the real meeting. Overlapping speech masks
words, and per-track capture is exactly what recovers them.

**Multitrack decoding is the thing to keep.** What has to go is the assumption that the
pass which decoded a word is authoritative about who spoke it.

### Rejected: "a word is crosstalk if another track independently said it too"

Crosstalk duplicates an utterance; genuine overlap does not. So: drop a word only when a
louder track produced the *same word* at the same time. On the synthetic crosstalk corpus
this scored well (cpWER 0.169).

On the real meeting it dropped **1 word out of 6839**, leaving 83 of the 84 bad ones in
place. The rule works on synthetic bleed because that bleed is a literal copy of the same
waveform, so the ASR emits the same token twice. Real bleed is acoustically degraded and
comes back as *different* text — the fabricated words are short function words
("Thank", "Mm.", "yeah.") that the loud speaker never said. A synthetic corpus that
validates a rule the real audio then refutes is the most useful failure in this whole
exercise.

### Accepted: a threshold the meeting estimates for itself

The first energy rule used a 6 dB margin, picked by taste, and it made things *worse* on
clean audio (0.079 → 0.129) by deleting real speech during genuine overlap. So rather
than tune, measure. Splitting every word by whether ground truth says its speaker was
actually talking, on the crosstalk fixture:

| | median gap | p95 | max |
|---|---|---|---|
| speaker genuinely active | 0.0 dB | 5.5 dB | 26.0 dB |
| speaker silent — pure crosstalk | **34.2 dB** | 35.1 dB | 36.2 dB |

There, the populations barely touch: at 18 dB the rule drops **140 of 140** crosstalk
words and **2 of 586** real ones, and cpWER goes 0.274 → 0.160.

But 18 dB is a magic number, and it encodes one room. So the prototype estimates it
instead: fit two clusters to the meeting's own gap distribution and accept the split only
when the upper cluster genuinely looks like a crosstalk mode — enough mass, well
separated, and **tight**. It never loses to the status quo, and it beats a hand-tuned constant:

| corpus | status quo | **adaptive** | threshold it chose | fixed 18 dB |
|---|---|---|---|---|
| isolated tracks | 0.079 | **0.079** | *none* | 0.088 |
| −55 dB crosstalk | 0.078 | **0.078** | *none* | 0.085 |
| −45 dB crosstalk | 0.087 | **0.087** | *none* | 0.094 |
| −35 dB crosstalk | 0.274 | **0.160** | 17.2 dB | 0.160 |
| −30 dB crosstalk | 0.652 | 0.199 | 14.8 dB | 0.195 |
| −25 dB crosstalk | 1.411 | **0.223** | 12.5 dB | 0.226 |

(cpWER.) The estimated threshold tracks the room, and where there is no crosstalk
population the estimator declines to act and the result is *identical to the status quo* —
zero regression risk by construction. The fixed 18 dB constant, by contrast, costs
0.007–0.009 on every clean corpus for no benefit. That is the argument for estimating
rather than tuning, and it is why the constant is not what this proposal recommends.

The tightness test is what earns its keep. An earlier Otsu version would split anything,
and on clean tracks it split the harmless tail of the *correct* population, measurably
making things worse (0.088 → 0.109). Returning "no crosstalk population here" has to be
an available answer.

### And then the real meeting said something inconvenient

Run against the 51-minute standup, the estimator returns **none**.

That is not a bug. The real distribution looks nothing like the synthetic one:

| | synthetic crosstalk fixture | real meeting |
|---|---|---|
| words whose attribution the evidence contradicts | 19% | **1.2%** |
| shape of that population | tight mode at 34 dB | diffuse, 6–64 dB |
| p50 / p97 of all gaps | — | 0.0 dB / 0.0 dB |

Real crosstalk is diffuse because every participant pair has a different bleed path and
level, and it is rare because most of the time nobody else is loud enough to matter. My
synthetic corpus applied a uniform −35 dB bleed to every track from every speaker, which
manufactured a clean bimodal separation that real audio does not have.

The two populations therefore **overlap** in the 6–26 dB band on real audio — genuine
simultaneous speech reaches 26 dB, and real crosstalk starts at 6 dB. No threshold
separates them perfectly.

### What that means for the recommendation

Deleting words on a threshold is the wrong default. What the evidence supports is:

1. **Always compute the gap and keep it.** Every word carries the dB margin by which its
   owner's microphone beat, or lost to, the loudest other microphone. That is provenance,
   it is cheap, and it makes the question answerable after the fact instead of guessed at
   build time.
2. **Mark, don't delete.** A word whose owner was decisively quieter is flagged as
   low-confidence attribution. The viewer can grey it, the summariser can skip it, and a
   human can overrule it — which is exactly D-683's "keep waveform evidence in an audit
   sidecar; never silently rewrite canonical words".
3. **Delete only where the meeting's own distribution is unambiguous.** When the estimator
   finds a genuine tight crosstalk mode — a room system, a speakerphone, the −25 to −35 dB
   regime where the status quo degrades 5.2× across that band alone (0.274 → 1.411), and
   18× measured end to end from −55 dB — dropping is clearly right and the
   data says so without a human picking a number.

This is a weaker claim than "delete 84 words per meeting" and it is the one the
measurements actually support.

## The seam, concretely

`cassini-go-recorder/internal/transcribe` today:

- `ProbeMKV` derives one `SpeakerID` per stream, from the `participant_id` tag
  (`participant_name`, then title, are fallbacks) — `audio.go:62-104`.
- `transcribePass` runs one recognizer pass per stream.
- `AssembleSegments(stream.SpeakerID, words, …)` stamps that pass's speaker onto every
  word it produced.

Three things are welded together that want to be separate:

| | what it needs |
|---|---|
| *what* was said | the best audio available — per-track, because a mix masks words |
| *when* it was said | one clock |
| *who* said it | relative energy across tracks, which the decoder never consults |

D-676/677/678 are three separate "which of my N clocks is authoritative" bugs. D-679 is
VAD draining. D-680 is turn ordering across passes. D-683 is attribution — and it cannot
be fixed inside this shape at all, because the producing pass *is* the answer to "who
said this".

## What is proposed

1. **Decode per track** — unchanged.
2. **Attribute** — per-track log-RMS envelope (32 ms frame, 16 ms hop) on the shared
   timeline. Score each track by how far above *its own* noise floor it sits over the
   word's span, so a participant recording 30 dB hotter than another cannot win on
   absolute loudness. This is D-683's "relative to each track's local distribution, not
   one global dB threshold", implemented rather than deferred. Record the gap on every
   word; mark the word when its own track lost decisively; drop it only when this
   meeting's distribution shows an unambiguous crosstalk mode.
3. **Refine (optional)** — run a diarizer over the regions each track *owns* and split
   any track carrying more than one person.

Stage 2 needs no model and no new dependency: the `ffmpeg` decode already happens and the
rest is array arithmetic. It costs nothing measurable — the status quo and the proposal
have the same RTF.

## Two people, one microphone

This is the part that is not a tuning question. `speakerIDFromLabel` produces one id per
stream, so a second person on the same device is **structurally unrepresentable** — there
is no label to give them. On a corpus where leo and ana share a `room-laptop` track (5
tracks, 6 people), leo scores WER 1.000 under the status quo, not because the words are
wrong but because he has nowhere to go.

Adding a diarizer over the owned regions of each track recovers him — WER 1.000 → 0.680 —
and improves overall cpWER from 0.245 to **0.174**. Stage 2 is what makes stage 3 expressible: once
attribution is a stage over a timeline, a second "who spoke when" provider composes onto
it instead of replacing the decoder.

Diarization costs **no new runtime dependency**. Twelve diarization symbols are already exported from the
`libsherpa-onnx-c-api.so` the recorder links today — ten matching
`SherpaOnnxOfflineSpeakerDiarization*` plus `SherpaOnnxCreate…` and `SherpaOnnxDestroy…`
(v1.13.1, verified with `nm -D --defined-only`); only
the Go wrapper is missing, plus two ONNX files on the download path `EnsureModel` already
implements.

## Method

**Synthetic corpus, exact ground truth.** 6 speakers, 37 turns, 669 words raw / 682 after
scorer normalisation, 182 s,
synthesised with kokoro from `harness/scenarios/showcase-lantern-festival.v1.json` by
`prototype/build_fixture.py`. Speaker, start, end and text are known exactly. Variants:
isolated tracks, the same plus crosstalk at −25/−30/−35/−45/−55 dB, and a shared-mic
variant.

**Real corpus.** The 2026-08-20 standup — 5 tracks, 51 minutes, the meeting D-683 cites.
Run on the production recording host against its model cache, so no meeting audio left our
infrastructure.

**Metric.** cpWER — concatenated-speaker WER under the optimal assignment of hypothesis
labels to reference speakers (`scipy.linear_sum_assignment`). It charges both a
misrecognised and a misattributed word, and is deliberately generous about naming: a
system is never punished for calling someone `room-laptop#4` instead of `leo`, only for
putting the wrong words under a speaker or collapsing two people into one label.

Every architecture runs through the same ported `Recognizer`: same Parakeet v3 int8, same
Silero parameters, same 55 s/10 s/0.5 s chunking, same token→word logic as `stt.go`.

**One metric that cannot score the proposal.** The "words the energy evidence
contradicts" count is a diagnostic for the status quo only — the proposal assigns *by*
energy, so it can never contradict energy. Everything the proposal is judged on comes
from ground truth.

## Crosstalk sweep — all three architectures

| crosstalk | status quo | single mix pass | **re-attributed (adaptive)** |
|---|---|---|---|
| none (isolated) | 0.079 | 0.565 | **0.079** |
| −55 dB | 0.078 | 0.559 | **0.078** |
| −45 dB | 0.087 | 0.521 | **0.087** |
| −35 dB | 0.274 | 0.585 | **0.160** |
| −30 dB | 0.652 | 0.600 | **0.199** |
| −25 dB | **1.411** | 0.601 | **0.223** |

cpWER. Three things read off this one table. The **single mix pass is bad everywhere** —
it is barely affected by crosstalk because overlap, not crosstalk, is what breaks it. The
**status quo has no floor under it**: an 18× blow-up from 0.078 to 1.411 as room acoustics
degrade, with nothing holding the curve down. And **re-attribution provides that floor**
while costing exactly nothing in the three rows where there is no crosstalk to remove,
because the estimator declines to act there.

## The architecture is model-agnostic, and that is now tested

If speaker attribution really is a stage over the timeline rather than a property of the
decoder, then swapping the decoder should not change the shape of the result. The
prototype makes that falsifiable: `asr_backends.py` defines one contract,

```python
decode(samples_16k) -> [(text, start_ms, end_ms), ...]
```

and the same Silero VAD segmentation and the same energy-attribution stage run in front
of every backend. Implemented: sherpa-onnx Parakeet (the production decoder), Qwen3-ASR
with the Qwen3 forced aligner, Voxtral, any audio-capable OpenAI-compatible chat model via
OpenRouter, and `gemini-3.5-transcribe`.

**The contract is also a filter.** A model that cannot emit per-word times cannot be
attributed by energy at all — there is nothing to line the envelopes up against. That is
not a limitation of this proposal; it is a hard requirement any decoder has to meet, and
it rules out otherwise-strong models. The 2026-08-27 audit already recorded the case:
Voxtral returns "a flat transcript with no speaker/overlap times".

### What the hosted models actually return

Measured on one known fixture clip (mira's first turn, 8.58 s, 23 reference words):

| model | word times | result |
|---|---|---|
| `google/gemini-3.5-flash` (chat, via OpenRouter) | asked for, in JSON | schema broke — the array was truncated at the token limit and had to be parsed as prose |
| `google/gemini-3.7-flash` (chat, via OpenRouter) | asked for, in JSON | honoured: 24 words vs 23 reference, monotonic, in-bounds — **but the spans cover only 6.80 s of an 8.58 s clip (ratio 0.79)**, so the times are a plausible generation, not a measurement |
| **`gemini-3.5-transcribe`** (Gemini Interactions API) | **native** | `word_info` annotations with `start_offset`, `end_offset` **and a per-word `speaker` label**. 216 tokens for 8.6 s, 3.4 s latency, transcript essentially exact |

The distinction in that last column is the important one. A chat model *generates* something
timestamp-shaped when asked; `gemini-3.5-transcribe` *measures*. Only the second can be
trusted as input to an acoustic attribution stage.

### `gemini-3.5-transcribe` could fill stages 1 and 3 at once

It emits speaker labels per word, so it is also a candidate for the shared-microphone
split — no separate diarizer, one call. Tested on the same shared-mic corpus (leo + ana
summed onto one track, 182 s, one API call, 10.8 s, 4551 audio tokens):

| | speakers found | turn purity | collapsed two people into one label? |
|---|---|---|---|
| local sherpa-onnx diarizer | 3 (for 2) | **11 / 13** | no |
| `gemini-3.5-transcribe` | 3 (for 2) | 8 / 14 | no |

Both over-segment; the local diarizer is the more accurate of the two on this one corpus,
and it keeps audio in-house. So the hosted model is a real option for the decoder slot and
a *weaker* one for the diarizer slot, on this evidence.

Three practical notes: it is reachable on the existing `<gcp-project>`
("Gemini API") project key with `generativelanguage.googleapis.com` already enabled and
billing on — no new account, no Vertex setup. It costs roughly $0.005/minute of audio
(~$0.30/hour). And it sends meeting audio to Google, which is a data-processing decision
(`docs/privacy.md`, D-404), not a technical one — the local sherpa path is the option that
keeps audio on our infrastructure.

**None of this changes the proposal.** It supports it: the attribution stage hosted three
very different decoders without modification, which is the whole claim.

## Correction: two of my "findings" were already fixed

**I built the baseline from a checkout 44 commits behind `origin/main`,** before the
D-684 fixes merged. Two things I reported as live defects are not:

- **VAD feed size.** Current `main` already has `vadWindowSamples = 512`,
  `vadDrainEverySamples`, and a `drainSegments` loop. The 0.93 → 4.68 s onset shift I
  measured is real *for the code I ported*, and it is already gone upstream.
- **D-679 (VAD draining).** Same — already fixed on `main`.

The 2026-08-27 audit in a developer's local worktree also reached a stronger conclusion than mine on
the mechanism: an earlier version of that audit blamed non-divisibility of 5-second calls
by Silero's 512-sample window, and a source review retracted it — sherpa-onnx retains the
remainder between `AcceptWaveform` calls, so the defensible cause is the *undrained queue*,
not per-call quantisation. My measurement is consistent with either; theirs is the better
explanation, and it is already implemented.

**Consequence for the numbers in this document:** the "status quo" column is the pre-D-684
pipeline in the segment-assembly and merged-fallback dimensions, so its absolute cpWER
should be re-run against current `main` before being quoted. It is *not* stale on the VAD
dimension — `prototype/core.py` feeds Silero its native 512-sample window and drains per
chunk for **both** arms, precisely so the architecture comparison is not contaminated by
that defect. The architectural comparison holds either way.

## Bugs that do survive on current `main`

1. **The merged fallback transcribes the delivery artifact.** `ensureMergedFallback` still
   calls `ExtractMixedFloats(webmPath)` — the 64 kbps Opus `meeting.webm` — rather than
   source audio. Confirmed present at `transcribe.go:222` on `origin/main`.
2. **The showcase fixture has real speech for only 2 of its 6 speakers.**
   `prepare-synthetic-meeting.py` still aborts with *"scenario overlaps two turns for
   participant …"* on `origin/main`, and the committed fixture was filled in from the
   `mock` backend. `ben/ana/noah/jules.ogg` are low-frequency artifact, not voice
   (zero-crossing rate 0.003–0.005 against 0.013–0.047); Parakeet returns empty string for
   all four. Exactly one scripted start time is wrong —
   `harness/scenarios/showcase-lantern-festival.v2.json` fixes the scenario, and the
   generator now slides a self-overlapping turn instead of aborting mid-run — aborting
   partway left the earlier participants generated and the later ones missing, which is
   exactly how a fixture ends up looking complete while being half mock.

   **Scope correction:** I first wrote that "anything gated on this fixture is exercising
   two speakers, not six". CI only ever uses `mira` — `MEDIA_PREFIX=…/mira` in
   `ci-e2e-installed-exapp-talk.sh`, and the image workflow fetches only `mira.ivf` and
   `mira.ogg` — and mira is one of the two tracks that works. So nothing in CI is silently
   degraded today. The four dead tracks bite whoever tries to use this as a multi-speaker
   fixture, which is how I found them, and the committed audio still needs regenerating.
3. ~~`amix` over `aresample=async=1` sparse inputs aborts in ffmpeg 7.1.~~
   **Superseded by open PR #218** (`fix/ffmpeg-sparse-offset-crash`), which diagnoses the
   same root cause better: one-shot multi-minute compensation through
   `aresample=async=1`, reproduced as a libswresample segfault on a real 3637 s-late
   stream. Their fix rebases tracks later than `sparseInitialOffsetRebaseMS` (1000 ms)
   through `anullsrc … concat` instead. Cite theirs, not mine.

### Two version/PR caveats on the numbers

**PR #218 rewrites the decode path this prototype ports.** `decode_tracks` /
`extract_speaker_floats` mirror the pre-#218 `sparseTimelineAudioFilter`. That matters
concretely for the real-meeting leg: on the 2026-08-20 standup **four of five tracks start
late** — Chris 16.4 s, participant-oMOvtGI5 54.0 s, chima 56.5 s, and **Ivan 356.6 s** —
all above the 1000 ms rebase threshold, and Ivan is the very track in the D-683 dispute.
The intended alignment is the same either way (silence, then audio, on the shared
timeline) and this run did not crash, so the results are believed sound; but they are
computed on the old mechanism and should be re-derived once #218 lands.

**PR #219 auto-upgrades the bundled FFmpeg.** Any ffmpeg-behaviour claim here was observed
on 7.1.3 locally and 4.4.2 on the rented box — neither is necessarily what production will
run. Version-dependent claims should be pinned or dropped.

## Relationship to the 2026-08-27 audit and `challenges.go`

There is substantial prior work in a developer's local worktree that this proposal does **not**
supersede, and that partly anticipates it:

- `docs/transcription-quality-audit-2026-08-27.md` diagnoses the same short-interjection
  problem across five defects, with a corpus survey (120 transcripts, 103 with
  cross-speaker overlap, 332 nested segments) far larger than mine.
- Its **finding #5 is my crosstalk result**, reached first, and adjudicated with blind
  Gemini passes on real clips — evidence I did not have. It states the same conclusion I
  arrived at empirically: *"This should not become a blanket 'drop quiet words' rule…
  The robust rule is relative and reviewable."*
- `internal/transcribe/challenges.go` (1106 lines + 500 of tests, uncommitted) already
  implements per-stream RMS envelopes, percentile-based per-track calibration, activity
  gap-closing, and a bounded ranked evidence sidecar. **That is stage 2 of this proposal,
  already written in Go, in the production package, in the evidence-not-action form.**
- `harness/transcription-bench/` is the D-681 harness, with Voxtral offline/realtime and
  Parakeet hotword runners and a Vast.ai matrix orchestrator.

What this proposal adds on top of that work, rather than duplicating it:

1. **The architectural framing** — the five audited defects share one root: speaker
   identity is the decoder-pass index. Still true on `main` today
   (`AssembleSegments(stream.SpeakerID, …)`).
2. **A measured rejection of single-mix-pass ASR** (42% of words lost). The audit compares
   *models*; this compares *pipeline shapes*. New.
3. **The shared-microphone result** — two people on one device are structurally
   unrepresentable, and a diarizer composes onto a timeline-based attribution stage to fix
   it (cpWER 0.245 → 0.174). Absent from the audit entirely, and the sherpa-onnx
   diarization C API is already linked into the binary.
4. **A synthetic corpus with exact ground truth** (669 words, known speaker/time/text) and
   permutation cpWER scoring — complementary to the audit's Gemini-adjudicated real clips:
   theirs has real acoustics and tiny N, mine has exact labels and is free to re-run.

## Limits

- **The 18 dB margin rests on one synthetic corpus and one real meeting.** The two agree,
  which is encouraging, but D-681's corpus is what should confirm it before the rule
  deletes words unsupervised. Ship stage 2 behind a flag writing dropped words to a
  sidecar first.
- **Automatic shared-microphone detection is not ready.** Splitting a track that really
  carries two people works; *deciding* that a track carries two does not. On the real
  meeting's five single-person tracks — one of which (Chris) carries no speech at all, so
  four are testable — naive clustering flagged **3 of 4** as shared;
  restricting the diarizer to each track's owned regions improved that to **2 of 4**. So
  stage 3 should ship as *evidence, not action* — record the candidate and its waveform
  support in a sidecar, exactly as D-683 specifies, and let a human or an explicit
  per-device setting authorise the split.
- **Stage 3 is a real cost.** Diarization took 316 s for 5 tracks × 51 min on 12 CPU
  threads (RTF 0.020 per track-second), roughly quadrupling transcription wall-clock.
  Another reason to run it only where it is wanted.

## Sequencing

1. **Fix the VAD feed size and the D-679 drain.** Independent, cheap, and they move
   timestamps.
2. **Repair the showcase fixture** so CI exercises six speakers, and make the generator
   slide past a self-overlap instead of aborting on it.
3. **Land stage 2 behind a flag**, writing dropped words to a sidecar rather than deleting
   them silently, so the first production runs are auditable and the
   84-words-per-51-minutes figure can be checked on meetings other than 2026-08-20.
4. **Score it on the D-681 corpus.** That ticket already asks for speaker-attributed WER;
   this proposal is its first consumer, and `prototype/run_eval.py` already emits the
   metric in the shape D-681 describes.
5. **Only then** wrap the sherpa-onnx diarization C API in Go and add stage 3 as evidence.

## Reproducing

Prototype in `prototype/`; `requirements.txt` pins sherpa-onnx to the version the
recorder links.

```bash
# ground-truth fixture and corpora
python3 build_fixture.py --scenario ../../../../harness/scenarios/showcase-lantern-festival.v1.json \
    --model kokoro-v1.0.int8.onnx --voices voices-v1.0.bin --out-dir fixture
bash build_corpora.sh fixture corpus

# every architecture, scored against ground truth
python3 run_eval.py --mkv corpus/bleed.mkv --clean-mkv corpus/clean.mkv \
    --ref fixture/ground-truth.json --out out/bleed.json
python3 report.py out/bleed.json

# why 18 dB: the gap distribution the threshold is read from
python3 gap_dist.py --mkv corpus/bleed.mkv --ref fixture/ground-truth.json \
    --model-dir <parakeet-dir> --vad <silero.onnx>

# real multitrack meeting: cost and bleed audit, no reference transcript
python3 run_real.py --mkv <recording>.mkv --out out/real.json \
    --model-dir ~/.cache/cassini/models/parakeet-tdt-0.6b-v3-int8 \
    --vad ~/.cache/cassini/vad/silero_vad.onnx

# hosted backends: do they emit usable word times?
export OPENROUTER_API_KEY=...            # for the chat models
gcloud services api-keys get-key-string <api-key-uid> \
    --project <gcp-project> --format='value(keyString)' > gemini.key
python3 probe_hosted_timestamps.py       # chat models, schema + span coverage
python3 probe_gemini_diarization.py      # gemini-3.5-transcribe on a shared mic
```

Archived run (fixture, all six corpora, 23 result JSONs, logs) is on the private archive host at
`<private-archive>/cassini-pipeline-shape.tgz`, with a SHA-256 and
a provenance README. The rented GPU was destroyed after the run.

## What implementation review changed

The design above survived, but three review rounds against the Go implementation found
eight defects the prototype never exposed — worth recording, because every one of them
lived in the gap between "measure level relative to each track's own floor" as a sentence
and as running code.

| defect | why the prototype missed it |
|---|---|
| Timeline padding measured as the noise floor: a late joiner scored **238 dB** above "its own floor" against 52 dB for identical continuous audio, and would have won every contested word | every prototype track started at t=0 |
| Attribution depended on stream order when one participant owned several streams (40.0 vs 0.0 dB) | prototype tracks had unique ids |
| Pooling a speaker's frames and taking one percentile is **duration-weighted**: a five-second speech-only rejoin swamped the one second that established the floor, and crosstalk beat the owner by 20 dB again | the first fix was tested with equal-length streams |
| Drop mode rebuilt segment bounds from the last word, reintroducing the overlapping-word invalid envelope fixed in #216 | prototype words never overlapped |
| The flag was dropped by LLM readable cleanup, making the summary filter a no-op in exactly the normal path | the prototype had no cleanup stage |
| Mapping a cleaned word to *any* overlapping source word let one contradicted word flag a legitimate neighbour | tested only where cleanup preserved the word count |
| Segments carrying text but no word list were discarded whenever another segment was flagged | prototype segments always had words |
| The viewer badge could never fire for portable meetings, whose display transcripts carry cleaned tokens rather than words | the prototype had no viewer |

Two of those — padding, and duration-weighted pooling — are the same mistake twice: a
floor is a property of a participant's microphone, and anything that lets a *stream's*
accidents set it is wrong. The shipped code therefore calibrates per logical speaker,
taking the quietest floor any of their streams established, over frames that carry real
captured audio.

The habit that would have prevented most of this is cheap: **survey the shape of
production data before writing a fixture, not after shipping one.** A few `ffprobe` passes
over the archive produce the table in `prodshape_test.go`, and every defect above except
the last two sits somewhere in it.

## Provenance of figures quoted from elsewhere

Numbers attributed to the 2026-08-27 audit, to `challenges.go` (1106 lines + 500 of tests)
and to `harness/transcription-bench/` come from the **uncommitted working tree on a developer machine** —
they are on no branch and in no commit, so a reader cannot audit them from this repository.
The archived run backing this document's own figures is at
`<private-archive>/cassini-pipeline-shape.tgz` on the private archive host (fixture, six
corpora, 23 result JSONs, logs, SHA-256).
