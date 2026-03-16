package transcribe

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// BuildConfig holds runtime options for the transcription pipeline.
type BuildConfig struct {
	Device     string    // "cpu" or "cuda"
	ModelID    ModelID   // defaults to ModelParakeet110M
	CacheDir   string    // root cache directory, e.g. ~/.cache/cassini
	LLM        LLMConfig // optional; if not configured, skip readable cleanup
	NumThreads int       // 0 = use default (4)
}

// BuildMeetingArtifact transcribes an MKV recording and writes all meeting
// bundle artifacts to outputDir:
//   - meeting.webm          — mono 48 kHz Opus mix of all speakers
//   - transcript.words.v1.json
//   - transcript.readable.v1.json + captions.vtt  (if LLM configured)
//   - manifest.json
func BuildMeetingArtifact(ctx context.Context, mkvPath, outputDir string, cfg BuildConfig, stdout io.Writer) error {
	if cfg.ModelID == "" {
		cfg.ModelID = defaultModelID
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
	if err := MixDownToWebM(mkvPath, len(streams), webmPath); err != nil {
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

	// --- 3. Download / verify STT model ---
	fmt.Fprintf(stdout, "  ensuring model %s is cached...\n", cfg.ModelID)
	modelPaths, err := EnsureModel(cfg.CacheDir, cfg.ModelID, stdout)
	if err != nil {
		return fmt.Errorf("ensure model: %w", err)
	}

	// --- 4. Create recognizer ---
	fmt.Fprintf(stdout, "  loading recognizer (device=%s)...\n", cfg.Device)
	rec, err := NewRecognizer(modelPaths, cfg.Device, cfg.NumThreads)
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
		samples, err := ExtractSpeakerFloats(mkvPath, stream.Index)
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
	if err := writeTranscriptWithHash(transcriptPath, streams, segments, audioDurationMS, sha256hex); err != nil {
		return fmt.Errorf("write transcript: %w", err)
	}

	// --- 8. Optional: LLM readable cleanup ---
	hasReadable := false
	if cfg.LLM.IsConfigured() {
		fmt.Fprintln(stdout, "  running LLM readable cleanup...")
		readableSegs, err := ReadableCleanup(cfg.LLM, segments)
		if err != nil {
			// Non-fatal: log and skip readable artifacts.
			fmt.Fprintf(stdout, "  warn: LLM cleanup failed: %v — skipping readable transcript\n", err)
		} else {
			applied := ApplyReadableText(segments, readableSegs)

			readablePath := filepath.Join(outputDir, "transcript.readable.v1.json")
			if err := writeTranscriptWithHash(readablePath, streams, applied, audioDurationMS, sha256hex); err != nil {
				return fmt.Errorf("write readable transcript: %w", err)
			}

			captionsPath := filepath.Join(outputDir, "captions.vtt")
			if err := WriteCaptionsVTT(captionsPath, streams, applied); err != nil {
				return fmt.Errorf("write captions: %w", err)
			}
			hasReadable = true
		}
	}

	// --- 9. Write manifest ---
	manifestPath := filepath.Join(outputDir, "manifest.json")
	srcBasename := filepath.Base(mkvPath)
	if err := WriteManifest(manifestPath, srcBasename, srcDurationMS, streams, segments, cfg.LLM.Model, hasReadable); err != nil {
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
	return BuildConfig{
		Device:  "cpu",
		ModelID: defaultModelID,
		LLM:     llm,
	}
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

// writeTranscriptWithHash writes a transcript JSON file, embedding the audio SHA-256.
func writeTranscriptWithHash(path string, streams []AudioStream, segments []Segment, audioDurationMS int64, sha256hex string) error {
	// Build the file the normal way, then patch the sha256 in via the raw struct.
	// WriteTranscriptJSON doesn't accept a sha256 param, so we build the struct manually.
	type wordEntry struct {
		Text    string `json:"text"`
		StartMS int64  `json:"startMs"`
		EndMS   int64  `json:"endMs"`
	}
	type segmentEntry struct {
		Speaker string      `json:"speaker"`
		StartMS int64       `json:"startMs"`
		EndMS   int64       `json:"endMs"`
		Text    string      `json:"text"`
		Words   []wordEntry `json:"words"`
	}
	type speakerEntry struct {
		ID    string `json:"id"`
		Label string `json:"label"`
	}
	type mediaEntry struct {
		Src        string `json:"src"`
		DurationMS int64  `json:"durationMs"`
		SHA256     string `json:"sha256,omitempty"`
	}
	type doc struct {
		Version  string         `json:"version"`
		Media    mediaEntry     `json:"media"`
		Speakers []speakerEntry `json:"speakers"`
		Segments []segmentEntry `json:"segments"`
	}

	seen := map[string]bool{}
	var speakers []speakerEntry
	for _, seg := range segments {
		if !seen[seg.SpeakerID] {
			seen[seg.SpeakerID] = true
			label := labelForSpeaker(seg.SpeakerID, streams)
			speakers = append(speakers, speakerEntry{ID: seg.SpeakerID, Label: label})
		}
	}

	var segs []segmentEntry
	for _, seg := range segments {
		ws := make([]wordEntry, len(seg.Words))
		for i, w := range seg.Words {
			ws[i] = wordEntry{Text: w.Text, StartMS: w.StartMS, EndMS: w.EndMS}
		}
		segs = append(segs, segmentEntry{
			Speaker: seg.SpeakerID,
			StartMS: seg.StartMS,
			EndMS:   seg.EndMS,
			Text:    seg.Text,
			Words:   ws,
		})
	}

	d := doc{
		Version:  "transcript.words.v1",
		Media:    mediaEntry{Src: "meeting.webm", DurationMS: audioDurationMS, SHA256: sha256hex},
		Speakers: speakers,
		Segments: segs,
	}
	return writeJSON(path, d)
}
