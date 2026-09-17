# Parakeet boundary investigation — 17 September 2026

The missing September 14 passages are reproducible, but changing a padding constant is not a sufficient explanation or a general fix. The evidence points to interacting segmentation, feature extraction, and decoder behavior. **The final decision retains beam decoding and vocabulary hints for normal individual-track transcription where hints are supported. Merged-audio fallback and the INT8 bundle without BPE vocabulary use greedy decoding. All retain the corrected reference frontend and audio timing. Participant speech uses whole VAD spans with recorded context; the fallback retains its separate overlapping windows. Neither receives synthetic decoder padding.**

The recorded comparisons used Cassini source snapshot `61fb8f94` and the production native libraries described below. The clip results below describe controlled comparisons against that snapshot. Full-meeting validation of the integrated production path is reported separately below.

## Normal-build validation

The production path now includes the corrected native frontend in CPU/CUDA packages and the developer CLI. Normal Parakeet v3 individual-track transcription retains modified beam search and supported vocabulary hints, uses complete VAD spans with recorded boundary context, and adds no synthetic decoder tail. The merged-audio fallback uses greedy search; the INT8 bundle also uses greedy when its BPE vocabulary is absent, since it cannot apply hints. The earlier greedy candidate below is a historical experiment, not the final decoder policy. Inputs too short to produce two feature frames return no words instead of passing an empty tensor to the native decoder.

Completed build checks include the recorder Go suite, CPU/CUDA native packages, a complete CPU operator image with an actual transcription and artifact readback, and an ARM64 image transcription test. The production native frontend reproduces the validated feature tensors exactly. macOS has not been exercised on hardware. The final executable also passed GPU replays of both full target tracks and eight fallback fixtures: its PCM and complete word outputs exactly matched the selected beam and greedy comparison arms respectively. Three targeted INT8 CPU replays confirmed the tokenizer-less bundle selects greedy, including both reported passages and a historical omission fixture.

A historical INT8 CPU comparison of the earlier greedy candidate used the same 19 recorded fixtures, keeping vocabulary bias disabled in both paths because that model bundle lacks a BPE vocabulary. Results retain the earlier development/holdout split:

| INT8 CPU path | Development disagreements / 712 | Development deletions | Held-out disagreements / 248 | Held-out deletions | False-speech control tokens |
| --- | ---: | ---: | ---: | ---: | ---: |
| Previous frontend, beam search, boundaries | 244 | 193 | 48 | 16 | 0 |
| Reference frontend, greedy, whole VAD + recorded context | 117 | 49 | 31 | 4 | 0 |

These are disagreements against model-generated references, not human-certified accuracy. Development insertions increased from 10 to 21, and individual clips can regress. One legacy test process exited 137; its remaining four fixtures completed in separate processes. All 19 production fixtures completed together. One fixture has no usable reference and is excluded from scores.

The archive replay has restarted under the final normal individual-track beam policy with automatic participant hints; validation of all 138 meetings with that policy is in progress and not complete. Earlier greedy GPU results are diagnostic evidence and do not certify the final decoder. It covers individual participant tracks through the public production recognizer; it does not generate meeting summaries. Source integrity is checked separately from decoded duration: timestamp-preserving silence can hide missing packets in a damaged recording. Twenty truncated historical MKVs were reconstructed from retained packet archives. All 5,517,760 audio packets across their 127 tracks were verified before replay; the original recordings were left untouched.

Archive testing also found a separate extraction defect: Opus pre-skip can consume sparse initial packets across a long mute gap. Rebasing decoded audio but restoring the first encoded packet timestamp moved one track’s speech about eleven minutes earlier. Extraction now restores the first decoded frame timestamp, preserving the meeting clock. Synthetic and recorded-source regressions pass. For reuse, GPU results must have both byte-identical PCM and the same inference policy. PCM identity alone cannot justify reusing greedy results for beam validation. The extraction also preserves the container timestamp origin consistently across early and late participant tracks.

Additional blind audio checks have confirmed an omitted passage recovered by the new path, genuine repeated greetings, and removal of a repetition present in the published transcript. A Gemini response hallucinated speech over a near-silent clip (about −92 dBFS RMS); that response is excluded from scoring. Published transcripts and model references are audit aids, not ground truth.

## What was tested

The benchmark replays recorded audio through Cassini's actual Go transcription path, including participant extraction, VAD, decoding, timestamps, overlap reconciliation, and filtering. It compared:

- 42 initial window/overlap/synthetic-padding conditions on 13 excerpts and controls.
- 16 further real-context and punctuation-seam conditions on those excerpts, plus three reconstructed August failure cases with original audio on both sides.
- Four greedy-decoder conditions and eight greedy/real-context conditions on all 16 excerpts and controls.
- An independent Hugging Face Parakeet reference implementation on the exact longer utterance crop: **all eight tested input variants retained the missing prefix**.

References came from blind Gemini audio transcription, without supplying the missing quotations as hints. They remain model hypotheses, not human ground truth: Gemini says “live coding” where the original report says “vibecoding”, and some isolated tracks contain other-speaker bleed. Normalized token disagreement, deletions, insertions, repeated words, and false-speech controls were measured separately. July 27 was withheld from the initial candidate selection and reported as confirmation.

The comparison below includes the other original excerpts and three August cases: 712 reference tokens, excluding empty-speech controls and July 27. It describes diagnostic interventions, **not recommended defaults**.

| Decoder / independent window / synthetic tail | Token disagreements | Deletions | Insertions | False-speech control tokens |
| --- | ---: | ---: | ---: | ---: |
| Existing beam / 10 s / 500 ms | 201 / 712 | 145 | 20 | 3 |
| Greedy / 10 s / 500 ms | 137 / 712 | 71 | 23 | 2 |
| Greedy / 10 s / none | 121 / 712 | 39 | 33 | 2 |
| Greedy / 25 s / none | 97 / 712 | 37 | 19 | 2 |

Greedy also disables hotword bias, so this comparison alone does not isolate search from contextual bias. It substantially reduces omissions but does not solve every passage: the quiet utterance's cropped phrase still disappears with the synthetic tail. Removing that tail partly restores it, while worsening the withheld July 27 excerpt. More words or a lower score on this small corpus cannot establish a universally better pipeline.

## Boundary sensitivity: what the experiments establish

Preserving the longer utterance with the larger resource window retained the missing clause at every tested real-context margin, from 30 to 1000 ms. Disagreement on its 57-token reference stayed between 10 and 11 tokens. Independent 10-second decoding ranged from 11 to 32 and lost different speech as the boundaries moved. This supports avoiding arbitrary cuts inside that utterance; it does not prove that 25 seconds is a privileged model setting.

Every greedy/real-context variant restored the quiet utterance's phrase as “Probably too much live coding”. Exact “vibecoding” recognition remains unresolved. Wider context was not monotonically safer: the 1000 ms variant produced a substantial omission in another participant's excerpt and new false speech on a bleed control. Even narrower variants retained small mixed-audio regressions.

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

A separate CPU FP32 diagnostic used Cassini's cached ONNX encoder, decoder and joiner with a greedy TDT loop, keeping the model and search fixed while changing feature extraction. On the quiet utterance's exact crop with the 500 ms tail, reconstructed sherpa-style features produced **empty output**; reference features produced “For too much by coding.” Without that tail both frontends produced a phrase, with different lexical errors. This isolates a frontend contribution to the omission, but does not recover the exact human wording.

The longer utterance's opening survived under both frontends with greedy search, with and without the tail. On the whole utterance the reference frontend also removed an extraneous opening emitted with sherpa-style features. Together with the native beam/greedy experiments, this supports investigating frontend and search separately. The diagnostic uses a Python decoding loop and ONNX Runtime 1.30.0 CPU, not the deployed 1.27.1 CUDA native recognizer; it is not validation of a production patch.

## Native follow-up: model parity, search, and utterance preservation

The experiment now uses a rebuilt sherpa-onnx v1.13.7 C API with the same ONNX Runtime 1.27.1 CUDA library as production. With all experimental flags disabled, the existing 16-clip corpus reproduces the baseline scores exactly. A standalone probe of the **compiled native** frontend, rather than a Python reconstruction, matched 12 saved reference feature tensors at RMS 1.68–2.82 × 10^-6, with exact frame counts. Thirty-two short-input and silence cases were finite and repeated feature reads were deterministic.

### What the beam investigation ruled out

The native beam scores zero-duration blank transitions but executes them as one-frame advances. A synthetic example proves that this mismatch can change speech into empty output. However, correcting it left final corpus disagreement unchanged in all 32 tested frontend/fixture combinations. It is not the demonstrated cause of these recording omissions.

A separate experiment reproduced NeMo's final length-normalized ranking. On the longer utterance's padded first crop, the final beam contained an empty candidate and three partial candidates; the full missing opening was already absent. Ranking differently recovered only a partial sentence. The quiet utterance's final candidates also lacked the phrase. These results point to search-path loss, not merely the last choice among surviving hypotheses. These beam-search experiments remain isolated diagnostics and are not part of the production patch.

### Historical greedy pipeline candidate

The earlier candidate combined the model-reference frontend with greedy decoding, preserves each whole VAD span instead of splitting it again at ten seconds, removes synthetic decoder tails, and expands boundaries into recorded audio. The existing VAD maximum duration and emergency safety split still bound work. We evaluated 0, 30, and 60 ms of real context as a stability check. The 30 ms central condition comes from Silero's reference utility, not selection of the lowest score. Sixty milliseconds scored slightly better on the development corpus, which is not a reason to promote it.

| Native configuration | Development disagreements / 712 | Deletions | False-speech control tokens | Previous holdout / 90 | New scored holdouts / 158 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Existing frontend + beam + existing boundaries | 201 | 145 | 3 | 14 | 19 |
| Reference frontend + beam + existing boundaries | 191 | 134 | 2 | 15 | 15 |
| Reference frontend + greedy + existing boundaries | 107 | 43 | 2 | 13 | 21 |
| Reference frontend + greedy + whole VAD + 30 ms real context, no synthetic tail | 94 | 34 | 3 | 13 | 20 |

The candidate recovers the longer utterance's opening across all three context conditions. With 30 and 60 ms, the quiet utterance's phrase is present, but “vibecoding” becomes “live coding” or “bytecoding”. With no context, the onset is still misrecognized. Development disagreements remain 93–96 across these context settings; individual clips vary more. Extra words from bleed/uncertain audio remain a concern, and exact lexical recovery is unresolved.

Three previously unused 60-second excerpts were selected by fixed time interval and highest track RMS, without inspecting ASR output. Two received fresh blind Gemini references; the third received no reference because the provider declined it, and is excluded from reference scoring. The candidate and baseline tie at **33 disagreements over the 248 scored held-out tokens combined**, but regressions and improvements occur on individual clips. These are small, model-referenced samples, not proof of general superiority. Greedy also drops participant hotword bias. That feature loss motivated the final controlled comparison below; normal builds retain beam search and supported hints.

Artifacts: [native frontend patch](../harness/patches/parakeet-v3-reference-frontend-sherpa-v1.13.7.patch), [compiled-feature probe](../harness/bin/check-parakeet-native-features.cc), [blank-scoring counterexample](../harness/bin/test_tdt_legal_blank.py), and [isolated build/replay instructions](../harness/patches/native-parakeet-experiments.md). All recordings, references, traces, and model outputs remain private.

## Final decoder decision: retain beam and hints

The earlier experiments had not tested the essential combined condition: corrected frontend **plus whole VAD spans, 30 ms of recorded context, no synthetic tail, and beam search with existing hints**. Comparing greedy under new boundaries against beam under old boundaries did not establish that removing beam or hints was necessary.

A new paired GPU matrix tested 19 fixtures under both boundary policies with beam plus available existing hints, beam without hints, and greedy. PCM hashes and sample counts matched within each comparison. Four fixtures are meeting mixes, whereas production transcribes individual participant tracks; mixing those strata exaggerated the evidence for changing the production decoder.

| Whole-span policy, individual tracks only | Reference tokens | Beam with available hints: disagreements | Greedy: disagreements |
| --- | ---: | ---: | ---: |
| Development, nine fixtures | 586 | 63 | 58 |
| Held-out, three scored fixtures | 248 | 29 | 33 |
| Combined, descriptive only | 834 | 92 | 91 |

These are normalized disagreements against blind model references, not human-certified error rates. The held-out split remains separate for assessment; it was not used to tune a new constant. Two individual-track false-speech controls produced two tokens with beam and three with greedy. A further individual track had no usable reference and is excluded. On four meeting mixes, beam had 71 disagreements versus 35 for greedy: those mixes account for 36 of the 41-error development difference in the unstratified comparison.

Both complete target participant tracks were then decoded, with matching PCM between arms. Beam with existing hints and greedy both retained the reported missing passages; exact recognition of “vibecoding” remains unresolved. Beam without hints still omitted the longer opening in the excerpt comparison. Existing hints affect search-path survival even when the expected passage contains none of the hinted names; this is a remaining sensitivity, not proof of lexical correctness.

Eight historical fixtures with a pre-existing hint file provided a separate vocabulary check. Hinted beam recovered an expected technical term and proper name that unbiased beam and greedy missed. Those two positive examples are meeting mixes, so they do not establish an isolated-track name-accuracy rate. Hints also produced four boosted-term tokens across two bleed-only controls. The legacy hinted pipeline already produced four false tokens on those controls, including three boosted terms, and produced additional spurious terms elsewhere. Hint-induced false speech is therefore an existing risk that remains visible in the tests, not a solved problem or a reason to maximize output length.

The final choice preserves the supported hint feature for normal individual-track transcription: retain beam search, corrected preprocessing, whole VAD spans, recorded boundary context, no synthetic decoder padding, and the timestamp fixes. Merged-audio fallback and the INT8 bundle without BPE vocabulary use greedy, as qualified below. The small isolated-track comparison does not justify removing hints for a one-token aggregate difference. This is not a claim that beam is universally more accurate. The complete archive must be replayed under this final decoder policy before that validation is complete; historical greedy results cannot substitute for it.

### Merged-audio fallback uses greedy

An additional eight-fixture comparison exercised the actual `useVAD=false` path, with identical PCM across the three arms. The corrected beam path had 103 disagreements over 172 reference tokens, versus 30 for corrected greedy and 74 for the legacy hinted path. This is not solely a mixed-audio effect: one isolated speech fixture regressed from three disagreements with legacy beam to 34 with corrected beam, while greedy had four. False-speech controls produced seven tokens with corrected beam, three with greedy, and four with legacy beam. The hinted arms used the same pre-existing vocabulary file and score.

These results qualify the decoder decision: the successful VAD-enabled target recovery does not validate direct no-VAD fallback. Boundary policy fields do not imply that the no-VAD path actually performs VAD segmentation. A separate CPU ablation on one mix and one isolated speech fixture found that restoring the old 500 ms synthetic tail did not eliminate the omissions; this was not a GPU tail comparison. The merged-audio fallback therefore uses greedy and records the hints as unapplied for that accepted result. Normal individual-track beam replay is unaffected by this exception. Archive replay and overall quality validation are not complete.

### INT8 without BPE vocabulary uses greedy

A completed 19-fixture CPU check compared corrected INT8 beam against the earlier corrected INT8 greedy candidate. This bundle has no BPE vocabulary and cannot apply the hint feature that motivates retaining beam in the normal FP32 path.

| Corrected INT8 decoder | Development disagreements / 712 | Development deletions | Held-out disagreements / 248 | False-speech control tokens |
| --- | ---: | ---: | ---: | ---: |
| Beam | 144 | 84 | 32 | 0 |
| Greedy | 117 | 49 | 31 | 0 |

Individual-track development disagreements were 91 versus 78 over 586 reference tokens; the difference is not confined to meeting mixes. Eighteen fixtures had identical PCM hashes and sample counts. One target participant crop differed after the timestamp-origin correction, so that comparison cannot isolate decoder choice. Excluding it leaves 129 versus 100 development disagreements over 655 tokens, and 76 versus 61 on individual-track development speech over 529 tokens. One fixture remains unreferenced. These are model-reference comparisons, with the original held-out split preserved.

The final INT8 exception is scoped to the v3 INT8 model with an absent BPE vocabulary, rather than removing hints from models that support them. Together with the merged-audio exception, it retains the tested greedy behavior where beam showed omissions without an available hint benefit. The complete archive replay uses the normal FP32 individual-track path and still requires completion.

## General strategy, in implementation order

1. **Establish frontend and decoder parity.** Compare feature tensors, encoder outputs and decoding on identical PCM against the reference implementation. Separate greedy versus beam, hotword bias, quantization/export, and frontend differences. Verify speech coverage across boundary perturbations before adopting a change.
2. **Use VAD to locate speech, preserving source context.** Maintain the original timeline and recover surrounding recorded samples. Check onset coverage independently from ASR. Keep an utterance together when resources permit; treat maximum duration as a memory/latency bound, rather than a lexical tuning parameter.
3. **For long speech, use contextual decoding with explicit output ownership.** Encode left/central/right recorded audio, emit only the owned central region, and preserve decoder state. NeMo provides this buffered design; its algorithm is materially different from independently decoding overlapping WAVs and concatenating text. [NeMo buffered implementation](https://github.com/NVIDIA-NeMo/Speech/blob/main/examples/asr/asr_chunked_inference/rnnt/speech_to_text_streaming_infer_rnnt.py)
4. **Measure coverage and make recovery bounded.** Flag audible/VAD speech with suspiciously absent output, retain provenance, and compare a limited alternate decode when necessary. Accept additional words only with temporal and cross-decode agreement; do not select the longest transcript or repeatedly vary padding until something appears. Evaluate duplicate words and bleed alongside deletions on independent recordings.

The reference frontend and whole-utterance policy are now wired into normal Parakeet v3 builds. Stateful contextual decoding and conservative recovery remain proposed work. This PR has not been deployed to production.

## Implemented and reviewable

- Fixed overlap-word normalization so sentence-final punctuation does not prevent matching the same boundary word, while preserving leading/internal dots in technical terms and numbers.
- Added recorded-audio benchmark controls using the production path and a reusable scorer with held-out separation, explicit empty-speech controls, and regression tests.
- Added an opt-in [same-model frontend diagnostic](../harness/bin/check-parakeet-frontend.py), using local ONNX models and WAVs without changing production dependencies.
- Enabled model-reference preprocessing while retaining beam decoding for normal hint-capable Parakeet v3 individual tracks, preserving complete VAD spans with 30 ms of recorded context and no synthetic decoder tail. The existing VAD resource limits remain.
- Built the patched native runtime into CPU/CUDA images and the developer CLI; the Go recognizer rejects an incompatible runtime instead of silently using the old frontend.
- Retain configured vocabulary and participant hints where the model bundle supports hotword bias. Merged-audio fallback and the INT8 bundle without BPE vocabulary use greedy with accurate unapplied-hint provenance. Other model families retain their existing policy.
- Added a resumable full-meeting GPU replay that processes every participant track through the public production recognizer. The final beam-policy archive replay is running; earlier greedy runs do not complete this requirement.

See [benchmark usage](parakeet-boundary-benchmark.md) and [primary-source literature and implementation notes](parakeet-boundary-literature-2026-09-17.md). Recordings and full reference/output transcripts remain outside this document.
