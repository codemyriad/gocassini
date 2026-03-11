# Diarization Fallback Research

Prepared on 2026-03-10.

## Scope

This note evaluates how Cassini should handle speaker diarization when its normal "one audio stream per participant" assumption does not hold.

It focuses on two cases:

1. A Cassini meeting where one recorded audio/video stream actually contains multiple humans around one device.
2. A single mixed recording, such as a meeting captured from one participant's computer, where there are no per-participant audio streams at all.

The goal is to identify the best practical open or open-weight diarization options, explain the real constraints, and recommend an efficient auto-selection policy that fits Cassini's current transcriber architecture.

This note does not deeply evaluate source separation or multi-talker ASR, but it does explain where diarization alone is insufficient.

## Cassini Today

Cassini's transcriber currently assumes the source MKV carries one audio stream per participant and uses each stream title as the speaker label. The current pipeline is documented in `cassini-transcriber/README.md` and `cassini-transcriber/docs/architecture.md`.

Important current properties:

- The canonical transcript is the source of truth and is aligned to the final delivered audio.
- Overlapping speaker segments are preserved in the canonical transcript.
- The pipeline extracts each participant track separately, preserves sparse packet-time gaps, detects speech activity per track, unions activity across speakers, compresses long all-speaker silence on the digest timeline, transcribes each track separately, and merges them into one canonical transcript.
- Local execution intentionally uses one heavy model stage at a time, and the work directory caches extracted audio and per-model responses.
- The checked-in transcriber does not currently have a diarization path; its current auto logic only selects the ASR backend and device.

This matters because any mixed-track fallback must preserve at least these invariants:

- stable speaker IDs,
- digest-timeline alignment,
- compatibility with silence-compressed output audio,
- predictable cacheability and restart behavior,
- acceptable handling of overlap.

## Problem Statement

The current "participants known" metadata is not enough to determine how many distinct human voices are present in a given recorded stream.

Examples:

- A meeting may have three platform participants, but one participant is a room device with five humans speaking into one stream.
- A Cassini-native MKV may still contain a mixed room stream even though the meeting has rich metadata.
- A single imported recording may contain any unknown number of speakers.

Therefore:

- global meeting participant count is not a safe proxy for speaker count inside a specific audio stream,
- routing diarization solely from recorder metadata is unsound,
- the auto policy has to distinguish "speaker count known and bounded" from "speaker count unknown".

## Research Findings

### 1. Best current practical open options

Published benchmark numbers are useful, but they are not perfectly apples-to-apples across vendors. They should be treated as directional evidence rather than a substitute for Cassini-specific evaluation on the same recordings and scoring protocol.

#### `pyannote/speaker-diarization-community-1`

This is the strongest default choice for offline mixed-audio diarization when speaker count is unknown.

Why it stands out:

- It is openly available under `CC-BY-4.0` and runs locally.
- It explicitly improves speaker assignment and speaker counting over the older pyannote pipeline.
- It exposes `num_speakers`, `min_speakers`, and `max_speakers` controls.
- It provides `exclusive_speaker_diarization`, which is useful when reconciling diarization with imperfect ASR timestamps.
- It supports offline use after download.
- It fits naturally into Python and is already the diarization backend used by tools such as WhisperX.

Important caveats:

- The model repository is gated on Hugging Face access terms.
- Official sources do not publish a directly comparable real-time-factor benchmark the way NVIDIA does for Sortformer.

#### `nvidia/diar_streaming_sortformer_4spk-v2.1`

This is the strongest meeting-oriented NVIDIA option when the processed clip is known to contain at most four distinct speakers.

Why it stands out:

- It is explicitly designed for streaming long-form diarization.
- It uses an Arrival-Order Speaker Cache to keep speaker identity consistent across chunks in a stream.
- NVIDIA publishes concrete speed figures on RTX 6000 Ada:
  - very high latency mode: RTF `0.002`,
  - low latency mode: RTF `0.093`.
- NVIDIA's published meeting benchmarks are strong on AMI and AliMeeting.
- It can also be used for offline diarization by using a long enough chunk size.

Important caveats:

- It accepts single-channel 16 kHz audio.
- Its output speaker dimension is fixed at `S = 4`.
- NVIDIA states that it can detect a maximum of 4 speakers and that performance degrades on recordings with 5 or more speakers.
- The model card is governed by the NVIDIA Open Model License Agreement.

#### NeMo cascaded diarization stack (`MarbleNet` + `TitaNet` + clustering/MSDD)

This is not the best "just use this" fallback, but it is highly relevant if Cassini needs a hierarchical or stitched diarization pipeline.

Why it matters:

- NeMo documents a cascaded diarization stack with VAD, speaker embeddings, clustering, and neural diarization.
- `TitaNet` is a practical open speaker embedding model for cross-chunk speaker linking.
- This stack is a good fit if Cassini needs local chunk diarization plus global speaker stitching.

Important caveat:

- Current NeMo MSDD docs describe the model configuration with a fixed speaker count per model, so MSDD is not the direct answer to arbitrary unknown-speaker mixed meetings.

#### `DiariZen`

This is an important research reference, but not a safe default product choice.

Why it matters:

- Its published benchmarks are strong and clearly better than pyannote v3.1 on several standard datasets.
- A recent independent benchmark paper describes DiariZen as a competitive open-source alternative.

Why it is not the obvious default:

- Its pretrained weights are explicitly `CC BY-NC 4.0`, with non-commercial restrictions.
- That makes it poor default material for a production fallback unless those licensing terms are acceptable.

### 2. Chunking helps, but not in the simple way it first appears

The idea that "if we segment enough, no segment will have more than 4 participants" is directionally useful, but it needs a precise caveat:

- Sortformer's `4spk` limit is a limit on distinct speakers in the processed clip, not just simultaneous overlap at one instant.
- A clip with five speakers taking short sequential turns can still violate the model's limit even if only one or two speak at a time.

This creates two separate problems:

1. The chunking policy has to keep the number of distinct speakers per processed chunk at or below four, not merely the number of simultaneous speakers.
2. Once audio is split into many local chunks, speaker identity continuity across chunks becomes a new problem.

This second point is fundamental. Pyannote's own pipeline description already frames diarization as:

- local speaker segmentation on short windows,
- local speaker embeddings,
- global agglomerative clustering.

So chunking is not a shortcut around global speaker linking. It is already part of how modern diarization pipelines work.

Running Sortformer independently on many small chunks without state would throw away one of its main advantages: the speaker cache that helps maintain identity consistency over time. If Cassini chunked mixed audio aggressively and ran isolated Sortformer calls, it would still need a separate cross-chunk re-identification stage afterward.

### 3. Efficiency findings

The efficiency picture from the sources is:

- Sortformer has the strongest official NVIDIA speed story for GPU deployments.
- Pyannote has straightforward GPU execution and in-memory processing hooks, but no directly comparable official RTF figure in the model card.
- Speaker count constraints improve pyannote behavior when some bounds are known.

An important secondary source result also matters here:

- SDBench reports that a pyannote-v3-based system called SpeakerKit achieved a `9.6x` speedup over pyannote v3 with comparable error rates.

This does not directly benchmark `community-1`, but it strongly suggests that:

- pyannote-style pipelines have meaningful optimization headroom,
- raw diarization efficiency is a legitimate systems concern,
- Cassini should design for cached and reusable diarization outputs rather than treat diarization as a disposable one-shot pass.

### 4. Overlap is the hardest product caveat

Cassini today preserves overlapping speaker segments because it has separate audio tracks for each speaker.

With a single mixed recording, diarization alone does not recreate that capability.

Diarization can answer "who spoke when?" It does not separate the audio waveforms. That means:

- speaker attribution can improve,
- turn boundaries can improve,
- word-to-speaker assignment can improve,
- but exact lexical recovery during simultaneous speech is still limited by the ASR path.

This is not a theoretical edge case. WhisperX, which combines faster-whisper-style ASR with pyannote diarization, explicitly documents that overlapping speech is not handled particularly well by Whisper or WhisperX.

For Cassini this means:

- diarization fallback is still worthwhile,
- but it is not equivalent to true multitrack capture,
- and if "accurately recover what both people said during overlap" is a hard product requirement, diarization alone is not enough.

That would require a later research track around speech separation or multi-talker ASR.

## Recommended Auto Policy

### Safe policy

This is the policy that is justified by the current evidence:

- If the source is a normal Cassini multitrack recording with one audio stream per participant, keep the current no-diarization path.
- If the source is mixed audio and the processed clip is explicitly known to contain at most four distinct speakers, and an NVIDIA GPU is available, prefer `nvidia/diar_streaming_sortformer_4spk-v2.1`.
- If the source is mixed audio and speaker count is unknown, prefer `pyannote/speaker-diarization-community-1`.
- Do not choose Sortformer solely because an NVIDIA GPU exists when speaker count is unknown.
- Do not infer speaker count from global meeting participant metadata alone.

### Why this is the safe policy

It is tempting to say "if NVIDIA exists, use Sortformer; otherwise use pyannote." The research does not support that as a generally correct rule.

The unsafe step is the hidden assumption that a mixed recording with unknown speaker count probably behaves like a `<= 4` speaker clip. Sometimes it will. Sometimes it will not. And the failure mode is exactly where diarization is already fragile: missed speech and speaker confusion in higher-speaker-count conditions.

### What about the assumption "hidden room/device voices max 4"?

If Cassini wants to adopt that as an explicit product assumption for a clearly marked input class, it can.

But that assumption must be explicit and documented. It is not derivable from current recorder metadata, and it should not silently control auto-routing for arbitrary mixed recordings.

## Architecture Options for Cassini

### Option A: Pyannote-only mixed-track fallback

This is the simplest and safest implementation path.

Flow:

1. Detect that the input is mixed or otherwise unsuitable for per-stream speaker attribution.
2. Run pyannote on the whole file, or on large speech-containing regions.
3. Produce stable diarization segments and speaker IDs.
4. Reconcile speaker labels with ASR word timestamps.
5. Emit the normal Cassini transcript schema.

Pros:

- works when speaker count is unknown,
- lower engineering risk,
- easiest path to a correct first release,
- most natural fit with current faster-whisper-based ASR flow.

Cons:

- no special NVIDIA fast path,
- likely slower than the best possible GPU-specific solution.

### Option B: Safe hybrid auto-routing

This is the policy I would currently recommend for productization.

Flow:

- use Sortformer only when the `<= 4 distinct speakers` bound is explicit,
- otherwise use pyannote.

Pros:

- simple,
- explainable,
- avoids unsound guesses,
- gives a real GPU advantage where the constraint is satisfied.

Cons:

- leaves some potential GPU speed upside on the table when count is unknown but happens to be small.

### Option C: Hierarchical hybrid with chunk-local diarization and global stitching

This is the most ambitious design and the one that most directly addresses the "segment enough to get local complexity below four" idea.

A plausible shape is:

1. Use cheap speech activity detection to create large speech-containing "super-chunks".
2. Run local diarization per super-chunk.
3. Use Sortformer on NVIDIA when local chunk complexity is expected to fit its constraints.
4. Extract speaker embeddings per local speaker track using something like TitaNet or pyannote-compatible embeddings.
5. Cluster or re-identify those local speakers globally across chunks.
6. Stitch the final speaker timeline back onto Cassini's source and digest timelines.

Pros:

- potentially the best long-term efficiency path on NVIDIA,
- can exploit local low-complexity windows,
- aligns well with Cassini's existing cacheable chunked architecture.

Cons:

- materially more complex,
- global stitching quality becomes a first-class risk,
- naive silence-based chunking is unlikely to be enough,
- the design has more moving parts than Cassini's current transcriber.

## Practical Conclusions

### Best immediate product choice

If the goal is a robust fallback that can ship, the best immediate choice is:

- add a mixed-track pyannote fallback first,
- add Sortformer only where the `<= 4 distinct speakers` bound is explicit.

### Best NVIDIA-specific choice

If the input class is genuinely bounded to four distinct speakers per processed clip, Sortformer looks like the best current open NVIDIA path.

### Most important non-obvious conclusion

Segmenting mixed audio aggressively enough to make Sortformer usable is not just a chunking problem. It becomes a speaker-linking problem.

That is the core systems caveat behind the original proposal.

### Most important product caveat

Diarization fallback improves speaker attribution, but it does not restore the full multitrack behavior Cassini currently enjoys for overlapping speech.

## Questions That Need Expert Review

These are the questions that matter most before locking the design:

1. Should Cassini's first mixed-track fallback optimize for correctness and simplicity (`pyannote` first), or for GPU throughput (`Sortformer` plus stitching)?
2. If Cassini chunk-routes to Sortformer, what is the chunking policy that limits distinct speakers per chunk without exploding chunk count?
3. What should Cassini use for cross-chunk speaker stitching: TitaNet embeddings, pyannote embeddings, or something else?
4. Should Cassini expose explicit speaker-count hints in CLI and environment configuration for mixed inputs?
5. How should Cassini mark or detect "room device" streams inside native MKVs so mixed-stream routing is explicit rather than inferred?
6. Is the product willing to accept degraded overlap transcript recovery on mixed recordings, or does that force a future separation or multi-talker ASR track?
7. What runtime and memory envelope is acceptable for long CPU-only pyannote runs?

## Suggested Experiments In This Repo

Cassini already has a useful synthetic meeting generator in `harness/bin/prepare-synthetic-meeting.py`, and it emits a manifest with both `participants` and per-turn references. That makes it possible to build repeatable mixed-audio evaluation cases inside this repo.

Recommended experiment set:

1. Generate synthetic meetings with known turn-level ground truth.
2. Downmix the participant tracks into one mono recording.
3. Evaluate three regimes:
   - `<= 4` total speakers,
   - `> 4` total speakers but locally sparse turn-taking,
   - `> 4` speakers with interruptions and overlap.
4. Compare:
   - full-file pyannote,
   - Sortformer on whole file where valid,
   - chunked Sortformer plus global stitching.
5. Measure:
   - diarization DER or equivalent against reference,
   - speaker count error,
   - wall-clock time,
   - GPU memory,
   - transcript speaker-assignment quality,
   - failure modes at chunk boundaries.

## Final Recommendation

As of 2026-03-10, the strongest conclusion from the available evidence is:

- `pyannote/speaker-diarization-community-1` should be Cassini's default fallback for mixed recordings with unknown speaker count.
- `nvidia/diar_streaming_sortformer_4spk-v2.1` should be the preferred NVIDIA path only when the `<= 4 distinct speakers` constraint is explicit and trusted.
- A future chunked Sortformer design is plausible, but it should be treated as a speaker-stitching project, not merely a chunking project.

## Sources

Cassini repo context:

- `cassini-transcriber/README.md`
- `cassini-transcriber/docs/architecture.md`
- `cassini-transcriber/cassini_transcriber/pipeline.py`
- `cassini-transcriber/cassini_transcriber/cli.py`
- `harness/bin/prepare-synthetic-meeting.py`

Primary external sources:

- pyannote Community-1 model card: https://huggingface.co/pyannote/speaker-diarization-community-1
- pyannote pipeline paper summary: https://www.isca-archive.org/interspeech_2023/bredin23_interspeech.html
- pyannote speaker configuration docs: https://docs.pyannote.ai/tutorials/speaker-configuration
- NVIDIA Streaming Sortformer 4spk v2.1 model card: https://huggingface.co/nvidia/diar_streaming_sortformer_4spk-v2.1
- NVIDIA NeMo speaker diarization models doc: https://docs.nvidia.com/nemo-framework/user-guide/latest/nemotoolkit/asr/speaker_diarization/models.html
- NVIDIA TitaNet model card: https://huggingface.co/nvidia/speakerverification_en_titanet_large
- NVIDIA Open Model License: https://www.nvidia.com/en-us/agreements/enterprise-software/nvidia-open-model-license/
- WhisperX README: https://github.com/m-bain/whisperX

Secondary benchmark and research context:

- SDBench: https://arxiv.org/abs/2507.16136
- Benchmarking Diarization Models: https://arxiv.org/abs/2509.26177
- DiariZen repository and benchmarks: https://github.com/BUTSpeechFIT/DiariZen
