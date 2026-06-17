package transcribe

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// BuildConfig holds runtime options for the transcription pipeline.
type BuildConfig struct {
	Device                string    // "cpu" or "cuda"
	ModelID               ModelID   // defaults to defaultModelID
	AdditionalModels      []ModelID // run extra transcription passes; each becomes a sibling transcript file referenced from manifest.files.transcripts
	CacheDir              string    // root cache directory, e.g. ~/.cache/cassini
	LLM                   LLMConfig // optional; if not configured, skip readable cleanup
	SummaryLLM            LLMConfig // optional; if not configured, skip summary generation
	StrictReadableCleanup bool      // fail the build if readable cleanup cannot complete
	NumThreads            int       // 0 = use default (4)
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
	if cfg.ModelID == "" {
		cfg.ModelID = DefaultModelID()
	}
	if cfg.Device == "" || cfg.Device == "auto" {
		cfg.Device = "cpu"
	}
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
	if err := WriteManifest(manifestPath, srcBasename, srcDurationMS, streams, segments, cfg.ModelID, cfg.LLM.Model, hasReadable, cfg.SummaryLLM.Model, hasSummary, additionalTranscripts); err != nil {
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
// synthetic "merged" speaker when the per-participant pass found nothing.
// Talk-recorder per-participant tracks can be sparse (DTX, comfort-noise
// frames between speaking turns) in ways that defeat the VAD even though
// the same audio sums to a clearly-transcribable mix; rather than ship an
// empty transcript in that case, fall back to the mix. Returns the
// (possibly extended) streams + segments. Does nothing when the
// per-participant pass already produced content.
// minWordsBeforeMergedFallback is the per-participant word threshold below
// which we re-run transcription over the merged mix. The strict ==0 gate
// was observed to skip fallback when a single stray word survived through
// an otherwise-failing pass (CI matrix run #5: 1 word total, fallback
// skipped, e2e word-run gate failed). 10 is conservative: well under the
// passing-run baseline of ~12-14 verbatim words, well over the noise.
const minWordsBeforeMergedFallback = 10

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
	extendedStreams := append(append([]AudioStream(nil), streams...), mergedStream)
	mergedSegments := MergeAndSortSegments([][]Segment{segments, mergedSegs})
	return extendedStreams, mergedSegments, nil
}

// transcribePass runs one full transcription pass over every speaker stream
// using the given recognizer config. Returns merged + sorted segments.
func transcribePass(ctx context.Context, mkvPath string, streams []AudioStream, modelPaths ModelPaths, vadPath, device string, numThreads int, stdout io.Writer) ([]Segment, error) {
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

	primary := ModelID(strings.TrimSpace(os.Getenv("CASSINI_STT_MODEL")))
	if primary == "" {
		primary = defaultModelID
	}

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
	}
}

// defaultDevice returns the device selected by CASSINI_STT_DEVICE if set
// (cpu / cuda / auto), or "cpu" if unset.
func defaultDevice() string {
	if v := strings.TrimSpace(os.Getenv("CASSINI_STT_DEVICE")); v != "" {
		return v
	}
	return "cpu"
}

func writeReadableArtifacts(outputDir string, streams []AudioStream, segments []Segment, audioDurationMS int64, sha256hex string, cfg BuildConfig, stdout io.Writer) ([]Segment, bool, error) {
	if !cfg.LLM.IsConfigured() {
		return nil, false, nil
	}

	fmt.Fprintln(stdout, "  running LLM readable cleanup...")
	readableSegs, err := readableCleanupFn(cfg.LLM, segments)
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
