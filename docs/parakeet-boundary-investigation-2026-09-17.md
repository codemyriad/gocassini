# Parakeet boundary investigation — 17 September 2026

The missing September 14 passages are reproducible, but changing a padding constant is not a sufficient explanation or a general fix. The evidence points to interacting segmentation, feature extraction, and decoder behavior. **Production window, padding, and decoder defaults remain unchanged.**

The recorded comparisons used Cassini source snapshot `61fb8f94` and the production native libraries described below. The PR preserves subsequent decoder configuration changes from `main`; the numerical results describe the audited snapshot, not a new sweep of every later change.

## What was tested

The benchmark replays recorded audio through Cassini's actual Go transcription path, including participant extraction, VAD, decoding, timestamps, overlap reconciliation, and filtering. It compared:

- 42 initial window/overlap/synthetic-padding conditions on 13 excerpts and controls.
- 16 further real-context and punctuation-seam conditions on those excerpts, plus three reconstructed August failure cases with original audio on both sides.
- Four greedy-decoder conditions and eight greedy/real-context conditions on all 16 excerpts and controls.
- An independent Hugging Face Parakeet reference implementation on the exact Chris crop: **all eight tested input variants retained the missing prefix**.

References came from blind Gemini audio transcription, without supplying the missing quotations as hints. They remain model hypotheses, not human ground truth: Gemini says “live coding” where Silvio reports “vibecoding”, and some isolated tracks contain other-speaker bleed. Normalized token disagreement, deletions, insertions, repeated words, and false-speech controls were measured separately. July 27 was withheld from the initial candidate selection and reported as confirmation.

The comparison below includes the other original excerpts and three August cases: 712 reference tokens, excluding empty-speech controls and July 27. It describes diagnostic interventions, **not recommended defaults**.

| Decoder / independent window / synthetic tail | Token disagreements | Deletions | Insertions | False-speech control tokens |
| --- | ---: | ---: | ---: | ---: |
| Existing beam / 10 s / 500 ms | 201 / 712 | 145 | 20 | 3 |
| Greedy / 10 s / 500 ms | 137 / 712 | 71 | 23 | 2 |
| Greedy / 10 s / none | 121 / 712 | 39 | 33 | 2 |
| Greedy / 25 s / none | 97 / 712 | 37 | 19 | 2 |

Greedy also disables hotword bias, so this comparison alone does not isolate search from contextual bias. It substantially reduces omissions but does not solve every passage: Silvio's cropped phrase still disappears with the synthetic tail. Removing that tail partly restores it, while worsening the withheld July 27 excerpt. More words or a lower score on this small corpus cannot establish a universally better pipeline.

## Boundary sensitivity: what the experiments establish

Preserving Chris's utterance with the larger resource window retained the missing clause at every tested real-context margin, from 30 to 1000 ms. Disagreement on his 57-token reference stayed between 10 and 11 tokens. Independent 10-second decoding ranged from 11 to 32 and lost different speech as the boundaries moved. This supports avoiding arbitrary cuts inside that utterance; it does not prove that 25 seconds is a privileged model setting.

Every greedy/real-context variant restored Silvio's phrase as “Probably too much live coding”. Exact “vibecoding” recognition remains unresolved. Wider context was not monotonically safer: the 1000 ms variant produced a substantial omission in another participant's excerpt and new false speech on a bleed control. Even narrower variants retained small mixed-audio regressions.

The three August cases that motivated earlier short-window handling also improved under several alternative conditions. Their existence therefore does not establish a permanent 10-second requirement. Conversely, gains on these cases do not validate a new window-and-padding combination as a general solution.

## Why a little silence can change a whole passage

Sherpa v1.13.7 normalizes each mel feature across the entire supplied utterance. PCM silence participates in those statistics, so adding zeros changes normalized features throughout the speech, not just its boundary. Leading padding also changes frame/subsampling alignment. These are concrete mechanisms for input sensitivity; they do not by themselves identify the cause of each omission. [Feature extraction](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/offline-stream.cc#L219), [normalization](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/math.cc#L134)

There is an upstream report of empty or very short Parakeet output under modified beam search that recovers under greedy decoding. The current beam implementation supports TDT. Its search differs from the reference in duration expansion, hypothesis merging, and final ranking. Keeping candidates at different frame offsets is also present in the reference and is not itself evidence of a bug. The proposed upstream patch remains unvalidated, and one of its claimed missing guards is already present in v1.13.7. [Issue #3267](https://github.com/k2-fsa/sherpa-onnx/issues/3267), [versioned beam implementation](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/offline-transducer-modified-beam-search-nemo-decoder.cc), [proposed patch #3657](https://github.com/k2-fsa/sherpa-onnx/pull/3657)

Frontend differences are independently verified. Sherpa defaults to a Povey window; its NeMo `is_librosa` flag changes mel filters, not the window or framing. Compared with the HF reference frontend, its edge treatment, frame alignment, and variance denominator also differ. The successful reference decodes strengthen the case for investigating frontend/export/decoder parity, but do not isolate which difference matters. Changing just the window name would not establish parity. [Sherpa defaults](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/features.h), [NeMo initialization](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/offline-recognizer-transducer-nemo-impl.h#L228), [HF frontend](https://github.com/huggingface/transformers/blob/main/src/transformers/models/parakeet/feature_extraction_parakeet.py)

### Numerical frontend comparison

The original NVIDIA `.nemo` checkpoint configuration and preprocessing source agree with the independently published Transformers frontend on the relevant choices. A pinned `kaldi-native-fbank==1.22.3` reconstruction of the sherpa feature path differed from the reference normalized features by RMS 0.287, 0.410, and 0.366 on the three exact production PCM crops. Reproducing the reference choices reduced those differences to 0.0000022–0.0000034 (maximum absolute difference below 0.00012).

The combined corrections were: Nyquist upper frequency instead of 7.6 kHz; symmetric Hann instead of Povey window; zero-centered, zero-padded frames instead of reflected edges with a half-hop offset; waveform-wide preemphasis instead of frame-local preemphasis; additive `2^-24` log guard instead of a clamp; and sample rather than population variance. This is a numerical frontend comparison, not yet an end-to-end validation of a patched native recognizer. The reconstructed baseline is not a direct dump from the deployed native library.

This provides a model-specified repair target, independent of the expected words. Correcting frontend parity still needs transcription regression tests: search behavior and ONNX export can contribute separately. The Go wrapper exposes too little of the frontend to implement these corrections just by changing its configuration.

### Same-ONNX-model diagnostic

A separate CPU FP32 diagnostic used Cassini's cached ONNX encoder, decoder and joiner with a greedy TDT loop, keeping the model and search fixed while changing feature extraction. On Silvio's exact crop with the 500 ms tail, reconstructed sherpa-style features produced **empty output**; reference features produced “For too much by coding.” Without that tail both frontends produced a phrase, with different lexical errors. This isolates a frontend contribution to the omission, but does not recover the exact human wording.

Chris's opening survived under both frontends with greedy search, with and without the tail. On the whole utterance the reference frontend also removed an extraneous opening emitted with sherpa-style features. Together with the native beam/greedy experiments, this supports investigating frontend and search separately. The diagnostic uses a Python decoding loop and ONNX Runtime 1.30.0 CPU, not the deployed 1.27.1 CUDA native recognizer; it is not validation of a production patch.

## Native follow-up: model parity, search, and utterance preservation

The experiment now uses a rebuilt sherpa-onnx v1.13.7 C API with the same ONNX Runtime 1.27.1 CUDA library as production. With all experimental flags disabled, the existing 16-clip corpus reproduces the baseline scores exactly. A standalone probe of the **compiled native** frontend, rather than a Python reconstruction, matched 12 saved reference feature tensors at RMS 1.68–2.82 × 10^-6, with exact frame counts. Thirty-two short-input and silence cases were finite and repeated feature reads were deterministic.

### What the beam investigation ruled out

The native beam scores zero-duration blank transitions but executes them as one-frame advances. A synthetic example proves that this mismatch can change speech into empty output. However, correcting it left final corpus disagreement unchanged in all 32 tested frontend/fixture combinations. It is not the demonstrated cause of these recording omissions.

A separate experiment reproduced NeMo's final length-normalized ranking. On Chris's padded first crop, the final beam contained an empty candidate and three partial candidates; the full missing opening was already absent. Ranking differently recovered only a partial sentence. Silvio's final candidates also lacked the phrase. These results point to search-path loss, not merely the last choice among surviving hypotheses. The native experiments remain opt-in and separate; none is silently enabled in production.

### A reference-based pipeline candidate

The candidate combines the model-reference frontend with greedy decoding, preserves each whole VAD span instead of splitting it again at ten seconds, removes synthetic decoder tails, and expands boundaries into recorded audio. The existing VAD maximum duration and emergency safety split still bound work. We evaluated 0, 30, and 60 ms of real context as a stability check. The 30 ms central condition comes from Silero's reference utility, not selection of the lowest score. Sixty milliseconds scored slightly better on the development corpus, which is not a reason to promote it.

| Native configuration | Development disagreements / 712 | Deletions | False-speech control tokens | Previous holdout / 90 | New scored holdouts / 158 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Existing frontend + beam + existing boundaries | 201 | 145 | 3 | 14 | 19 |
| Reference frontend + beam + existing boundaries | 191 | 134 | 2 | 15 | 15 |
| Reference frontend + greedy + existing boundaries | 107 | 43 | 2 | 13 | 21 |
| Reference frontend + greedy + whole VAD + 30 ms real context, no synthetic tail | 94 | 34 | 3 | 13 | 20 |

The candidate recovers Chris's opening across all three context conditions. With 30 and 60 ms, Silvio's phrase is present, but “vibecoding” becomes “live coding” or “bytecoding”. With no context, the onset is still misrecognized. Development disagreements remain 93–96 across these context settings; individual clips vary more. Extra words from bleed/uncertain audio remain a concern, and exact lexical recovery is unresolved.

Three previously unused 60-second excerpts were selected by fixed time interval and highest track RMS, without inspecting ASR output. Two received fresh blind Gemini references; the third received no reference because the provider declined it, and is excluded from reference scoring. The candidate and baseline tie at **33 disagreements over the 248 scored held-out tokens combined**, but regressions and improvements occur on individual clips. These are small, model-referenced samples, not proof of general superiority. Greedy also drops participant hotword bias; proper-name accuracy needs explicit validation before adoption.

Artifacts: [native frontend patch](../harness/patches/parakeet-v3-reference-frontend-sherpa-v1.13.7.patch), [compiled-feature probe](../harness/bin/check-parakeet-native-features.cc), [blank-scoring counterexample](../harness/bin/test_tdt_legal_blank.py), and [isolated build/replay instructions](../harness/patches/native-parakeet-experiments.md). All recordings, references, traces, and model outputs remain private.

## General strategy, in implementation order

1. **Establish frontend and decoder parity.** Compare feature tensors, encoder outputs and decoding on identical PCM against the reference implementation. Separate greedy versus beam, hotword bias, quantization/export, and frontend differences. Verify speech coverage across boundary perturbations before adopting a change.
2. **Use VAD to locate speech, preserving source context.** Maintain the original timeline and recover surrounding recorded samples. Check onset coverage independently from ASR. Keep an utterance together when resources permit; treat maximum duration as a memory/latency bound, rather than a lexical tuning parameter.
3. **For long speech, use contextual decoding with explicit output ownership.** Encode left/central/right recorded audio, emit only the owned central region, and preserve decoder state. NeMo provides this buffered design; its algorithm is materially different from independently decoding overlapping WAVs and concatenating text. [NeMo buffered implementation](https://github.com/NVIDIA-NeMo/Speech/blob/main/examples/asr/asr_chunked_inference/rnnt/speech_to_text_streaming_infer_rnnt.py)
4. **Measure coverage and make recovery bounded.** Flag audible/VAD speech with suspiciously absent output, retain provenance, and compare a limited alternate decode when necessary. Accept additional words only with temporal and cross-decode agreement; do not select the longest transcript or repeatedly vary padding until something appears. Evaluate duplicate words and bleed alongside deletions on independent recordings.

The frontend and whole-utterance candidate are implemented in isolated native patches and benchmark controls. Stateful contextual decoding and conservative recovery remain proposed work. No replacement pipeline has been deployed.

## Implemented and reviewable

- Fixed overlap-word normalization so sentence-final punctuation does not prevent matching the same boundary word, while preserving leading/internal dots in technical terms and numbers.
- Added recorded-audio benchmark controls using the production path and a reusable scorer with held-out separation, explicit empty-speech controls, and regression tests.
- Added an opt-in [same-model frontend diagnostic](../harness/bin/check-parakeet-frontend.py), using local ONNX models and WAVs without changing production dependencies.
- Preserved production decoder, 10-second window, overlap, and padding defaults while the causal investigation continues.

See [benchmark usage](parakeet-boundary-benchmark.md) and [primary-source literature and implementation notes](parakeet-boundary-literature-2026-09-17.md). Recordings and full reference/output transcripts remain outside this document.
