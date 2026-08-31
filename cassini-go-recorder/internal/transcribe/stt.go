package transcribe

import (
	"fmt"
	"log"
	"sort"
	"strings"
	"unicode"

	sherpa "github.com/k2-fsa/sherpa-onnx-go/sherpa_onnx"
)

// Word is a single transcribed word with millisecond timestamps.
type Word struct {
	Text    string
	StartMS int64
	EndMS   int64

	// AttributionGapDB is how far the loudest OTHER participant track sat above
	// this word's own track at this instant, each measured against its own noise
	// floor. Near zero means the attributed speaker really was the loudest voice;
	// a large positive gap means somebody else was, and this word is a crosstalk
	// candidate. Set by AnnotateAttribution; see attribution.go.
	AttributionGapDB float64
	// HasAttributionGap distinguishes "measured 0 dB" from "never measured".
	HasAttributionGap bool
	// LowConfidenceSpeaker marks a word whose gap cleared this meeting's own
	// estimated crosstalk threshold. The word is kept — the transcript stays
	// canonical — but consumers may grey it, exclude it from a summary, or ask a
	// human. It is never set unless the meeting shows an unambiguous crosstalk
	// population.
	LowConfidenceSpeaker bool

	// extentCap is the furthest EndMS this word is ever allowed to reach: the
	// end of its last token INCLUDING a trailing punctuation mark. EndMS itself
	// stops at the last speech-bearing token, which Parakeet's 320ms-capped
	// duration head routinely leaves short of the real acoustic end, so the
	// energy gate may push EndMS forward over the speaker's own continuing
	// audio — but never past this cap, which is the end the punctuation-
	// inclusive rule would have produced. Keeping it as a ceiling rather than
	// as the end is what stops a sentence-final mark, stamped at the *next*
	// onset, from stretching a word across the pause that follows it.
	//
	// It is decode-pipeline scaffolding, not transcript data: unexported so it
	// cannot reach any JSON artifact, and cleared by filterWordsByEnergy once
	// applied. A zero (or otherwise not-greater-than-EndMS) value means "no
	// extension permitted", so Words built anywhere else are left alone.
	extentCap int64
}

// extentCapMS is the ceiling filterWordsByEnergy may extend this word to. An
// unset cap collapses to the word's own end, so the rule can only ever leave
// such a word exactly as it was.
func (w Word) extentCapMS() int64 {
	if w.extentCap > w.EndMS {
		return w.extentCap
	}
	return w.EndMS
}

// Recognizer wraps a sherpa-onnx offline recognizer with Silero VAD segmentation.
type Recognizer struct {
	r          *sherpa.OfflineRecognizer
	vad        *sherpa.VoiceActivityDetector
	sampleRate int
}

// vadWindowSamples is the configured SileroVad.WindowSize. sherpa-onnx can
// buffer a non-window remainder between AcceptWaveform calls, but its Flush
// method does not evaluate that final remainder. Feeding exact windows (and
// zero-padding only the final one) therefore preserves every input sample and
// follows the upstream detector's intended call shape.
const vadWindowSamples = 512

// The stock 0.5 Silero threshold missed direct acknowledgements as short as
// 420-500ms in real per-participant tracks. The lower threshold recovers those
// turns; the conservative energy gate below prevents newly admitted digital
// silence and near-silence from becoming attributed ASR hallucinations.
const (
	vadSpeechThreshold       = 0.18
	vadMinSpeechDuration     = 0.10
	minimumWordPeakAmplitude = 0.001  // -60 dBFS
	minimumWordRMSAmplitude  = 0.0001 // -80 dBFS
	minimumActiveAmplitude   = 0.0005 // -66 dBFS
	minimumActiveDurationMS  = 5
	wordEnergyPreMarginMS    = int64(100)
	wordEnergyPostMarginMS   = int64(200)
)

// Constants for the word-end extension in filterWordsByEnergy.
//
// wordEndScanWindowMS is the resolution the owner's own track is examined at.
// A window counts as active on exactly the gate's existing terms: at least
// minimumActiveDurationMS of it at or above minimumActiveAmplitude (-66 dBFS).
// 10ms is one feature frame of the model's front end and twice the minimum
// active duration, so a window can neither be won nor lost by a single sample.
//
// wordEndGapToleranceMS is how long a silence inside the extension is still
// part of the word rather than the end of it. It is the gate's own
// wordEnergyPostMarginMS, and for the same reason: Parakeet has been measured
// placing a word up to 180ms before its PCM (see
// TestFilterWordsByEnergyAllowsMeasuredDecoderLead), so the gap between a
// word's stamped end and the rest of its own sound is jitter of that order,
// and an unvoiced stop closure inside a word ("okay", "that") is shorter
// still. Measured over the 506 capped words of the reference meeting, against
// the recorder's own samples: 80ms leaves 18 words missing at least 100ms of
// their own continuing audio, 160ms leaves 4, 200ms leaves 3 — and those three
// are the next utterance's onset arriving before the mark that closed this
// word, which the next word covers. Bridging further would start absorbing
// other turns, which is the bug this whole rule exists to prevent.
//
// The word ends at the last active window and no further. There is
// deliberately no grace past it: the scan has just measured the audio itself
// at 10ms resolution, which settles the question the model's 80ms timestamp
// quantisation left open, and every window beyond the last active one is by
// definition below the gate's own activity floor — sub-threshold tail or
// silence, and the measurement showed such a window can instead hold another
// speaker.
//
// Extending is bounded by the cap in every case, so the walk can never reach
// further than the punctuation-inclusive rule already did.
const (
	wordEndScanWindowMS   = int64(10)
	wordEndGapToleranceMS = wordEnergyPostMarginMS
)

// vadDrainEverySamples controls how often queued speech segments are popped
// and transcribed while feeding (every ~5 seconds at 16 kHz). Draining as we
// go keeps the VAD's internal circular buffer small instead of letting whole
// sparse tracks accumulate.
const vadDrainEverySamples = 16000 * 5

// maxSafeSegmentSamples is the safety-fallback split size (55 seconds at 16 kHz).
// VAD MaxSpeechDuration=25s keeps segments short, but pathological silence-free
// speech could still exceed that; 55s gives a comfortable ONNX-safe ceiling.
const maxSafeSegmentSamples = 16000 * 55

// nonVADWindowSamples / nonVADWindowOverlapSamples define the fixed sliding
// window used by the non-VAD (useVAD=false) chunked decode. The merged-mix
// fallback feeds ~75s of dense audio with no silence boundaries; decoding it
// as one or two huge spans makes the int8 Parakeet decoder land a single
// low-confidence result whose word run is unstable across runs. Splitting into
// short (~15s) windows keeps each int8 decode short and high-confidence, so one
// bad span can no longer zero the whole transcript. The 0.5s overlap ensures a
// word straddling a window boundary is captured by at least one window; the
// overlap is de-duplicated by order-preserving text/time alignment (see
// transcribeNonVADChunked).
const (
	nonVADWindowSamples        = 16000 * 15 // 15s window at 16 kHz
	nonVADWindowOverlapSamples = 16000 / 2  // 0.5s overlap at 16 kHz
	vadDecodeWindowSamples     = 16000 * 10 // 10s window at 16 kHz
	vadDecodeWindowOverlap     = 16000 / 2  // 0.5s overlap at 16 kHz
	vadDecodeWindowGrace       = 16000 / 2  // keep up to 10.5s as one decode
	vadDecodeMinTerminal       = 16000 * 5  // give a final decode useful context
	overlapDuplicateTolerance  = int64(400) // measured adjacent-decode shift: ~300ms
	overlapSingletonTolerance  = int64(200) // conservative for a lone repeated word
	overlapShiftConsistency    = int64(100) // support wider matches with a phrase
)

func newVADModelConfig(modelPath string, sampleRate int) sherpa.VadModelConfig {
	cfg := sherpa.VadModelConfig{}
	cfg.SileroVad.Model = modelPath
	cfg.SileroVad.Threshold = vadSpeechThreshold
	cfg.SileroVad.MinSilenceDuration = 0.5
	cfg.SileroVad.MinSpeechDuration = vadMinSpeechDuration
	cfg.SileroVad.WindowSize = vadWindowSamples
	cfg.SileroVad.MaxSpeechDuration = 25.0
	cfg.SampleRate = sampleRate
	// Silero VAD is a tiny stateful model run per 32 ms window; it is fastest
	// single-threaded on CPU. Running it on a GPU provider turns each window
	// into a micro kernel-launch (measured ~3x slower on sparse/long streams),
	// so VAD stays on CPU regardless of the recogniser device. See vadProvider.
	cfg.NumThreads = 1
	cfg.Provider = vadProvider()
	cfg.Debug = 0
	return cfg
}

// NewRecognizer creates an offline recognizer from the given model paths and a
// Silero VAD model. provider is "cpu" or "cuda"; vadModelPath is the path to
// silero_vad.onnx.
func NewRecognizer(paths ModelPaths, vadModelPath, provider string, numThreads int) (*Recognizer, error) {
	if numThreads < 1 {
		numThreads = 4
	}

	cfg := sherpa.OfflineRecognizerConfig{}
	cfg.FeatConfig.SampleRate = paths.SampleRate
	cfg.FeatConfig.FeatureDim = paths.FeatureDim
	if paths.EncoderFile != "" {
		// Transducer model (encoder + decoder + joiner).
		cfg.ModelConfig.Transducer.Encoder = paths.EncoderFile
		cfg.ModelConfig.Transducer.Decoder = paths.DecoderFile
		cfg.ModelConfig.Transducer.Joiner = paths.JoinerFile
	} else {
		// CTC model (single model file).
		cfg.ModelConfig.NemoCTC.Model = paths.ModelFile
	}
	cfg.ModelConfig.Tokens = paths.TokensFile
	cfg.ModelConfig.ModelType = paths.ModelType
	cfg.ModelConfig.NumThreads = numThreads
	cfg.ModelConfig.Provider = provider
	cfg.ModelConfig.Debug = 0

	r := sherpa.NewOfflineRecognizer(&cfg)
	if r == nil {
		return nil, fmt.Errorf("failed to create sherpa-onnx recognizer (check model paths and provider %q)", provider)
	}

	vadCfg := newVADModelConfig(vadModelPath, paths.SampleRate)

	vad := sherpa.NewVoiceActivityDetector(&vadCfg, 60.0)
	if vad == nil {
		sherpa.DeleteOfflineRecognizer(r)
		return nil, fmt.Errorf("failed to create Silero VAD (check model path: %s)", vadModelPath)
	}

	return &Recognizer{r: r, vad: vad, sampleRate: paths.SampleRate}, nil
}

// Transcribe runs ASR on the given float32 samples (16 kHz, mono, [-1,1]).
//
// When useVAD is true, Silero VAD chunks the input at natural silence
// boundaries before each segment is sent to the ASR model. This is the
// right choice for genuinely sparse speech (per-participant tracks where
// each bot is muted ~25/30s per rotation, real meetings with pauses).
//
// When useVAD is false, the input is split into fixed overlapping windows
// (~15s window, ~0.5s overlap) and each window is decoded through
// transcribeSegment, with words de-duplicated across the overlap region by
// text/time alignment (see transcribeNonVADChunked). Use this when the caller
// already knows the audio is dense and continuous — the merged-fallback path
// against the rotated mix is the canonical case. A single full-length decode
// of that ~75s mix lands one giant low-confidence int8 span whose word run is
// unstable run-to-run; short windows keep each int8 decode short and
// high-confidence so one bad span can't zero the whole transcript. Bypassing
// VAD also removes the Silero failure surface — its default 0.5 threshold has
// been observed to reject loud (-19 to -29 dB) dense audio in ~17-33% of CI
// runs.
//
// Word timestamps refer to the full recording timeline either way.
func (r *Recognizer) Transcribe(samples []float32, sampleRate int, useVAD bool) ([]Word, error) {
	if len(samples) == 0 {
		return nil, nil
	}
	audioEndMS := int64(len(samples)) * 1000 / int64(sampleRate)

	if !useVAD {
		words, err := r.transcribeNonVADChunked(samples, sampleRate)
		if err != nil {
			return nil, err
		}
		decodedWords := len(words)
		words = finalizeTranscriptWords(samples, sampleRate, words, audioEndMS, 0)
		if len(words) == 0 && len(samples) >= sampleRate*5 {
			audioSeconds := float64(len(samples)) / float64(sampleRate)
			log.Printf("transcribe: 0 words from %.1fs of audio (VAD bypassed); ASR decoded=%d, retained=%d after energy gate", audioSeconds, decodedWords, len(words))
		}
		return words, nil
	}

	r.vad.Reset()

	var allWords []Word
	var segCount int
	var totalSpeechSamples int
	var vadTailPaddingSamples int

	// drainSegments transcribes every speech segment the VAD has queued so far.
	drainSegments := func() error {
		for !r.vad.IsEmpty() {
			seg := r.vad.Front()
			r.vad.Pop()
			if seg == nil || len(seg.Samples) == 0 {
				continue
			}
			segCount++
			totalSpeechSamples += len(seg.Samples)

			// seg.Start is the sample index of the segment start within the full recording.
			segOffsetMS := int64(seg.Start) * 1000 / int64(sampleRate)

			words, err := r.transcribeSegment(seg.Samples, sampleRate, segOffsetMS, true)
			if err != nil {
				return err
			}
			allWords = append(allWords, words...)
		}
		return nil
	}

	// Feed audio to the VAD one configured window per call. Draining completed
	// segments periodically is the important memory bound: without it, a long
	// sparse track leaves completed speech queued while the internal circular
	// buffer grows for the duration of the recording.
	sinceDrain := 0
	for off := 0; off < len(samples); off += vadWindowSamples {
		end := off + vadWindowSamples
		if end > len(samples) {
			end = len(samples)
		}
		window := samples[off:end]
		if len(window) < vadWindowSamples {
			// Flush only closes an already-detected speech segment; it does not
			// run the detector over sherpa-onnx's buffered partial window.
			padded := make([]float32, vadWindowSamples)
			copy(padded, window)
			vadTailPaddingSamples = vadWindowSamples - len(window)
			window = padded
		}
		r.vad.AcceptWaveform(window)
		sinceDrain += end - off
		if sinceDrain >= vadDrainEverySamples {
			sinceDrain = 0
			if err := drainSegments(); err != nil {
				return nil, err
			}
		}
	}
	r.vad.Flush()
	if err := drainSegments(); err != nil {
		return nil, err
	}
	// The final VAD window is zero-padded to its configured call size. Keep that
	// detector-only padding, and any decoder tail padding, out of the public
	// recording timeline. Apply the energy gate before diagnostics so a decode
	// containing only silent hallucinations is still reported as zero retained
	// words rather than suppressing the warning.
	decodedWords := len(allWords)
	allWords = finalizeTranscriptWords(samples, sampleRate, allWords, audioEndMS, samplesToCeilMS(vadTailPaddingSamples, sampleRate))
	if len(allWords) == 0 && len(samples) >= sampleRate*5 {
		audioSeconds := float64(len(samples)) / float64(sampleRate)
		speechSeconds := float64(totalSpeechSamples) / float64(sampleRate)
		log.Printf("transcribe: 0 words from %.1fs of audio; VAD segments=%d totalling %.1fs of speech; ASR decoded=%d, retained=%d after energy gate", audioSeconds, segCount, speechSeconds, decodedWords, len(allWords))
	}
	return allWords, nil
}

// WordEndsAreBoundedByAudio declares the AudioBoundedWordEnds guarantee for
// the bundled decoder: this recognizer's words reach the caller only through
// finalizeTranscriptWords, which ends each one where the speaker's own audio
// ends (filterWordsByEnergy) instead of at its last token's timestamp.
//
// The declaration lives next to the code that earns it, and both return paths
// of Transcribe above go through that one function — the only other exit is
// the empty-input case, which returns no words to make a claim about. A build
// that stopped measuring would have to delete this method, and the compile-time
// AudioBoundedWordEnds assertion in backend.go turns that into a build failure
// rather than a manifest that quietly keeps claiming it.
func (r *Recognizer) WordEndsAreBoundedByAudio() bool { return true }

func finalizeTranscriptWords(samples []float32, sampleRate int, words []Word, audioEndMS, paddedTailMS int64) []Word {
	words = clampWordsToTimelineEnd(words, audioEndMS, paddedTailMS)
	return filterWordsByEnergy(samples, sampleRate, words)
}

// filterWordsByEnergy decides, against the owner's own track, both whether a
// word is real and how far it reaches.
//
// It drops decoder output whose source interval is digital silence or
// near-silence. A 100ms pre-margin and 200ms post-margin tolerate measured
// model timestamp jitter without widening both sides unnecessarily. Requiring
// a -60 dBFS peak, -80 dBFS RMS, and 5ms of active samples rejects isolated
// clicks while remaining conservative around quiet acknowledgements.
//
// A retained word is then extended forward over its speaker's own continuing
// audio, up to its extentCap (see Word). Parakeet's duration head saturates at
// 320ms, so the last speech token of a longer word stops hundreds of
// milliseconds short of the sound; without this the word would be clipped
// mid-syllable. The cap is what the punctuation-inclusive rule would have
// produced, so the end can never exceed the legacy end — which bounds
// fabricated overlap at the level the punctuation-inclusive rule already
// produced rather than eliminating it — and the extension never goes
// backwards, so no real speech can be cut. The cap is cleared on the way out:
// it is decode scaffolding and nothing downstream has any business reading it.
//
// The measurement is deliberately local to this stage, which already owns the
// owner's samples and the -66 dBFS / 5ms activity terms. It does not borrow
// the attribution stage's envelope: word timing must not depend on whether or
// how attribution ran.
//
// The input slice may be compacted in place.
func filterWordsByEnergy(samples []float32, sampleRate int, words []Word) []Word {
	if len(words) == 0 || len(samples) == 0 || sampleRate <= 0 {
		return words
	}
	audioEndMS := int64(len(samples)) * 1000 / int64(sampleRate)
	minimumActiveSamples := (sampleRate*minimumActiveDurationMS + 999) / 1000
	kept := words[:0]
	for _, word := range words {
		startMS, endMS := word.StartMS, word.EndMS
		if endMS < 0 || endMS < startMS || startMS > audioEndMS {
			continue
		}
		if startMS < 0 {
			startMS = 0
		}
		if endMS > audioEndMS {
			endMS = audioEndMS
		}
		if startMS > wordEnergyPreMarginMS {
			startMS -= wordEnergyPreMarginMS
		} else {
			startMS = 0
		}
		if endMS < audioEndMS-wordEnergyPostMarginMS {
			endMS += wordEnergyPostMarginMS
		} else {
			endMS = audioEndMS
		}
		start := int(startMS * int64(sampleRate) / 1000)
		end := int((endMS*int64(sampleRate) + 999) / 1000)
		if end > len(samples) {
			end = len(samples)
		}
		if end <= start {
			continue
		}
		var peak float32
		var squareSum float64
		activeSamples := 0
		for _, sample := range samples[start:end] {
			squareSum += float64(sample) * float64(sample)
			if sample < 0 {
				sample = -sample
			}
			if sample >= minimumActiveAmplitude {
				activeSamples++
			}
			if sample > peak {
				peak = sample
			}
		}
		meanSquare := squareSum / float64(end-start)
		minimumMeanSquare := float64(minimumWordRMSAmplitude) * float64(minimumWordRMSAmplitude)
		if peak >= minimumWordPeakAmplitude && meanSquare >= minimumMeanSquare && activeSamples >= minimumActiveSamples {
			word.EndMS = wordEndOverContinuingAudio(samples, sampleRate, word, audioEndMS)
			word.extentCap = 0
			kept = append(kept, word)
		}
	}
	return kept
}

// wordEndOverContinuingAudio returns the word's end after following its
// speaker's own audio forward from the end of its last speech-bearing token.
//
// It walks wordEndScanWindowMS windows from word.EndMS towards the cap, using
// the gate's own activity terms, and keeps going while the audio is still
// there; a gap shorter than wordEndGapToleranceMS is bridged (a stop closure
// inside a word is not the end of the word), a longer one ends the walk. The
// end lands at the last active window, and never past the cap.
//
// Two properties hold by construction, and are pinned by tests:
//   - the result is never above the cap, so it is never above the end the
//     punctuation-inclusive rule produced. That bounds fabricated overlap by
//     what the legacy rule already produced; it does not eliminate it, because
//     the legacy end could itself sit over a neighbour's speech;
//   - the result is never below word.EndMS, so it cannot truncate speech, move
//     a start, or collapse a word to zero length.
//
// A word whose audio has genuinely stopped finds no active window and keeps
// the end its tokens gave it. A word that is silent throughout never reaches
// here: the gate above has already dropped it.
func wordEndOverContinuingAudio(samples []float32, sampleRate int, word Word, audioEndMS int64) int64 {
	end := word.EndMS
	capMS := word.extentCapMS()
	if capMS > audioEndMS {
		capMS = audioEndMS
	}
	if capMS <= end || sampleRate <= 0 {
		return end
	}
	windowSamples := int((int64(sampleRate)*wordEndScanWindowMS + 999) / 1000)
	if windowSamples <= 0 {
		return end
	}
	minimumActiveSamples := (sampleRate*minimumActiveDurationMS + 999) / 1000
	lastActiveMS := int64(-1)
	for at := end; at < capMS; at += wordEndScanWindowMS {
		start := int(at * int64(sampleRate) / 1000)
		stop := start + windowSamples
		if stop > len(samples) {
			stop = len(samples)
		}
		if start < 0 || start >= stop {
			break
		}
		active := 0
		for _, sample := range samples[start:stop] {
			if sample < 0 {
				sample = -sample
			}
			if sample >= minimumActiveAmplitude {
				active++
			}
		}
		if active >= minimumActiveSamples {
			lastActiveMS = at + wordEndScanWindowMS
			continue
		}
		since := lastActiveMS
		if since < 0 {
			since = end
		}
		if at-since >= wordEndGapToleranceMS {
			break
		}
	}
	if lastActiveMS < 0 {
		// Nothing but silence follows the last spoken token. The token end is
		// the whole truth about this word.
		return end
	}
	extended := lastActiveMS
	if extended > capMS {
		extended = capMS
	}
	if extended < end {
		extended = end
	}
	return extended
}

// transcribeSegment transcribes one source span, splitting it into sub-chunks
// if it exceeds maxSafeSegmentSamples. segOffsetMS is the ms position of seg[0]
// within the full recording. vadSegment means the source span came from VAD and
// therefore ends at a detected speech boundary with its closing silence removed.
func (r *Recognizer) transcribeSegment(samples []float32, sampleRate int, segOffsetMS int64, vadSegment bool) ([]Word, error) {
	// Silero can merge quiet pauses until MaxSpeechDuration forces a roughly
	// 25s segment. Parakeet TDT v3 sometimes emits only a prefix (or nothing)
	// for those long, abrupt spans even with trailing silence. Decode long VAD
	// spans through 10s windows with 0.5s overlap, padding each child window
	// below and de-duplicating the seam. The shorter VAD window is deliberate:
	// one clear production turn remained partial with the 15s fallback window.
	if vadSegment && sampleRate > 0 {
		bounds := vadSegmentWindowBounds(len(samples), sampleRate)
		if len(bounds) > 1 {
			overlapSamples := scaleSamples(vadDecodeWindowOverlap, sampleRate)
			overlapMS := int64(overlapSamples) * 1000 / int64(sampleRate)
			var windowedWords []Word
			for i, win := range bounds {
				windowOffsetMS := segOffsetMS + int64(win.start)*1000/int64(sampleRate)
				words, err := r.transcribeSegment(samples[win.start:win.end], sampleRate, windowOffsetMS, true)
				if err != nil {
					return nil, err
				}
				windowedWords = dedupOverlappingWords(windowedWords, words, i == 0, windowOffsetMS, overlapMS)
			}
			return windowedWords, nil
		}
	}

	var allWords []Word
	for start := 0; start < len(samples); start += maxSafeSegmentSamples {
		end := start + maxSafeSegmentSamples
		if end > len(samples) {
			end = len(samples)
		}
		chunk := samples[start:end]

		// Parakeet TDT v3 can return zero or partial tokens when a chunk ends
		// abruptly. VAD deliberately strips the closing silence from every
		// speech segment, including long segments forced closed at Silero's
		// MaxSpeechDuration, so every VAD decode needs a short synthetic tail.
		// Non-VAD windows retain the established short-chunk workaround while
		// avoiding a decode change for the normal 15s sliding windows.
		decoderTailPaddingSamples := decoderTailPadSamples(len(chunk), sampleRate, vadSegment)
		if decoderTailPaddingSamples > 0 {
			padded := make([]float32, len(chunk)+decoderTailPaddingSamples) // +0.5s
			copy(padded, chunk)
			chunk = padded
		}

		stream := sherpa.NewOfflineStream(r.r)
		if stream == nil {
			return nil, fmt.Errorf("failed to create offline stream")
		}
		stream.AcceptWaveform(sampleRate, chunk)
		r.r.Decode(stream)
		result := stream.GetResult()
		var words []Word
		if result != nil {
			// Copy result data before deleting the stream — result may point
			// into stream-owned memory.
			words = tokensToWords(result.Tokens, result.Timestamps, result.Durations)
		}
		sherpa.DeleteOfflineStream(stream)

		// Offset timestamps: chunk start within segment + segment start within recording.
		chunkOffsetMS := segOffsetMS + int64(start)*1000/int64(sampleRate)
		for i := range words {
			words[i].StartMS += chunkOffsetMS
			words[i].EndMS += chunkOffsetMS
			// The cap lives on the same timeline as the end it bounds. An unset
			// cap stays unset in effect: it can only ever be shifted to a value
			// at or below the shifted end, which extentCapMS reads as "no
			// extension".
			words[i].extentCap += chunkOffsetMS
		}
		// The recognizer may timestamp a genuine final token inside the synthetic
		// 0.5s decoder tail. Clamp tokens stamped within the actual padding to a
		// zero-length word at the real boundary; the later energy gate decides
		// whether the real audio tail supports them. Discard only timestamps beyond
		// the padding that was supplied to this decode.
		chunkEndMS := segOffsetMS + int64(end)*1000/int64(sampleRate)
		words = clampWordsToTimelineEnd(words, chunkEndMS, samplesToCeilMS(decoderTailPaddingSamples, sampleRate))
		allWords = append(allWords, words...)
	}
	return allWords, nil
}

func vadSegmentWindowBounds(total, sampleRate int) []windowBound {
	windowSamples := scaleSamples(vadDecodeWindowSamples, sampleRate)
	overlapSamples := scaleSamples(vadDecodeWindowOverlap, sampleRate)
	if total <= windowSamples+scaleSamples(vadDecodeWindowGrace, sampleRate) {
		if total <= 0 {
			return nil
		}
		return []windowBound{{start: 0, end: total}}
	}
	bounds := nonVADWindowBounds(total, windowSamples, overlapSamples)
	// A source only one sample longer than a window would otherwise produce a
	// final child containing roughly 0.5s of overlap and no useful new speech.
	// Keep spans up to 10.5s whole. For longer spans, rebalance a terminal child
	// under 5s so every child has useful context while retaining exact 0.5s
	// overlaps and gap-free coverage.
	if len(bounds) > 1 {
		last := len(bounds) - 1
		minTerminalSamples := scaleSamples(vadDecodeMinTerminal, sampleRate)
		if bounds[last].end-bounds[last].start < minTerminalSamples {
			bounds[last].start = total - minTerminalSamples
			bounds[last-1].end = bounds[last].start + overlapSamples
		}
	}
	return bounds
}

const decoderTailPadMinSeconds = 10

func decoderTailPadSamples(chunkSamples, sampleRate int, vadSegment bool) int {
	if chunkSamples <= 0 || sampleRate <= 0 {
		return 0
	}
	if vadSegment || chunkSamples < decoderTailPadMinSeconds*sampleRate {
		return sampleRate / 2
	}
	return 0
}

// clampWordsToTimelineEnd clips tokens that straddle a real PCM boundary.
// Tokens stamped at the boundary or within the synthetic padded tail are
// retained as zero-length boundary words so the energy gate can inspect the
// real audio immediately before them. Tokens starting beyond the supplied
// padding are removed. Decoder and VAD padding never extends public timestamps.
// The input slice may be compacted in place.
func clampWordsToTimelineEnd(words []Word, endMS, paddedTailMS int64) []Word {
	if paddedTailMS < 0 {
		paddedTailMS = 0
	}
	paddedEndMS := endMS + paddedTailMS
	if paddedEndMS < endMS { // Saturate on malformed/overflowing input.
		paddedEndMS = int64(^uint64(0) >> 1)
	}
	kept := words[:0]
	for _, word := range words {
		// Do not turn a malformed padded timestamp into an apparently valid
		// zero-length boundary word.
		if word.EndMS < word.StartMS {
			continue
		}
		if word.StartMS > paddedEndMS {
			continue
		}
		if word.StartMS >= endMS {
			word.StartMS = endMS
			word.EndMS = endMS
		} else if word.EndMS > endMS {
			word.EndMS = endMS
		}
		// Decoder and VAD padding never extends public timestamps, and it must
		// not extend the ceiling either: audio past the real PCM boundary is
		// synthetic and cannot justify reaching into it.
		if word.extentCap > endMS {
			word.extentCap = endMS
		}
		kept = append(kept, word)
	}
	return kept
}

func samplesToCeilMS(samples, sampleRate int) int64 {
	if samples <= 0 || sampleRate <= 0 {
		return 0
	}
	return (int64(samples)*1000 + int64(sampleRate) - 1) / int64(sampleRate)
}

// transcribeNonVADChunked decodes dense, silence-free audio (the merged-mix
// fallback case) by sliding a fixed window over the input instead of handing
// the whole buffer to a single decode. Each window is transcribed via the same
// transcribeSegment / NewOfflineStream path the VAD segments use; a short final
// window retains the established decoder-tail-pad policy. Its words already
// carry full-recording timestamps because we pass the window start as
// segOffsetMS.
// Adjacent windows overlap by nonVADWindowOverlapSamples so a word straddling a
// boundary is captured by at least one window; confirmed duplicates are aligned
// by normalized text, order, and timestamp. If two populated overlap hypotheses
// cannot be aligned at all, the merged fallback retains its legacy deterministic
// midpoint ownership instead of emitting both unstable readings.
func (r *Recognizer) transcribeNonVADChunked(samples []float32, sampleRate int) ([]Word, error) {
	windowSamples := scaleSamples(nonVADWindowSamples, sampleRate)
	overlapSamples := scaleSamples(nonVADWindowOverlapSamples, sampleRate)

	overlapMS := int64(overlapSamples) * 1000 / int64(sampleRate)

	var allWords []Word
	firstWindow := true
	for _, win := range nonVADWindowBounds(len(samples), windowSamples, overlapSamples) {
		windowStartMS := int64(win.start) * 1000 / int64(sampleRate)
		words, err := r.transcribeSegment(samples[win.start:win.end], sampleRate, windowStartMS, false)
		if err != nil {
			return nil, err
		}
		// Align the region already covered by the previous window's tail (the
		// overlap [windowStartMS, windowStartMS+overlapMS]). Confirmed duplicate
		// words are kept once despite timestamp jitter. Unlike the VAD path, two
		// populated but wholly disagreeing merged-fallback hypotheses use the old
		// midpoint cut so each seam still has one deterministic owner.
		allWords = dedupMergedFallbackWords(allWords, words, firstWindow, windowStartMS, overlapMS)
		firstWindow = false
	}
	return allWords, nil
}

// windowBound is a half-open [start,end) sample range for one decode window.
type windowBound struct {
	start int
	end   int
}

// nonVADWindowBounds returns the sliding-window sample ranges covering
// [0,total). Each window is windowSamples long (the last is shorter) and starts
// overlapSamples before the end of the previous window, i.e. the stride is
// windowSamples-overlapSamples. A non-positive or oversized window collapses to
// a single full-length window so the function never returns zero windows for a
// non-empty input and never loops forever.
func nonVADWindowBounds(total, windowSamples, overlapSamples int) []windowBound {
	if total <= 0 {
		return nil
	}
	if windowSamples <= 0 || windowSamples >= total {
		return []windowBound{{start: 0, end: total}}
	}
	stride := windowSamples - overlapSamples
	if stride <= 0 {
		stride = windowSamples
	}
	var bounds []windowBound
	for start := 0; start < total; start += stride {
		end := start + windowSamples
		if end >= total {
			bounds = append(bounds, windowBound{start: start, end: total})
			break
		}
		bounds = append(bounds, windowBound{start: start, end: end})
	}
	return bounds
}

// dedupOverlappingWords merges adjacent VAD-window hypotheses. Decoder timestamps
// can shift by hundreds of milliseconds when the same word is seen with left
// versus right context, so a hard midpoint splice can either duplicate that
// word or remove both copies. Instead, find an order-preserving alignment of
// equal normalized words near the overlap and remove only those confirmed
// duplicates. One-sided and lexically different words always survive. Of two
// matched copies, keep the one farther from its decode boundary, where the
// recognizer had more context. firstWindow has no preceding hypothesis.
func dedupOverlappingWords(acc, next []Word, firstWindow bool, windowStartMS, overlapMS int64) []Word {
	return dedupOverlappingWordsWithPolicy(acc, next, firstWindow, windowStartMS, overlapMS, false)
}

// dedupMergedFallbackWords preserves the non-VAD merged fallback's legacy
// one-owner-per-seam behavior when adjacent int8 decodes produce wholly
// different readings in a populated overlap. Where at least one confident
// text/time match exists, it retains the context-aware aligned merge.
func dedupMergedFallbackWords(acc, next []Word, firstWindow bool, windowStartMS, overlapMS int64) []Word {
	return dedupOverlappingWordsWithPolicy(acc, next, firstWindow, windowStartMS, overlapMS, true)
}

func dedupOverlappingWordsWithPolicy(acc, next []Word, firstWindow bool, windowStartMS, overlapMS int64, midpointOnDisagreement bool) []Word {
	if firstWindow {
		return append(acc, next...)
	}
	if len(acc) == 0 {
		return append(acc, next...)
	}
	if len(next) == 0 {
		return acc
	}

	overlapEndMS := windowStartMS + overlapMS
	matches := alignOverlapWords(acc, next, windowStartMS, overlapEndMS, overlapDuplicateTolerance)
	matches = confidentOverlapMatches(matches, acc, next, overlapEndMS)
	if midpointOnDisagreement && len(matches) == 0 &&
		hasWordInOverlap(acc, windowStartMS, overlapEndMS) &&
		hasWordInOverlap(next, windowStartMS, overlapEndMS) {
		return mergeOverlapAtMidpoint(acc, next, windowStartMS, overlapMS)
	}
	dropAcc := make([]bool, len(acc))
	dropNext := make([]bool, len(next))
	for _, match := range matches {
		oldWord := acc[match.acc]
		newWord := next[match.next]
		oldHasDuration := oldWord.EndMS > oldWord.StartMS
		newHasDuration := newWord.EndMS > newWord.StartMS
		if oldHasDuration != newHasDuration {
			if newHasDuration {
				dropAcc[match.acc] = true
			} else {
				dropNext[match.next] = true
			}
			continue
		}
		oldContext := overlapEndMS - wordMidpointMS(oldWord)
		newContext := wordMidpointMS(newWord) - windowStartMS
		if oldContext < 0 {
			oldContext = 0
		}
		if newContext < 0 {
			newContext = 0
		}
		if newContext > oldContext {
			dropAcc[match.acc] = true
		} else {
			dropNext[match.next] = true
		}
	}

	merged := make([]Word, 0, len(acc)+len(next)-len(matches))
	for i, word := range acc {
		if !dropAcc[i] {
			merged = append(merged, word)
		}
	}
	for i, word := range next {
		if !dropNext[i] {
			merged = append(merged, word)
		}
	}
	// One-sided words from the two hypotheses can interleave in the overlap.
	// Restore chronological order while retaining stable source order for ties.
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].StartMS < merged[j].StartMS
	})
	return merged
}

func hasWordInOverlap(words []Word, overlapStartMS, overlapEndMS int64) bool {
	for _, word := range words {
		if word.EndMS < word.StartMS || normalizeOverlapWord(word.Text) == "" {
			continue
		}
		if word.EndMS == word.StartMS {
			if word.StartMS >= overlapStartMS && word.StartMS <= overlapEndMS {
				return true
			}
			continue
		}
		// Duration words must positively intersect the half-open overlap. Mere
		// contact at either boundary does not mean both decodes populated it.
		if word.EndMS > overlapStartMS && word.StartMS < overlapEndMS {
			return true
		}
	}
	return false
}

// mergeOverlapAtMidpoint is the deterministic ownership rule used by the
// non-VAD merged fallback before text/time alignment was introduced. The
// preceding decode owns starts before the overlap midpoint; the next decode
// owns starts at or after it.
func mergeOverlapAtMidpoint(acc, next []Word, windowStartMS, overlapMS int64) []Word {
	cutMS := windowStartMS + overlapMS/2
	trimmed := len(acc)
	for trimmed > 0 && acc[trimmed-1].StartMS >= cutMS {
		trimmed--
	}
	// Build a fresh result so appending the next-window half cannot overwrite
	// the caller's acc backing array. The aligned path has the same no-alias
	// property, and keeping both policies consistent makes test/retry reuse safe.
	merged := make([]Word, 0, trimmed+len(next))
	merged = append(merged, acc[:trimmed]...)
	for _, word := range next {
		if word.StartMS >= cutMS {
			merged = append(merged, word)
		}
	}
	return merged
}

type overlapWordRef struct {
	index     int
	normal    string
	timestamp int64
}

type overlapWordMatch struct {
	acc  int
	next int
}

type overlapAlignmentCell struct {
	matches  int
	distance int64
	step     byte
}

func alignOverlapWords(acc, next []Word, overlapStartMS, overlapEndMS, toleranceMS int64) []overlapWordMatch {
	left := overlapWordRefs(acc, overlapStartMS, overlapEndMS, toleranceMS)
	right := overlapWordRefs(next, overlapStartMS, overlapEndMS, toleranceMS)
	if len(left) == 0 || len(right) == 0 {
		return nil
	}

	dp := make([][]overlapAlignmentCell, len(left)+1)
	for i := range dp {
		dp[i] = make([]overlapAlignmentCell, len(right)+1)
	}
	for i := 1; i <= len(left); i++ {
		dp[i][0] = dp[i-1][0]
		dp[i][0].step = 'l'
	}
	for j := 1; j <= len(right); j++ {
		dp[0][j] = dp[0][j-1]
		dp[0][j].step = 'r'
	}
	for i := 1; i <= len(left); i++ {
		for j := 1; j <= len(right); j++ {
			best := dp[i-1][j]
			best.step = 'l'
			if betterOverlapAlignment(dp[i][j-1], best) {
				best = dp[i][j-1]
				best.step = 'r'
			}
			distance := absMSDifference(left[i-1].timestamp, right[j-1].timestamp)
			if left[i-1].normal == right[j-1].normal && distance <= toleranceMS {
				matched := dp[i-1][j-1]
				matched.matches++
				matched.distance += distance
				matched.step = 'm'
				if betterOrEqualOverlapAlignment(matched, best) {
					best = matched
				}
			}
			dp[i][j] = best
		}
	}

	matches := make([]overlapWordMatch, 0, dp[len(left)][len(right)].matches)
	for i, j := len(left), len(right); i > 0 || j > 0; {
		switch dp[i][j].step {
		case 'm':
			matches = append(matches, overlapWordMatch{acc: left[i-1].index, next: right[j-1].index})
			i--
			j--
		case 'l':
			i--
		case 'r':
			j--
		default:
			i, j = 0, 0
		}
	}
	for i, j := 0, len(matches)-1; i < j; i, j = i+1, j-1 {
		matches[i], matches[j] = matches[j], matches[i]
	}
	return matches
}

func overlapWordRefs(words []Word, overlapStartMS, overlapEndMS, toleranceMS int64) []overlapWordRef {
	refs := make([]overlapWordRef, 0, len(words))
	for i, word := range words {
		if word.EndMS < word.StartMS || word.EndMS < overlapStartMS-toleranceMS || word.StartMS > overlapEndMS+toleranceMS {
			continue
		}
		normal := normalizeOverlapWord(word.Text)
		if normal == "" {
			continue
		}
		refs = append(refs, overlapWordRef{index: i, normal: normal, timestamp: word.StartMS})
	}
	return refs
}

// confidentOverlapMatches narrows the permissive alignment so two distinct,
// rapidly repeated words are not collapsed merely because they share text.
// A lone pair must be within 200ms. The wider measured 300ms shift is accepted
// only for an adjacent multiword run with a consistent shift, or when replacing
// a zero-length token clamped at the prior decode boundary.
func confidentOverlapMatches(matches []overlapWordMatch, acc, next []Word, overlapEndMS int64) []overlapWordMatch {
	if len(matches) == 0 {
		return nil
	}
	keep := make([]bool, len(matches))
	for i, match := range matches {
		oldWord := acc[match.acc]
		newWord := next[match.next]
		if absMSDifference(oldWord.StartMS, newWord.StartMS) <= overlapSingletonTolerance ||
			(oldWord.StartMS == overlapEndMS && oldWord.EndMS == overlapEndMS) {
			keep[i] = true
		}
	}
	for i := 0; i+1 < len(matches); i++ {
		left := matches[i]
		right := matches[i+1]
		if right.acc != left.acc+1 || right.next != left.next+1 {
			continue
		}
		leftShift := next[left.next].StartMS - acc[left.acc].StartMS
		rightShift := next[right.next].StartMS - acc[right.acc].StartMS
		if absMSDifference(leftShift, rightShift) <= overlapShiftConsistency {
			keep[i] = true
			keep[i+1] = true
		}
	}
	confident := make([]overlapWordMatch, 0, len(matches))
	for i, match := range matches {
		if keep[i] {
			confident = append(confident, match)
		}
	}
	return confident
}

func normalizeOverlapWord(text string) string {
	normalized := strings.Map(func(r rune) rune {
		if r == '\u2018' || r == '\u2019' {
			return '\''
		}
		return unicode.ToLower(r)
	}, text)
	normalized = strings.TrimFunc(normalized, func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune(",!?;:\"“”()[]{}<>", r)
	})
	for _, r := range normalized {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			return normalized
		}
	}
	return ""
}

func wordMidpointMS(word Word) int64 {
	if word.EndMS <= word.StartMS {
		return word.StartMS
	}
	return word.StartMS + (word.EndMS-word.StartMS)/2
}

func betterOverlapAlignment(candidate, current overlapAlignmentCell) bool {
	return candidate.matches > current.matches ||
		(candidate.matches == current.matches && candidate.distance < current.distance)
}

func betterOrEqualOverlapAlignment(candidate, current overlapAlignmentCell) bool {
	return candidate.matches > current.matches ||
		(candidate.matches == current.matches && candidate.distance <= current.distance)
}

func absMSDifference(a, b int64) int64 {
	if a >= b {
		return a - b
	}
	return b - a
}

// scaleSamples rescales a sample count defined at 16 kHz to the given sample
// rate so the window/overlap durations stay constant if a model ever reports a
// different rate. At the canonical 16 kHz it is the identity.
func scaleSamples(samplesAt16k, sampleRate int) int {
	if sampleRate == 16000 || sampleRate <= 0 {
		return samplesAt16k
	}
	return samplesAt16k * sampleRate / 16000
}

// Close frees the underlying recognizer and VAD.
func (r *Recognizer) Close() {
	sherpa.DeleteOfflineRecognizer(r.r)
	sherpa.DeleteVoiceActivityDetector(r.vad)
}

// tokensToWords converts BPE/char tokens with per-token timestamps into
// word-level timings. Handles both BPE (▁ word-start marker) and
// character-level CTC (space as delimiter).
func tokensToWords(tokens []string, timestamps, durations []float32) []Word {
	if len(tokens) == 0 {
		return nil
	}

	var words []Word
	var curText strings.Builder
	var wordStartMs float64 = -1
	var lastSpeechEndMs float64 = -1
	var lastTokenEndMs float64 = -1
	hasSpeechToken := false

	flush := func() {
		text := strings.TrimSpace(curText.String())
		if text != "" {
			endMs := lastSpeechEndMs
			if endMs < wordStartMs {
				// Either the word carries no acoustic token at all (a standalone
				// punctuation mark) or every one of them was stamped before the
				// word start. Collapse to a zero-length word rather than
				// inventing an extent.
				endMs = wordStartMs
			}
			// The punctuation-inclusive end travels with the word as a ceiling,
			// not as the end. Parakeet's duration head is quantised to at most
			// 320ms and cannot describe a longer word, so the last speech token
			// often stops short of the real acoustic end; the energy gate walks
			// the owner's own audio forward from there, and this is how far it
			// may go — exactly the end the old punctuation-inclusive rule
			// produced, and never further.
			//
			// Only a word that carries a speech-bearing token gets that
			// ceiling. A word built entirely from punctuation has no audio of
			// its own for the gate to follow: everything after it belongs to
			// whatever speaks next, and the mark's own timestamp IS that next
			// onset, so a ceiling there is an invitation to walk the word
			// across a neighbour's speech. Such a word keeps exactly the
			// zero-length extent its tokens stamped.
			capMs := endMs
			if hasSpeechToken {
				capMs = maxFloat64(lastTokenEndMs, endMs)
			}
			words = append(words, Word{
				Text:      text,
				StartMS:   int64(wordStartMs),
				EndMS:     int64(endMs),
				extentCap: int64(capMs),
			})
		}
		curText.Reset()
		wordStartMs = -1
		// The end is per word: a word must never inherit the previous word's
		// end, or a token stamped before it (a zero-duration boundary token,
		// or a word re-decoded from an overlapping window) silently absorbs
		// the whole preceding span. The cap and the speech-bearing flag are
		// per word for the same reason.
		lastSpeechEndMs = -1
		lastTokenEndMs = -1
		hasSpeechToken = false
	}

	for i, tok := range tokens {
		var ts, dur float64
		if i < len(timestamps) {
			ts = float64(timestamps[i]) * 1000
		}
		if i < len(durations) {
			dur = float64(durations[i]) * 1000
		}
		end := ts + dur

		// Sherpa marks word starts in a few different ways depending on the
		// model: SentencePiece-style ▁ prefixes, standalone space tokens, and
		// literal leading spaces like " So". Missing the last form collapses an
		// entire phrase into one long token, which destroys the original pause
		// structure and leads to visibly wrong seek points in the UI.
		if strings.HasPrefix(tok, "▁") || strings.HasPrefix(tok, " ") || tok == "<space>" {
			flush()
			tok = strings.TrimPrefix(tok, "▁")
			tok = strings.TrimLeft(tok, " ")
			if tok == "" {
				continue
			}
		}
		if wordStartMs < 0 {
			wordStartMs = ts
		}
		curText.WriteString(tok)
		lastTokenEndMs = maxFloat64(lastTokenEndMs, ts, end)
		// The mark itself stays in the word's text; it just may not decide how
		// far the word reaches. Parakeet stamps a sentence-final mark at the
		// *next* acoustic onset, so honouring its timestamp stretches the word
		// it is attached to across the pause that follows the sentence — which
		// the viewer then paints as an overlap with whoever spoke during that
		// pause. It survives only as a ceiling on the audio-driven end.
		if !tokenIsNonAcousticPunctuation(tok) {
			// Some models emit zero-duration boundary tokens. Keep the word end
			// at least at the token's own timestamp so a word is never flushed
			// with an end that precedes its last spoken token.
			hasSpeechToken = true
			lastSpeechEndMs = maxFloat64(lastSpeechEndMs, ts, end)
		}
	}
	flush()
	return splitMultiWordTokens(words)
}

// tokenIsNonAcousticPunctuation reports whether a decoder token is nothing but
// delimiter punctuation. Parakeet's punctuation head writes these marks rather
// than hearing them, and stamps a sentence-final one at the *next* acoustic
// onset, so such a token's timestamp is evidence about the following word, not
// about the extent of the one it is attached to.
//
// The set is deliberately narrow: only marks that are never spoken. Symbols
// like % and $ stand for spoken words ("percent", "dollars") and carry real
// audio, so they are not listed and keep extending the word end. A token that
// mixes text and punctuation ("irit.") is not punctuation-only and also keeps
// extending it.
func tokenIsNonAcousticPunctuation(token string) bool {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		switch r {
		case '.', ',', '?', '!', ';', ':',
			'…', '‥', '¿', '¡',
			'。', '．', '｡', '，', '、', '､', '？', '！', '；', '：',
			'؟', '،', '؛', '।', '॥':
		default:
			return false
		}
	}
	return true
}

func maxFloat64(values ...float64) float64 {
	max := values[0]
	for _, value := range values[1:] {
		if value > max {
			max = value
		}
	}
	return max
}

func splitMultiWordTokens(words []Word) []Word {
	out := make([]Word, 0, len(words))
	for _, word := range words {
		parts := strings.Fields(word.Text)
		if len(parts) <= 1 {
			out = append(out, word)
			continue
		}

		// Some sherpa models now emit phrase-sized "tokens" with one shared time
		// span. Split those into real word entries here so downstream display
		// alignment and seek can anchor cleaned text to individual source words.
		span := word.EndMS - word.StartMS
		if span <= 0 {
			span = int64(len(parts))
		}
		for index, part := range parts {
			start := word.StartMS + (span*int64(index))/int64(len(parts))
			end := word.StartMS + (span*int64(index+1))/int64(len(parts))
			if end < start {
				end = start
			}
			split := Word{
				Text:    part,
				StartMS: start,
				EndMS:   end,
			}
			if index == len(parts)-1 {
				// Only the phrase's last part may reach the phrase's ceiling.
				// The interior parts end where the next one starts, and letting
				// them grow would overlap their own neighbours.
				split.extentCap = word.extentCap
			}
			out = append(out, split)
		}
	}
	return out
}
