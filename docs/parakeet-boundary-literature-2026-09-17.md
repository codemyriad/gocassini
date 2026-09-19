# Parakeet boundary handling: primary-source review

Reviewed 2026-09-17 for the reported September 14 speech omissions. This is a review of upstream papers, documentation, and code; it does not establish which configuration wins on Cassini recordings.

## Decoder investigation supersedes padding optimization

An upstream reproducer reports the same failure family: Parakeet v3 with modified beam search emits empty text or “Yeah.” on audible speech, while greedy decoding recovers it. The report used an older release, so it corroborates the symptom rather than proving Cassini's cause. [Issue #3267](https://github.com/k2-fsa/sherpa-onnx/issues/3267)

Inspection of the exact **v1.13.7** source establishes:

- Modified beam search explicitly supports TDT. It chooses one duration for all token candidates. When that duration is zero, a blank is scored with the zero-duration probability but forcibly advances one frame: the score describes a different transition from the one executed. This is a concrete scoring inconsistency. [Beam decoder](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/offline-transducer-modified-beam-search-nemo-decoder.cc#L277)
- Each stream's feature matrix is normalized across **all its frames**, per feature, before batching. The normalizer subtracts each feature's temporal mean and divides by standard deviation plus epsilon. Consequently, adding PCM silence changes features throughout the utterance, not merely at the end. This supplies a concrete mechanism by which padding changes existing speech predictions, but does not itself establish why beam search drops whole passages. [Feature extraction](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/offline-stream.cc#L219), [normalization](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/math.cc#L134)
- The NeMo recognizer reads normalization configuration from model metadata, pads already-normalized feature matrices with zeros for batching, and passes actual feature lengths into the encoder. Such batch padding differs from Cassini's PCM silence, which participates in feature extraction and normalization. [Recognizer](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/offline-recognizer-transducer-nemo-impl.h#L160)
- A known variance-cancellation bug was fixed before this release; v1.13.7 computes variance from centered values. Do not attribute this incident to that older arithmetic bug without further evidence. [Normalization fix #3857](https://github.com/k2-fsa/sherpa-onnx/pull/3857)

The proposed upstream fix [PR #3657](https://github.com/k2-fsa/sherpa-onnx/pull/3657) remains open and requests reproduction/validation. Its assertion that a blank-plus-zero-duration guard is missing does **not** describe v1.13.7: that version already advances blank hypotheses by at least one frame. The PR also changes duration scoring and symbol limits, but its effectiveness is not established here. It should not be copied as a validated solution.

NeMo's reference TDT beam implementation excludes zero-duration blank transitions and includes the probability of the positive duration actually selected. It also explores alternative token-duration pairs and merges equivalent hypotheses. **Correction to the earlier hypothesis:** the coexistence of hypotheses at different frame offsets alone is not proof of a bug; the reference also retains future-frame hypotheses. [Reference TDT beam](https://github.com/NVIDIA-NeMo/Speech/blob/main/nemo/collections/asr/parts/submodules/tdt_beam_decoding.py#L434)

A [runnable one-frame counterexample](../harness/bin/test_tdt_legal_blank.py) demonstrates the narrower scoring problem without audio: the existing beam selects empty output with an inflated probability of 0.36; the legal empty transition has probability 0.04; the speech path has probability 0.2916. Correcting the blank transition makes beam select speech, as greedy does. This proves the algorithmic inconsistency can change output, not that it caused a specific recording's omission.

An [opt-in native experiment](../harness/patches/README-tdt-legal-blank.md) changes only blank duration selection and scoring, preserving non-blank behavior and existing defaults. It is a minimal correction, not full joint token-duration search. The causal recording test must hold frontend, audio, segmentation, bias and runtime fixed while toggling this correction, and inspect both speech recovery and regressions. Greedy/beam and hotword ablations remain separate checks. The earlier padding sweep is diagnostic evidence, not a justification for a new magic-number default.

### Completed native validation: limits of the decoder corrections

Across 32 native legal-blank cases (16 excerpts with each of two frontend variants), normalized output token sequences were unchanged from the corresponding original beam decoder. The correction did not recover the reported omissions in this test.

A further 48-condition direct-crop probe separated final ranking from earlier search. On Chris's 9.308-second crop plus a 500 ms tail, the empty path scored −15.7268, ahead of a 17-token partial path at −20.2669. NeMo-style final length normalization selected that partial path, but the missing prefix was absent from all surviving final candidates. On Silvio's crop, the corrected frontend with beam still selected “Yeah”; the full phrase was absent from the final beam.

The scoring inconsistency is established independently, but **is not an established cause of these recording failures**. Final-score normalization is also insufficient. These traces leave candidate generation/pruning and acoustic input differences before final ranking as mechanisms to distinguish; they do not identify a unique earlier failure.

### Frontend parity is a separate hypothesis

The default sherpa feature configuration uses a Povey window and `snip_edges=false`. Its NeMo initializer sets `is_librosa=true`, but the Hann assignment is commented out. That flag selects **mel-filter construction only**; it does not switch the STFT window or edge treatment. Sherpa v1.13.7 pins kaldi-native-fbank v1.22.3. [Sherpa feature defaults](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/sherpa-onnx/csrc/features.h), [pinned dependency](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/cmake/kaldi-native-fbank.cmake), [mel-filter implementation](https://github.com/csukuangfj/kaldi-native-fbank/blob/v1.22.3/kaldi-native-fbank/csrc/mel-computations.cc)

There are concrete differences from the current Hugging Face Parakeet frontend: HF uses a symmetric Hann window, centered STFT with constant-zero edge padding, and normalization variance divided by N−1. KNF's non-snipped frames start with a center at half a frame shift (5 ms here), reflect boundary samples, and sherpa's normalizer divides by N. KNF calls its periodic Hann window `hann` and its symmetric Hann `hanning`, so changing the window string alone does not establish parity. [KNF windows/framing](https://github.com/csukuangfj/kaldi-native-fbank/blob/v1.22.3/kaldi-native-fbank/csrc/feature-window.cc), [HF Parakeet frontend](https://github.com/huggingface/transformers/blob/main/src/transformers/models/parakeet/feature_extraction_parakeet.py)

Even sherpa's Parakeet export test explicitly selects `hann`, unlike its production recognizer default. The v3 test points to the v2 test implementation. These are verified source differences, **not proof that any one causes the observed omissions**. Compare extracted tensors and identical crops against the reference implementation before changing the frontend. [Sherpa export test](https://github.com/k2-fsa/sherpa-onnx/blob/v1.13.7/scripts/nemo/parakeet-tdt-0.6b-v2/test_onnx.py)

## What the sources support

| Question | Evidence | Implication for Cassini |
| --- | --- | --- |
| Is 10 seconds an inherent Parakeet limit? | NVIDIA's v3 model card supports much longer offline inputs: up to 24 minutes with full attention on an A100 80 GB, or 3 hours with local attention. The local-attention example sets `[256, 256]`. [Model card](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3#transcribing-long-form-audio) | Ten seconds is an application tradeoff, not a model requirement. These NeMo capabilities do not establish the memory behavior of Cassini's ONNX export. |
| How does NVIDIA handle chunk boundaries? | The same model card demonstrates 2 seconds of new audio with 10 seconds of left context and 2 seconds of right context. [Model card](https://huggingface.co/nvidia/parakeet-tdt-0.6b-v3#streaming-with-parakeet-models) | Context means surrounding recorded audio, not zero samples. |
| Is there a concrete long-recording recipe? | NeMo's buffered/streaming inference script recommends 10 seconds left / 10 seconds new / 5 seconds right for long files. It runs the encoder on the full buffer, discards context encoder frames before decoding, and carries decoder state across chunks. [NeMo implementation](https://github.com/NVIDIA-NeMo/Speech/blob/main/examples/asr/asr_chunked_inference/rnnt/speech_to_text_streaming_infer_rnnt.py) | This is not equivalent to independently decoding 10-second WAVs with 500 ms overlap and concatenating their text. Upstream's suggested numbers apply to its algorithm; transplanting them alone would not reproduce it. |
| Does the literature prescribe 500 ms of synthetic trailing silence? | Sherpa-onnx's example specifically for offline Parakeet TDT v3 passes the waveform directly to `OfflineRecognizer` and decodes, without appending silence. [Offline Parakeet example](https://github.com/k2-fsa/sherpa-onnx/blob/master/python-api-examples/offline-nemo-parakeet-decode-file.py) | I found no primary-source validation of a universally optimal 500 ms tail for this offline model. Keep zero padding as an experimental variable. |
| Why do some examples append silence? | K2's ASR demo has separate paths: the offline ONNX path accepts original samples directly; the online path adds 300 ms leading and 600 ms trailing zeros and calls `input_finished()`. [K2 demo implementation](https://huggingface.co/spaces/k2-fsa/automatic-speech-recognition/blob/main/model.py) | Streaming flush examples are not evidence that offline Parakeet needs the same padding. |
| What should happen at VAD boundaries? | Silero's reference utility defaults to 30 ms padding on each side, implemented by expanding timestamps into the original waveform. It recommends dataset-specific threshold tuning and prefers a silence boundary when splitting overlong speech. [Silero utility](https://github.com/snakers4/silero-vad/blob/master/src/silero_vad/utils_vad.py) | Preserve actual audio before and after detected speech. Appending zeros cannot recover speech excluded by an incorrect VAD boundary. The reference utility's defaults are not necessarily sherpa-onnx's behavior. |

## Papers: useful evidence and limits

The original TDT paper explains that the decoder jointly predicts tokens and durations and can skip encoder frames. It motivates checking model-specific decoding and alignment behavior, but does not prescribe Cassini's chunk size, overlap, or silence padding. [Xu et al., ICML 2023](https://proceedings.mlr.press/v202/xu23g.html)

Section 6.4.1 of the Canary/Parakeet technical report describes 30–40-second chunks with 1-second overlap and token-level longest-common-subsequence merging. That passage explicitly concerns a FastConformer encoder paired with a Transformer decoder: **Canary**, not Parakeet's TDT decoder. It supports overlap plus explicit reconciliation as a general technique, but does not establish a Parakeet optimum. [NVIDIA technical report, §6.4.1](https://arxiv.org/html/2509.14128v1#S6.SS4.SSS1)

## Recording experiment suggested by this evidence

These are proposed Cassini measurements, not claims of published optimal settings:

1. Preserve a human-checked reference for the reported phrases and several independent meeting excerpts. Gemini can supply a second hypothesis; listen and resolve disagreements before scoring.
2. Re-decode the original continuous participant audio around each phrase, then compare the existing VAD crop. This separates missing source samples from recognition failure inside a retained crop.
3. Vary one factor at a time initially: original-waveform head/tail context (0, 250, 500, 1000 ms), synthetic tail (0, 250, 500, 1000 ms), chunk duration (5, 10, 15, 25 seconds), and overlap (0, 500, 1000, 2000 ms). Include an unsplit VAD-span baseline.
4. Shift window start positions as well as lengths. A recovered phrase at one favorable alignment is weaker evidence than recovery across offsets and held-out meetings.
5. Compare word error rate and deletion rate, duplicate boundary words, empty outputs despite speech, runtime, and timestamps. Count errors after overlap reconciliation as well as before it: more recognized words alone can include duplicates or hallucinations.
6. Evaluate a separate architectural candidate using real left/right context and owned central output regions. If staying with independent offline decodes, test timestamp-aware boundary reconciliation and preserve legitimate repeated words (such as “my my” and “plan plan”). NeMo's stateful decoder algorithm is a distinct implementation option, not a parameter-only change.

The sources justify testing context preservation and boundary reconciliation. They do not establish 10 seconds, 500 ms tail, or 500 ms overlap as optimal for Cassini's model export and meeting audio.
