package transcribe

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// BuildConfig holds runtime options for the transcription pipeline.
type BuildConfig struct {
	Device                string     // "cpu" or "cuda"
	ModelID               ModelID    // defaults to defaultModelID
	AdditionalModels      []ModelID  // run extra transcription passes; each becomes a sibling transcript file referenced from manifest.files.transcripts
	CacheDir              string     // root cache directory, e.g. ~/.cache/cassini
	LLM                   LLMConfig  // optional; if not configured, skip readable cleanup
	SummaryLLM            LLMConfig  // optional; if not configured, skip summary generation
	StrictReadableCleanup bool       // fail the build if readable cleanup cannot complete
	NumThreads            int        // 0 = derive from device (CUDA=1; CPU=core count, capped)
	Quality               STTQuality // "" = balanced; picks model/device when not explicitly set
	TranscriptionTerms    []string   // optional preferred spellings for LLM readable cleanup; does not affect raw ASR
	// Backend selects the speech decoder ("" = CASSINI_STT_BACKEND, else the
	// bundled sherpa-onnx). See backend.go.
	Backend string
	// SkipAttribution disables the cross-track attribution stage entirely.
	SkipAttribution bool
	// DropCrosstalk removes flagged words instead of only marking them. Off by
	// default: the transcript stays canonical and the evidence travels with it.
	DropCrosstalk bool
	// SourceAudioRoom is the Talk room token this recording belongs to. Source
	// captures are selected by room AND overlapping call window; without it the
	// selection falls back to the window alone, which is weaker.
	SourceAudioRoom string
	// SourceAudioDir is the root of participant-uploaded source captures
	// (the operator's capture root). When set, a speaker whose upload can be
	// placed on the meeting timeline is transcribed from that audio instead of
	// from the track the SFU delivered. Empty disables ingestion entirely.
	SourceAudioDir string
}

var (
	readableCleanupFn     = ReadableCleanup
	buildMeetingSummaryFn = BuildMeetingSummary
	// ensureModelFn / ensureVADFn / buildSpeakerEnvelopesFn are seams so the
	// pipeline can be exercised end-to-end with a registered fake backend and
	// no multi-hundred-MB model download.
	ensureModelFn           = EnsureModel
	ensureVADFn             = EnsureVAD
	buildSpeakerEnvelopesFn = BuildSpeakerEnvelopes
)

// BuildMeetingArtifact transcribes an MKV recording and writes all meeting
// bundle artifacts to outputDir:
//   - meeting.webm          — mono 48 kHz Opus mix of all speakers
//   - transcript.words.v1.json
//   - transcript.readable.v1.json + captions.vtt  (if LLM configured)
//   - summary.md            — V0 template format (if SummaryLLM configured)
//   - manifest.json
func BuildMeetingArtifact(ctx context.Context, mkvPath, outputDir string, cfg BuildConfig, stdout io.Writer) error {
	// Resolve the STT execution policy for this host: an explicit device/model
	// always wins; otherwise derive both from the quality tier and detected
	// hardware (a GPU box runs fp32, a CPU box int8). CUDA uses one host thread
	// by default so GPU inference does not create unnecessary CPU/RAM pressure.
	cfg.Device = ResolveDevice(cfg.Device)
	if cfg.ModelID == "" {
		cfg.ModelID = ModelForQuality(cfg.Quality, cfg.Device)
	}
	if cfg.NumThreads < 1 {
		cfg.NumThreads = DefaultNumThreadsForDevice(cfg.Device)
	}
	// Resolve AND validate the backend before any real work. The registry is
	// the authority on what exists; an unknown id must fail here, not after
	// the full mixdown, audio hash and model download have already run — the
	// misconfiguration lives in the environment, so a late failure repeats
	// all of that work on every operator retry.
	backend, err := LookupRecognizerBackend(cfg.Backend)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "  STT policy: backend=%s device=%s model=%s threads=%d quality=%s\n",
		backend,
		cfg.Device, cfg.ModelID, cfg.NumThreads, NormalizeQuality(string(cfg.Quality)))
	if cfg.CacheDir == "" {
		cfg.CacheDir = defaultCacheDir()
	}

	// --- 1. Probe MKV ---
	fmt.Fprintln(stdout, "  probing audio streams...")
	streams, srcDurationMS, err := ProbeMKV(mkvPath)
	if err != nil {
		return fmt.Errorf("probe MKV: %w", err)
	}
	fmt.Fprintf(stdout, "  found %d audio stream(s), duration %d ms\n", len(streams), srcDurationMS)

	// --- 2. Mix down to meeting.webm, splicing in participant captures ---
	//
	// One render, two consumers. Each participant's recorded track is decoded
	// onto the meeting timeline first; a participant who uploaded their own
	// microphone recording gets that laid over their track wherever it has
	// audio; and the result is both what the encoder mixes and — resampled to
	// 16 kHz — what the recogniser hears. So every word in the transcript can
	// be heard in the published audio, at the same instant, because the two
	// come from the same samples rather than from two placements that agree
	// approximately.
	//
	// The published audio used to stay the recorded mix, on the argument that
	// playback should be the meeting as the room heard it. That argument lost:
	// a transcript quoting words that are inaudible in the recording is worse
	// than a mix that is a little cleaner than the call was.
	webmPath := filepath.Join(outputDir, "meeting.webm")
	fmt.Fprintln(stdout, "  mixing audio to meeting.webm...")
	mix, err := PrepareMix(mkvPath, streams)
	if err != nil {
		return fmt.Errorf("mix audio: %w", err)
	}
	defer mix.Close()

	var sourceAudio []SourceRenderReport
	if cfg.SourceAudioDir != "" {
		// ApplySourceAudio creates its own work directory, and only once it has
		// a capture to render. Ingestion is on by default, so the ordinary case
		// is a build with no upload for this room, and that build has to leave
		// the bundle byte for byte what a build without ingestion would leave —
		// not an empty _work/sourceaudio to explain to whoever finds it.
		sourceAudio = ApplySourceAudio(ctx, mix, streams, cfg.SourceAudioDir, cfg.SourceAudioRoom, outputDir, stdout)
	}
	if err := mix.Encode(webmPath); err != nil {
		if !mix.Substituted() {
			return fmt.Errorf("mix audio: %w", err)
		}
		// The encoder refused the spliced inputs. Fall back to the build this
		// meeting would have had without ingestion at all rather than lose it
		// to an improvement in its playback — and fall back on BOTH sides. A
		// recorded mix under a spliced transcript is the very thing this build
		// order exists to prevent, and it would also leave a rejoined
		// participant's other streams dropped from transcription while their
		// audio is back in the mix.
		fmt.Fprintf(stdout, "  source audio: the spliced mix would not encode (%v); publishing the recorded mix and transcribing the recorded tracks\n", err)
		revertSourceAudio(streams, sourceAudio, "the spliced mix would not encode: "+err.Error())
		if revertErr := mix.RevertSubstitutions(); revertErr != nil {
			return fmt.Errorf("mix audio: %w (after %v)", revertErr, err)
		}
		if err := mix.Encode(webmPath); err != nil {
			return fmt.Errorf("mix audio: %w", err)
		}
	}
	// The decoded tracks and renders are large — a two-hour meeting is hundreds
	// of megabytes per speaker — and nothing after this point reads them. Free
	// them before the model download and the transcription pass rather than at
	// the end of the build.
	mix.Close()

	// Compute SHA-256 of the decoded PCM for integrity tracking. After the
	// splice, not before it: this hash travels in every transcript as
	// media.sha256, and it has to describe the meeting.webm that was actually
	// published rather than an intermediate nobody kept.
	sha256hex, _, err := PCMsha256FromWebM(webmPath)
	if err != nil {
		return fmt.Errorf("compute audio hash: %w", err)
	}

	audioDurationMS, err := AudioDurationMS(webmPath)
	if err != nil {
		return fmt.Errorf("get audio duration: %w", err)
	}
	// Source container duration remains provenance, but malformed container
	// metadata can vastly overstate it. The measured playable mix is a safer
	// allocation hint for per-speaker PCM; packet PTS still controls timing.
	setPCMCapacityDurationHints(streams, audioDurationMS)

	// --- 3. Download / verify STT model and VAD ---
	fmt.Fprintf(stdout, "  ensuring model %s is cached...\n", cfg.ModelID)
	modelPaths, err := ensureModelFn(cfg.CacheDir, cfg.ModelID, stdout)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}

	vadPath, err := ensureVADFn(cfg.CacheDir, stdout)
	if err != nil {
		return fmt.Errorf("ensure VAD model: %w", err)
	}

	// --- 4. Transcribe with the primary model, then any additional models.
	// The primary model writes the default transcript.words.v1.json. Each
	// additional model writes a sibling transcript file and is recorded under
	// manifest.files.transcripts for indexed portable output.
	// Whether the manifest may claim provenance.wordTimings.endsBoundedByAudio
	// is decided by the recognizers, not by this function: every pass below
	// records what its decoder declared, and the claim survives only if all of
	// them made it. See wordEndGuarantee in backend.go.
	wordEnds := &wordEndGuarantee{}
	segments, err := transcribePass(ctx, mkvPath, streams, modelPaths, vadPath, backend, cfg.Device, cfg.NumThreads, stdout, wordEnds)
	if err != nil {
		return err
	}

	// Per-participant Talk recordings can carry sparse / DTX-encoded audio
	// that defeats the VAD even when the mixed timeline clearly contains
	// speech. Fall back to transcribing the already-mixed meeting.webm under
	// a synthetic "merged" speaker so the bundle still ships usable content.
	streams, segments, err = ensureMergedFallback(ctx, webmPath, streams, segments, modelPaths, vadPath, backend, cfg.Device, cfg.NumThreads, stdout, wordEnds)
	if err != nil {
		return err
	}

	// --- Cross-track speaker attribution ---
	// Measure every word against the per-track energy on the shared timeline and
	// record the result as provenance. This is what makes crosstalk visible: a
	// word the decoder produced on a participant's track while somebody else was
	// decisively louder is a bleed candidate, not that participant speaking.
	// Annotate-only by default; see attribution.go for why dropping is opt-in.
	// The envelopes depend only on the audio, not on the STT model, so one
	// cache serves the primary transcript and every additional model: each
	// track is decoded for attribution once per build, not once per pass.
	envCache := &speakerEnvelopeCache{}
	attrProv := &AttributionProvenance{Mode: attributionModeForConfig(cfg)}
	if cfg.SkipAttribution {
		// Even a disabled stage leaves a trace: otherwise a build that never
		// measured anything is indistinguishable from one that ran clean.
		attrProv.Reason = "disabled by configuration (CASSINI_ATTRIBUTION_DISABLED)"
	} else {
		segments = applyAttributionReported(mkvPath, streams, segments, modelPaths.SampleRate, cfg, envCache, stdout, attrProv)
	}

	if err := writeTranscriptWithHash(filepath.Join(outputDir, "transcript.words.v1.json"), "transcript.words.v1", streams, segments, audioDurationMS, sha256hex); err != nil {
		return fmt.Errorf("write transcript: %w", err)
	}

	additionalTranscripts, err := runAdditionalTranscripts(ctx, mkvPath, outputDir, streams, audioDurationMS, sha256hex, backend, cfg, envCache, stdout, wordEnds)
	if err != nil {
		return err
	}

	// --- 8. Optional: LLM readable cleanup ---
	cleanedSegs, hasReadable, err := writeReadableArtifacts(outputDir, streams, segments, audioDurationMS, sha256hex, cfg, stdout)
	if err != nil {
		return err
	}

	// --- 9. Optional: meeting summary generation ---
	summaryInput := segments
	if hasReadable {
		summaryInput = cleanedSegs
	}
	// The canonical transcript keeps every word; the summary does not. A reader
	// can see and overrule a word marked as probable crosstalk, but a generated
	// summary has no such reader, and a fabricated interjection there becomes a
	// decision somebody supposedly made. This is a no-op unless the meeting's own
	// distribution showed an unambiguous crosstalk population.
	if filtered, removed := WithoutLowConfidenceWords(summaryInput); removed > 0 {
		fmt.Fprintf(stdout, "  excluding %d low-confidence words from the summary input\n", removed)
		summaryInput = filtered
	}
	hasSummary, err := writeSummaryArtifact(outputDir, streams, summaryInput, cfg, stdout)
	if err != nil {
		return err
	}

	// --- 10. Write manifest ---
	manifestPath := filepath.Join(outputDir, "manifest.json")
	srcBasename := filepath.Base(mkvPath)
	if err := WriteManifest(manifestPath, srcBasename, srcDurationMS, audioDurationMS, streams, segments, backend, cfg.ModelID, cfg.Device, cfg.LLM.Model, hasReadable, cfg.SummaryLLM.Model, hasSummary, additionalTranscripts, attrProv, wordEnds.provenance(), sourceAudio); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Fprintf(stdout, "  done: %d segments, %d words\n", len(segments), CountWords(segments))
	return nil
}

// AdditionalTranscript describes one extra transcript file emitted by a
// secondary STT model. The producer turns these into manifest.files.transcripts
// entries so the portable-meeting packer can place them in separate
// CASSINI_TX_<ID>_PAYLOAD_* tag sets.
type AdditionalTranscript struct {
	ID      string  // sanitised model id, suitable for tag namespace (a-z 0-9 -, max 32 chars)
	Path    string  // relative to outputDir, e.g. transcript-parakeet-tdt-06b-v2-int8.words.v1.json
	ModelID ModelID // raw STT model id, kept for provenance
	Backend string  // resolved STT backend id that produced this transcript
}

// ensureMergedFallback transcribes the already-mixed meeting.webm under a
// synthetic "merged" speaker when the per-participant pass is too thin.
// Talk-recorder per-participant tracks can be sparse (DTX, comfort-noise
// frames between speaking turns) in ways that defeat the VAD even though
// the same audio sums to a clearly-transcribable mix; rather than ship an
// empty or nearly empty transcript in that case, fall back to the mix. Returns the
// (possibly extended) streams + segments. Does nothing when the
// per-participant pass meets the minimum word threshold.
// minWordsBeforeMergedFallback is the per-participant word threshold below
// which we re-run transcription over the merged mix. The strict ==0 gate
// was observed to skip fallback when a single stray word survived through
// an otherwise-failing pass (CI matrix run #5: 1 word total, fallback
// skipped, e2e word-run gate failed). 10 is conservative: well under the
// passing-run baseline of ~12-14 verbatim words, well over the noise.
const minWordsBeforeMergedFallback = 10

// The merged pass has no participant attribution, so it must recover more
// than a token or two before replacing words that still identify a speaker.
// Require at least two additional words and at least a 20% coverage gain. The
// zero-word failure mode remains special: any non-empty mix is an improvement.
const (
	mergedFallbackMinExtraWords          = 2
	mergedFallbackMinRelativeGainPercent = 20
)

// shouldFireMergedFallback decides whether the merged-mix fallback pass
// is worth running. Extracted so the trigger threshold has a dedicated
// regression test without needing a sherpa-onnx model + audio fixture.
func shouldFireMergedFallback(segments []Segment) bool {
	return CountWords(segments) < minWordsBeforeMergedFallback
}

func ensureMergedFallback(ctx context.Context, webmPath string, streams []AudioStream, segments []Segment, modelPaths ModelPaths, vadPath, backend, device string, numThreads int, stdout io.Writer, guarantee *wordEndGuarantee) ([]AudioStream, []Segment, error) {
	if !shouldFireMergedFallback(segments) {
		return streams, segments, nil
	}
	fmt.Fprintf(stdout, "  per-participant transcription thin (%d words); transcribing mixed track as fallback...\n", CountWords(segments))

	mergedStream := AudioStream{
		Index:        -1,
		SpeakerID:    "merged",
		SpeakerLabel: "Everyone",
		Channels:     1,
		StartTimeMS:  0,
	}

	rec, err := newRecognizerForPass(backend, modelPaths, vadPath, device, numThreads, guarantee)
	if err != nil {
		return streams, segments, fmt.Errorf("create recognizer for merged fallback: %w", err)
	}
	defer rec.Close()

	select {
	case <-ctx.Done():
		return streams, segments, ctx.Err()
	default:
	}

	samples, err := ExtractMixedFloats(webmPath)
	if err != nil {
		return streams, segments, fmt.Errorf("extract mixed audio: %w", err)
	}
	// useVAD=false: the merged mix is dense by construction (the rotator
	// keeps the mix continuously audible). Silero with default threshold
	// rejects this audio ~17-33% of runs in CI; the entire purpose of the
	// fallback is to recover when VAD-gated transcription fails, so it
	// must not itself depend on VAD. Transcribe(useVAD=false) decodes this
	// ~75s mix in short overlapping windows (see transcribeNonVADChunked) so
	// no single low-confidence int8 span can zero the whole transcript.
	words, err := rec.Transcribe(samples, modelPaths.SampleRate, false /*useVAD*/)
	if err != nil {
		return streams, segments, fmt.Errorf("transcribe merged fallback: %w", err)
	}
	fmt.Fprintf(stdout, "    merged: %d words\n", len(words))
	if len(words) == 0 {
		return streams, segments, nil
	}

	mergedSegs := AssembleSegments(mergedStream.SpeakerID, words, 0, 0)
	chosenSegments, useMerged := chooseMergedFallback(segments, mergedSegs)
	if !useMerged {
		fmt.Fprintf(stdout, "    merged fallback did not clear attribution-preserving margin (need at least %d words vs %d attributed); keeping participant pass\n",
			minimumMergedFallbackWords(CountWords(segments)), CountWords(segments))
		return streams, chosenSegments, nil
	}

	// The mixed pass covers the same meeting timeline as the participant pass.
	// Treat these as alternative hypotheses, never additive sources: appending
	// both duplicated every word that survived the thin participant pass. Only
	// a mixed hypothesis that clears the attribution-preserving margin reaches
	// the transcript.
	extendedStreams := append(append([]AudioStream(nil), streams...), mergedStream)
	return extendedStreams, chosenSegments, nil
}

// chooseMergedFallback keeps the two transcription hypotheses mutually
// exclusive. Attribution is more valuable than a marginal coverage increase,
// so the synthetic mixed pass replaces the participant pass only when it clears
// the documented absolute and relative recovery margin above.
func chooseMergedFallback(participantSegments, mergedSegments []Segment) ([]Segment, bool) {
	if CountWords(mergedSegments) < minimumMergedFallbackWords(CountWords(participantSegments)) {
		return participantSegments, false
	}
	return MergeAndSortSegments([][]Segment{mergedSegments}), true
}

func minimumMergedFallbackWords(participantWords int) int {
	if participantWords == 0 {
		return 1
	}
	relativeGain := (participantWords*mergedFallbackMinRelativeGainPercent + 99) / 100
	if relativeGain < mergedFallbackMinExtraWords {
		relativeGain = mergedFallbackMinExtraWords
	}
	return participantWords + relativeGain
}

// transcribePass runs one full transcription pass over every speaker stream
// using the given recognizer config. Returns merged + sorted segments.
func transcribePass(ctx context.Context, mkvPath string, streams []AudioStream, modelPaths ModelPaths, vadPath, backend, device string, numThreads int, stdout io.Writer, guarantee *wordEndGuarantee) ([]Segment, error) {
	// Streams a participant's source capture already covers are dropped here
	// rather than inside each transcription path, so sequential and parallel
	// cannot disagree about what was transcribed.
	streams = transcribableStreams(streams)
	conc := resolveStreamConcurrency(len(streams), numThreads, device)
	if conc <= 1 {
		return transcribeStreamsSequential(ctx, mkvPath, streams, modelPaths, vadPath, backend, device, numThreads, stdout, guarantee)
	}
	return transcribeStreamsParallel(ctx, mkvPath, streams, modelPaths, vadPath, backend, device, numThreads, conc, stdout, guarantee)
}

// transcribableStreams filters out streams suppressed by source-audio
// ingestion. Returns everything when nothing is suppressed, which is every
// build that has no uploads.
func transcribableStreams(streams []AudioStream) []AudioStream {
	keep := streams[:0:0]
	for _, stream := range streams {
		if !stream.SuppressTranscription {
			keep = append(keep, stream)
		}
	}
	return keep
}

// transcribeStreamsSequential transcribes each speaker stream one at a time with
// a single shared recognizer. Used when concurrency resolves to 1 (single
// speaker, tight thread budget, or low free RAM).
func transcribeStreamsSequential(ctx context.Context, mkvPath string, streams []AudioStream, modelPaths ModelPaths, vadPath, backend, device string, numThreads int, stdout io.Writer, guarantee *wordEndGuarantee) ([]Segment, error) {
	fmt.Fprintf(stdout, "  loading recognizer (device=%s)...\n", device)
	rec, err := newRecognizerForPass(backend, modelPaths, vadPath, device, numThreads, guarantee)
	if err != nil {
		return nil, fmt.Errorf("create recognizer: %w", err)
	}
	defer rec.Close()

	perSpeakerSegs := make([][]Segment, len(streams))
	for i, stream := range streams {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		fmt.Fprintf(stdout, "  transcribing %s (stream index %d)...\n", stream.SpeakerLabel, stream.Index)
		samples, err := ExtractSpeakerFloats(mkvPath, stream)
		if err != nil {
			return nil, fmt.Errorf("extract audio for %s: %w", stream.SpeakerLabel, err)
		}
		words, err := rec.Transcribe(samples, modelPaths.SampleRate, true /*useVAD*/)
		if err != nil {
			return nil, fmt.Errorf("transcribe %s: %w", stream.SpeakerLabel, err)
		}
		fmt.Fprintf(stdout, "    %s: %d words\n", stream.SpeakerLabel, len(words))
		perSpeakerSegs[i] = AssembleSegments(stream.SpeakerID, words, 0, 0)
	}
	return MergeAndSortSegments(perSpeakerSegs), nil
}

// transcribeStreamsParallel transcribes speaker streams concurrently across
// `conc` workers, each owning its OWN recognizer (no shared sherpa state, so no
// thread-safety question) with an even share of the intra-op thread budget.
// Concurrency is already bounded by core count and free RAM (see
// resolveStreamConcurrency), so this never oversubscribes or OOMs the host.
// Per-speaker results are written by index, so the merged output is independent
// of completion order. The first error cancels the rest.
func transcribeStreamsParallel(ctx context.Context, mkvPath string, streams []AudioStream, modelPaths ModelPaths, vadPath, backend, device string, numThreads, conc int, stdout io.Writer, guarantee *wordEndGuarantee) ([]Segment, error) {
	threadsPer := numThreads / conc
	if threadsPer < 1 {
		threadsPer = 1
	}
	fmt.Fprintf(stdout, "  transcribing %d streams, concurrency=%d (%d threads each, device=%s)...\n", len(streams), conc, threadsPer, device)

	perSpeakerSegs := make([][]Segment, len(streams))
	type streamJob struct {
		idx    int
		stream AudioStream
	}
	jobs := make(chan streamJob)

	gctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
		cancel()
	}
	logLine := func(format string, a ...any) {
		mu.Lock()
		fmt.Fprintf(stdout, format, a...)
		mu.Unlock()
	}

	worker := func() {
		defer wg.Done()
		rec, err := newRecognizerForPass(backend, modelPaths, vadPath, device, threadsPer, guarantee)
		if err != nil {
			fail(fmt.Errorf("create recognizer: %w", err))
			return
		}
		defer rec.Close()
		for j := range jobs {
			if gctx.Err() != nil {
				return
			}
			logLine("  transcribing %s (stream index %d)...\n", j.stream.SpeakerLabel, j.stream.Index)
			samples, err := ExtractSpeakerFloats(mkvPath, j.stream)
			if err != nil {
				fail(fmt.Errorf("extract audio for %s: %w", j.stream.SpeakerLabel, err))
				return
			}
			words, err := rec.Transcribe(samples, modelPaths.SampleRate, true /*useVAD*/)
			if err != nil {
				fail(fmt.Errorf("transcribe %s: %w", j.stream.SpeakerLabel, err))
				return
			}
			logLine("    %s: %d words\n", j.stream.SpeakerLabel, len(words))
			perSpeakerSegs[j.idx] = AssembleSegments(j.stream.SpeakerID, words, 0, 0)
		}
	}

	wg.Add(conc)
	for i := 0; i < conc; i++ {
		go worker()
	}

feed:
	for i, s := range streams {
		select {
		case <-gctx.Done():
			break feed
		case jobs <- streamJob{idx: i, stream: s}:
		}
	}
	close(jobs)
	wg.Wait()

	if firstErr != nil {
		return nil, firstErr
	}
	return MergeAndSortSegments(perSpeakerSegs), nil
}

// runAdditionalTranscripts re-transcribes the same audio with each model in
// cfg.AdditionalModels and writes a sibling transcript file per model.
// backend is the already-validated recognizer id from BuildMeetingArtifact and
// envCache carries the per-track attribution envelopes so extra models reuse
// the primary pass's decode instead of re-running ffmpeg over every track.
func runAdditionalTranscripts(ctx context.Context, mkvPath, outputDir string, streams []AudioStream, audioDurationMS int64, sha256hex, backend string, cfg BuildConfig, envCache *speakerEnvelopeCache, stdout io.Writer, guarantee *wordEndGuarantee) ([]AdditionalTranscript, error) {
	if len(cfg.AdditionalModels) == 0 {
		return nil, nil
	}
	vadPath, err := ensureVADFn(cfg.CacheDir, stdout)
	if err != nil {
		return nil, fmt.Errorf("ensure VAD model: %w", err)
	}
	out := make([]AdditionalTranscript, 0, len(cfg.AdditionalModels))
	seen := map[string]bool{sanitizeTranscriptID(string(cfg.ModelID)): true}
	for _, modelID := range cfg.AdditionalModels {
		if modelID == "" || modelID == cfg.ModelID {
			continue
		}
		id := sanitizeTranscriptID(string(modelID))
		if seen[id] {
			continue
		}
		seen[id] = true

		fmt.Fprintf(stdout, "  ensuring additional model %s is cached...\n", modelID)
		modelPaths, err := ensureModelFn(cfg.CacheDir, modelID, stdout)
		if err != nil {
			return nil, fmt.Errorf("ensure additional model %s: %w", modelID, err)
		}
		segs, err := transcribePass(ctx, mkvPath, streams, modelPaths, vadPath, backend, cfg.Device, cfg.NumThreads, stdout, guarantee)
		if err != nil {
			return nil, fmt.Errorf("additional transcribe %s: %w", modelID, err)
		}
		// Every transcript this build emits carries the same attribution
		// contract, or switching models would silently change whether a word
		// has provenance.
		if !cfg.SkipAttribution {
			segs = applyAttributionCached(mkvPath, streams, segs, modelPaths.SampleRate, cfg, envCache, stdout)
		}
		path := fmt.Sprintf("transcript-%s.words.v1.json", id)
		if err := writeTranscriptWithHash(filepath.Join(outputDir, path), "transcript.words.v1", streams, segs, audioDurationMS, sha256hex); err != nil {
			return nil, fmt.Errorf("write additional transcript %s: %w", id, err)
		}
		out = append(out, AdditionalTranscript{ID: id, Path: path, ModelID: modelID, Backend: backend})
	}
	return out, nil
}

// sanitizeTranscriptID maps a model id to the published transcript-id regex
// ^[a-z0-9][a-z0-9-]{0,31}$. Dots and other unsupported runes become hyphens
// and the result is truncated to 32 runes.
func sanitizeTranscriptID(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.TrimRight(out[:32], "-")
	}
	return out
}

// DefaultBuildConfig returns a BuildConfig populated from standard environment
// variables. The caller should override Device and CacheDir as needed.
func DefaultBuildConfig() BuildConfig {
	llm := DefaultLLMConfig()
	llm.APIKey = os.Getenv("OPENROUTER_API_KEY")
	llm.BaseURL = os.Getenv("OPENROUTER_BASE_URL")
	if llm.BaseURL == "" {
		llm.BaseURL = os.Getenv("LLM_BASE_URL")
	}
	if llm.BaseURL == "" && llm.APIKey != "" {
		llm.BaseURL = "https://openrouter.ai/api/v1"
	}
	if model := os.Getenv("LLM_MODEL"); model != "" {
		llm.Model = model
	}

	summaryLLM := llm
	if model := os.Getenv("SUMMARY_MODEL"); model != "" {
		summaryLLM.Model = model
	}
	if envBool("CASSINI_SUMMARY_DISABLED") {
		// Disable summary independently of readable cleanup. IsConfigured()
		// requires both APIKey and BaseURL, so blanking the key is sufficient.
		summaryLLM.APIKey = ""
	}

	// Leave an unset model empty: BuildMeetingArtifact derives it from the
	// quality tier and the resolved device (GPU -> fp32, CPU -> int8). An
	// explicit CASSINI_STT_MODEL still wins.
	primary := ModelID(strings.TrimSpace(os.Getenv("CASSINI_STT_MODEL")))

	var additional []ModelID
	if raw := strings.TrimSpace(os.Getenv("CASSINI_STT_ADDITIONAL_MODELS")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			additional = append(additional, ModelID(part))
		}
	}

	return BuildConfig{
		Device:                defaultDevice(),
		ModelID:               primary,
		AdditionalModels:      additional,
		LLM:                   llm,
		SummaryLLM:            summaryLLM,
		StrictReadableCleanup: envBool("CASSINI_READABLE_STRICT_BATCHES"),
		NumThreads:            envInt("CASSINI_STT_NUM_THREADS"),
		Quality:               NormalizeQuality(os.Getenv("CASSINI_STT_QUALITY")),
		Backend:               ResolveRecognizerBackend(""),
		SkipAttribution:       envBool("CASSINI_ATTRIBUTION_DISABLED"),
		DropCrosstalk:         envBool("CASSINI_ATTRIBUTION_DROP"),
		TranscriptionTerms:    parseTranscriptionTerms(os.Getenv("CASSINI_TRANSCRIPTION_TERMS")),
	}
}

// envInt parses a positive integer env var, returning 0 when unset or invalid.
func envInt(key string) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// defaultDevice returns the device requested via CASSINI_STT_DEVICE
// (cpu / cuda / auto), or "auto" when unset so the device is auto-detected
// (GPU when present) in BuildMeetingArtifact.
func defaultDevice() string {
	if v := strings.TrimSpace(os.Getenv("CASSINI_STT_DEVICE")); v != "" {
		return v
	}
	return "auto"
}

func writeReadableArtifacts(outputDir string, streams []AudioStream, segments []Segment, audioDurationMS int64, sha256hex string, cfg BuildConfig, stdout io.Writer) ([]Segment, bool, error) {
	if !cfg.LLM.IsConfigured() {
		return nil, false, nil
	}

	fmt.Fprintln(stdout, "  running LLM readable cleanup...")
	llmCfg := cfg.LLM
	llmCfg.PreferredSpellings = preferredSpellingsForCleanup(cfg.TranscriptionTerms, streams)
	readableSegs, err := readableCleanupFn(llmCfg, segments)
	if err != nil {
		if cfg.StrictReadableCleanup {
			return nil, false, fmt.Errorf("readable cleanup: %w", err)
		}
		fmt.Fprintf(stdout, "  warn: LLM cleanup failed: %v — skipping readable transcript\n", err)
		return nil, false, nil
	}

	applied := ApplyReadableText(segments, readableSegs)

	readablePath := filepath.Join(outputDir, "transcript.readable.v1.json")
	// Readable cleanup produces a distinct artifact contract from the raw word
	// transcript. If we stamp it as transcript.words.v1, downstream loaders will
	// ignore the cleaned content and fall back to raw ASR text.
	if err := writeTranscriptWithHash(readablePath, "transcript.readable.v1", streams, applied, audioDurationMS, sha256hex); err != nil {
		return nil, false, fmt.Errorf("write readable transcript: %w", err)
	}

	captionsPath := filepath.Join(outputDir, "captions.vtt")
	if err := WriteCaptionsVTT(captionsPath, streams, applied); err != nil {
		return nil, false, fmt.Errorf("write captions: %w", err)
	}
	return applied, true, nil
}

func writeSummaryArtifact(outputDir string, streams []AudioStream, segments []Segment, cfg BuildConfig, stdout io.Writer) (bool, error) {
	if !cfg.SummaryLLM.IsConfigured() {
		return false, nil
	}

	fmt.Fprintln(stdout, "  generating meeting summary...")
	body, err := buildMeetingSummaryFn(cfg.SummaryLLM, streams, segments)
	if err != nil {
		fmt.Fprintf(stdout, "  warn: summary generation failed: %v — skipping summary\n", err)
		return false, nil
	}

	summaryPath := filepath.Join(outputDir, "summary.md")
	if err := os.WriteFile(summaryPath, []byte(body), 0o644); err != nil {
		return false, fmt.Errorf("write summary: %w", err)
	}
	return true, nil
}

// defaultCacheDir returns the default cache directory for models.
func defaultCacheDir() string {
	if v := os.Getenv("CASSINI_CACHE_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".cache", "cassini")
	}
	return filepath.Join(home, ".cache", "cassini")
}

func envBool(name string) bool {
	switch strings.TrimSpace(strings.ToLower(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// writeTranscriptWithHash writes a transcript JSON file, embedding the audio
// SHA-256 and the caller-specified transcript version.
func writeTranscriptWithHash(path string, version string, streams []AudioStream, segments []Segment, audioDurationMS int64, sha256hex string) error {
	if err := ValidateSegments(segments); err != nil {
		return err
	}

	switch version {
	case transcriptWordsVersion:
		return writeJSON(path, buildTranscriptFile(streams, segments, audioDurationMS, sha256hex))
	case readableTranscriptVersion:
		return writeJSON(path, buildReadableTranscriptFile(streams, segments, audioDurationMS, sha256hex))
	default:
		return fmt.Errorf("unsupported transcript version %q", version)
	}
}

// speakerEnvelopeCache memoises the per-track attribution envelopes for one
// build. Envelopes depend only on the audio and the analysis sample rate,
// never on the STT model, so the primary transcript and every additional
// model share one ffmpeg decode per track instead of re-decoding the whole
// meeting once per transcription pass.
type speakerEnvelopeCache struct {
	mu           sync.Mutex
	bySampleRate map[int][]*SpeakerEnvelope
}

// envelopes returns the cached envelopes for sampleRate, building them on the
// first call. A nil receiver builds directly, uncached.
func (c *speakerEnvelopeCache) envelopes(mkvPath string, streams []AudioStream, sampleRate int, progress io.Writer) ([]*SpeakerEnvelope, error) {
	if c == nil {
		return buildSpeakerEnvelopesFn(mkvPath, streams, sampleRate, progress)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if envs, ok := c.bySampleRate[sampleRate]; ok {
		return envs, nil
	}
	envs, err := buildSpeakerEnvelopesFn(mkvPath, streams, sampleRate, progress)
	if err != nil {
		return nil, err
	}
	if c.bySampleRate == nil {
		c.bySampleRate = make(map[int][]*SpeakerEnvelope, 1)
	}
	c.bySampleRate[sampleRate] = envs
	return envs, nil
}

// segmentsBelongToAnyStream reports whether at least one segment is attributed
// to one of the given participant streams.
func segmentsBelongToAnyStream(segments []Segment, streams []AudioStream) bool {
	ids := make(map[string]bool, len(streams))
	for _, s := range streams {
		ids[s.SpeakerID] = true
	}
	for _, seg := range segments {
		if ids[seg.SpeakerID] {
			return true
		}
	}
	return false
}

// applyAttribution annotates words with cross-track attribution evidence.
//
// It is deliberately non-fatal: attribution is provenance, and failing to
// measure it must never cost the operator a transcript that decoded fine. On
// any error the segments are returned untouched and the reason is reported.
func applyAttribution(mkvPath string, streams []AudioStream, segments []Segment, sampleRate int, cfg BuildConfig, stdout io.Writer) []Segment {
	return applyAttributionCached(mkvPath, streams, segments, sampleRate, cfg, nil, stdout)
}

// attributionModeForConfig names how the attribution stage was configured for
// this build: "disabled", "drop", or the default "annotate".
func attributionModeForConfig(cfg BuildConfig) string {
	switch {
	case cfg.SkipAttribution:
		return "disabled"
	case cfg.DropCrosstalk:
		return "drop"
	default:
		return "annotate"
	}
}

// applyAttributionCached is applyAttribution with a per-build envelope cache;
// a nil cache builds the envelopes directly.
func applyAttributionCached(mkvPath string, streams []AudioStream, segments []Segment, sampleRate int, cfg BuildConfig, cache *speakerEnvelopeCache, stdout io.Writer) []Segment {
	return applyAttributionReported(mkvPath, streams, segments, sampleRate, cfg, cache, stdout, nil)
}

// applyAttributionReported additionally records what the stage did — or why
// it did nothing — into report, for the manifest's provenance. In drop mode
// the deleted words carry their evidence away with them, so this record is
// the only trace the deletion leaves; without it, rebuilding the same
// recording without CASSINI_ATTRIBUTION_DROP yields a different transcript
// with byte-identical provenance. A nil report skips the bookkeeping.
func applyAttributionReported(mkvPath string, streams []AudioStream, segments []Segment, sampleRate int, cfg BuildConfig, cache *speakerEnvelopeCache, stdout io.Writer, report *AttributionProvenance) []Segment {
	realStreams := make([]AudioStream, 0, len(streams))
	for _, s := range streams {
		// The merged-fallback speaker is a synthetic mix of everyone, so it has
		// no track of its own to compare against and must not become a rival.
		if s.Index < 0 {
			continue
		}
		realStreams = append(realStreams, s)
	}
	if len(realStreams) < 2 {
		// Attribution compares tracks against each other; with one participant
		// there is nothing for a word to be misattributed away from.
		if report != nil {
			report.Reason = "single participant track; no rival track to measure against"
		}
		return segments
	}
	// After the merged fallback replaces the transcript, every word belongs to
	// the synthetic "merged" speaker: no envelope can ever match it, so
	// decoding every participant track would measure nothing at real cost.
	if !segmentsBelongToAnyStream(segments, realStreams) {
		fmt.Fprintln(stdout, "  attribution skipped: no transcript words belong to a participant track; nothing to measure")
		if report != nil {
			report.Reason = "no transcript words belong to a participant track (merged fallback)"
		}
		return segments
	}

	fmt.Fprintln(stdout, "  measuring cross-track speaker attribution...")
	envelopes, err := cache.envelopes(mkvPath, realStreams, sampleRate, nil)
	if err != nil {
		fmt.Fprintf(stdout, "  warn: attribution skipped: %v\n", err)
		if report != nil {
			report.Reason = fmt.Sprintf("failed: %v", err)
		}
		return segments
	}
	annotated, res := AnnotateAttribution(segments, envelopes, cfg.DropCrosstalk)
	if report != nil {
		report.Ran = true
		report.WordsMeasured = res.WordsMeasured
		report.WordsFlagged = res.Flagged
		report.WordsDropped = res.Dropped
		if res.ThresholdFound {
			threshold := res.ThresholdDB
			report.ThresholdDB = &threshold
		}
	}
	switch {
	case res.WordsMeasured == 0:
		fmt.Fprintln(stdout, "    no words could be measured; transcript unchanged")
	case !res.ThresholdFound:
		fmt.Fprintf(stdout, "    %d words measured; no crosstalk population in this meeting\n",
			res.WordsMeasured)
	case cfg.DropCrosstalk:
		fmt.Fprintf(stdout, "    %d words measured; threshold %.1f dB; %d dropped as crosstalk\n",
			res.WordsMeasured, res.ThresholdDB, res.Dropped)
	default:
		fmt.Fprintf(stdout, "    %d words measured; threshold %.1f dB; %d flagged low-confidence\n",
			res.WordsMeasured, res.ThresholdDB, res.Flagged)
	}
	return annotated
}
