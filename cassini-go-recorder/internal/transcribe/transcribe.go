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
}

var (
	readableCleanupFn     = ReadableCleanup
	buildMeetingSummaryFn = BuildMeetingSummary
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
	fmt.Fprintf(stdout, "  STT policy: device=%s model=%s threads=%d quality=%s\n",
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

	// --- 2. Mix down to meeting.webm ---
	webmPath := filepath.Join(outputDir, "meeting.webm")
	fmt.Fprintln(stdout, "  mixing audio to meeting.webm...")
	if err := MixDownToWebM(mkvPath, streams, webmPath); err != nil {
		return fmt.Errorf("mix audio: %w", err)
	}

	// Compute SHA-256 of the decoded PCM for integrity tracking.
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
	modelPaths, err := EnsureModel(cfg.CacheDir, cfg.ModelID, stdout)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}

	vadPath, err := EnsureVAD(cfg.CacheDir, stdout)
	if err != nil {
		return fmt.Errorf("ensure VAD model: %w", err)
	}

	// --- 4. Transcribe with the primary model, then any additional models.
	// The primary model writes the default transcript.words.v1.json (v1
	// fallback). Each additional model writes a sibling transcript file and
	// is recorded under manifest.files.transcripts for v2 multi-tx output.
	segments, err := transcribePass(ctx, mkvPath, streams, modelPaths, vadPath, cfg.Device, cfg.NumThreads, stdout)
	if err != nil {
		return err
	}

	// Per-participant Talk recordings can carry sparse / DTX-encoded audio
	// that defeats the VAD even when the mixed timeline clearly contains
	// speech. Fall back to transcribing the already-mixed meeting.webm under
	// a synthetic "merged" speaker so the bundle still ships usable content.
	streams, segments, err = ensureMergedFallback(ctx, webmPath, streams, segments, modelPaths, vadPath, cfg.Device, cfg.NumThreads, stdout)
	if err != nil {
		return err
	}

	if err := writeTranscriptWithHash(filepath.Join(outputDir, "transcript.words.v1.json"), "transcript.words.v1", streams, segments, audioDurationMS, sha256hex); err != nil {
		return fmt.Errorf("write transcript: %w", err)
	}

	additionalTranscripts, err := runAdditionalTranscripts(ctx, mkvPath, outputDir, streams, audioDurationMS, sha256hex, cfg, stdout)
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
	hasSummary, err := writeSummaryArtifact(outputDir, streams, summaryInput, cfg, stdout)
	if err != nil {
		return err
	}

	// --- 10. Write manifest ---
	manifestPath := filepath.Join(outputDir, "manifest.json")
	srcBasename := filepath.Base(mkvPath)
	if err := WriteManifest(manifestPath, srcBasename, srcDurationMS, audioDurationMS, streams, segments, cfg.ModelID, cfg.LLM.Model, hasReadable, cfg.SummaryLLM.Model, hasSummary, additionalTranscripts); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Fprintf(stdout, "  done: %d segments, %d words\n", len(segments), CountWords(segments))
	return nil
}

// AdditionalTranscript describes one extra transcript file emitted by a
// secondary STT model. The producer turns these into manifest.files.transcripts
// entries so the v2 portable-meeting packer can fan them out into separate
// CASSINI_TX_<ID>_PAYLOAD_* tag sets.
type AdditionalTranscript struct {
	ID      string  // sanitised model id, suitable for tag namespace (a-z 0-9 - _, max 32 chars)
	Path    string  // relative to outputDir, e.g. transcript-parakeet-tdt-06b-v2-int8.words.v1.json
	ModelID ModelID // raw STT model id, kept for provenance
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

func ensureMergedFallback(ctx context.Context, webmPath string, streams []AudioStream, segments []Segment, modelPaths ModelPaths, vadPath, device string, numThreads int, stdout io.Writer) ([]AudioStream, []Segment, error) {
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

	rec, err := NewRecognizer(modelPaths, vadPath, device, numThreads)
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
func transcribePass(ctx context.Context, mkvPath string, streams []AudioStream, modelPaths ModelPaths, vadPath, device string, numThreads int, stdout io.Writer) ([]Segment, error) {
	conc := resolveStreamConcurrency(len(streams), numThreads, device)
	if conc <= 1 {
		return transcribeStreamsSequential(ctx, mkvPath, streams, modelPaths, vadPath, device, numThreads, stdout)
	}
	return transcribeStreamsParallel(ctx, mkvPath, streams, modelPaths, vadPath, device, numThreads, conc, stdout)
}

// transcribeStreamsSequential transcribes each speaker stream one at a time with
// a single shared recognizer. Used when concurrency resolves to 1 (single
// speaker, tight thread budget, or low free RAM).
func transcribeStreamsSequential(ctx context.Context, mkvPath string, streams []AudioStream, modelPaths ModelPaths, vadPath, device string, numThreads int, stdout io.Writer) ([]Segment, error) {
	fmt.Fprintf(stdout, "  loading recognizer (device=%s)...\n", device)
	rec, err := NewRecognizer(modelPaths, vadPath, device, numThreads)
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
func transcribeStreamsParallel(ctx context.Context, mkvPath string, streams []AudioStream, modelPaths ModelPaths, vadPath, device string, numThreads, conc int, stdout io.Writer) ([]Segment, error) {
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
		rec, err := NewRecognizer(modelPaths, vadPath, device, threadsPer)
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
func runAdditionalTranscripts(ctx context.Context, mkvPath, outputDir string, streams []AudioStream, audioDurationMS int64, sha256hex string, cfg BuildConfig, stdout io.Writer) ([]AdditionalTranscript, error) {
	if len(cfg.AdditionalModels) == 0 {
		return nil, nil
	}
	vadPath, err := EnsureVAD(cfg.CacheDir, stdout)
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
		modelPaths, err := EnsureModel(cfg.CacheDir, modelID, stdout)
		if err != nil {
			return nil, fmt.Errorf("ensure additional model %s: %w", modelID, err)
		}
		segs, err := transcribePass(ctx, mkvPath, streams, modelPaths, vadPath, cfg.Device, cfg.NumThreads, stdout)
		if err != nil {
			return nil, fmt.Errorf("additional transcribe %s: %w", modelID, err)
		}
		path := fmt.Sprintf("transcript-%s.words.v1.json", id)
		if err := writeTranscriptWithHash(filepath.Join(outputDir, path), "transcript.words.v1", streams, segs, audioDurationMS, sha256hex); err != nil {
			return nil, fmt.Errorf("write additional transcript %s: %w", id, err)
		}
		out = append(out, AdditionalTranscript{ID: id, Path: path, ModelID: modelID})
	}
	return out, nil
}

// sanitizeTranscriptID maps a model id to the format-v2 transcript-id regex
// ^[a-z0-9][a-z0-9_-]{0,31}$. Dots and other unsupported runes become hyphens
// and the result is truncated to 32 runes.
func sanitizeTranscriptID(id string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 32 {
		out = strings.TrimRight(out[:32], "-_")
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
