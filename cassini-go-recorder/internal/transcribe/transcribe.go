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
	ModelID               ModelID   // defaults to ModelParakeet110M
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
		cfg.ModelID = DefaultModelID
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

	// --- 4. Create recognizer ---
	fmt.Fprintf(stdout, "  loading recognizer (device=%s)...\n", cfg.Device)
	rec, err := NewRecognizer(modelPaths, vadPath, cfg.Device, cfg.NumThreads)
	if err != nil {
		return fmt.Errorf("create recognizer: %w", err)
	}
	defer rec.Close()

	// --- 5. Transcribe each speaker track ---
	perSpeakerSegs := make([][]Segment, len(streams))
	for i, stream := range streams {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		fmt.Fprintf(stdout, "  transcribing %s (stream index %d)...\n", stream.SpeakerLabel, stream.Index)
		samples, err := ExtractSpeakerFloats(mkvPath, stream)
		if err != nil {
			return fmt.Errorf("extract audio for %s: %w", stream.SpeakerLabel, err)
		}
		words, err := rec.Transcribe(samples, modelPaths.SampleRate)
		if err != nil {
			return fmt.Errorf("transcribe %s: %w", stream.SpeakerLabel, err)
		}
		fmt.Fprintf(stdout, "    %s: %d words\n", stream.SpeakerLabel, len(words))
		perSpeakerSegs[i] = AssembleSegments(stream.SpeakerID, words, 0, 0)
	}

	// --- 6. Merge and sort all segments ---
	segments := MergeAndSortSegments(perSpeakerSegs)

	// --- 7. Write word-level transcript ---
	transcriptPath := filepath.Join(outputDir, "transcript.words.v1.json")
	if err := writeTranscriptWithHash(transcriptPath, "transcript.words.v1", streams, segments, audioDurationMS, sha256hex); err != nil {
		return fmt.Errorf("write transcript: %w", err)
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
	if err := WriteManifest(manifestPath, srcBasename, srcDurationMS, streams, segments, cfg.ModelID, cfg.LLM.Model, hasReadable, cfg.SummaryLLM.Model, hasSummary); err != nil {
		return fmt.Errorf("write manifest: %w", err)
	}

	fmt.Fprintf(stdout, "  done: %d segments, %d words\n", len(segments), CountWords(segments))
	return nil
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

	return BuildConfig{
		Device:                "cpu",
		ModelID:               DefaultModelID,
		LLM:                   llm,
		SummaryLLM:            summaryLLM,
		StrictReadableCleanup: envBool("CASSINI_READABLE_STRICT_BATCHES"),
	}
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
