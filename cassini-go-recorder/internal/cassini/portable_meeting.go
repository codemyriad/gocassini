package cassini

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gocassini/internal/meetingtime"
	"gocassini/internal/portable"
)

type portablePackOptions struct {
	Title        string
	CreatedAtUTC string
}

type portableMeetingSource struct {
	MeetingDir         string
	AudioPath          string
	Transcript         portableTranscriptArtifact
	ReadableTranscript map[string]any
	DisplayTranscript  map[string]any
	Artifact           portableMeetingArtifact
}

type portableMeetingArtifact struct {
	GeneratedAt string `json:"generatedAt"`
	Source      struct {
		Basename        string `json:"basename"`
		DurationMS      int64  `json:"durationMs"`
		RecordedAtLocal string `json:"recordedAtLocal"`
	} `json:"source"`
	Provenance *portable.Provenance `json:"provenance"`
	Files      struct {
		Audio              string `json:"audio"`
		Transcript         string `json:"transcript"`
		ReadableTranscript string `json:"readableTranscript"`
		DisplayTranscript  string `json:"displayTranscript"`
	} `json:"files"`
	SpeakerCount int `json:"speakerCount"`
	WordCount    int `json:"wordCount"`
}

type portableTranscriptArtifact struct {
	Version string `json:"version"`
	Media   struct {
		Src        string `json:"src"`
		DurationMS int64  `json:"durationMs"`
		SHA256     string `json:"sha256"`
	} `json:"media"`
	Speakers []portable.Speaker          `json:"speakers"`
	Segments []portableTranscriptSegment `json:"segments"`
}

type portableTranscriptSegment struct {
	Speaker string                   `json:"speaker"`
	StartMS int64                    `json:"startMs"`
	EndMS   int64                    `json:"endMs"`
	Text    string                   `json:"text"`
	Words   []portableTranscriptWord `json:"words"`
}

type portableTranscriptWord struct {
	Text    string `json:"text"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
}

type portableAudioProbe struct {
	Streams []portableAudioProbeStream `json:"streams"`
	Format  struct {
		Duration string `json:"duration"`
	} `json:"format"`
}

type portableAudioProbeStream struct {
	CodecType  string `json:"codec_type"`
	CodecName  string `json:"codec_name"`
	SampleRate string `json:"sample_rate"`
	Channels   int    `json:"channels"`
	Duration   string `json:"duration"`
}

type portableAudioIntegrity struct {
	SampleRate  int
	Channels    int
	SampleCount int64
	DurationMS  int64
	PCMSHA256   string
}

func isPortableMeetingOutput(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".opus")
}

func preparePortableMeetingOutput(path string) (string, error) {
	resolved, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve portable meeting output path: %w", err)
	}
	if info, err := os.Stat(resolved); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("portable meeting output already exists as a directory: %s", resolved)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("stat portable meeting output path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(resolved), 0o755); err != nil {
		return "", fmt.Errorf("create portable meeting output directory: %w", err)
	}
	return resolved, nil
}

func createPortableWorkRoot(finalOutputPath string) (string, error) {
	workParent := filepath.Join(filepath.Dir(finalOutputPath), ".cassini-work")
	if err := os.MkdirAll(workParent, 0o755); err != nil {
		return "", fmt.Errorf("create work root: %w", err)
	}
	prefix := sanitizeWorkPrefix(strings.TrimSuffix(filepath.Base(finalOutputPath), filepath.Ext(finalOutputPath)))
	if prefix == "" {
		prefix = "meeting"
	}
	root := filepath.Join(workParent, prefix+".portable-work")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create work directory: %w", err)
	}
	return root, nil
}

func sanitizeWorkPrefix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-.")
}

func packMeetingBundle(ctx context.Context, meetingDir string, outPath string, opts portablePackOptions) error {
	resolvedOut, err := preparePortableMeetingOutput(outPath)
	if err != nil {
		return err
	}

	source, err := loadPortableMeetingSource(meetingDir)
	if err != nil {
		return err
	}
	stagedAudioPath, err := createPortableStagePath(resolvedOut)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(stagedAudioPath)
	}()
	if err := writePortableMeetingFile(ctx, source.AudioPath, stagedAudioPath, nil); err != nil {
		return fmt.Errorf("stage portable meeting audio: %w", err)
	}

	audio, err := computePortableAudioIntegrity(stagedAudioPath)
	if err != nil {
		return err
	}
	manifest, err := buildPortableMeetingManifest(source, audio, resolvedOut, opts)
	if err != nil {
		return err
	}
	payload, err := portable.EncodeManifest(manifest, portable.DefaultPayloadChunkSize)
	if err != nil {
		return err
	}
	finalStagePath, err := createPortableStagePath(resolvedOut)
	if err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(finalStagePath)
	}()
	if err := writePortableMeetingFile(ctx, stagedAudioPath, finalStagePath, portable.BuildOpusTags(manifest, payload)); err != nil {
		return err
	}
	if err := verifyPortableMeetingFile(finalStagePath, manifest); err != nil {
		return err
	}
	if err := os.Remove(resolvedOut); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("replace portable meeting output: %w", err)
	}
	if err := os.Rename(finalStagePath, resolvedOut); err != nil {
		return fmt.Errorf("move portable meeting output into place: %w", err)
	}
	return nil
}

func loadPortableMeetingSource(meetingDir string) (portableMeetingSource, error) {
	rootDir, err := filepath.Abs(meetingDir)
	if err != nil {
		return portableMeetingSource{}, fmt.Errorf("resolve meeting bundle path: %w", err)
	}

	artifactPath := filepath.Join(rootDir, "manifest.json")
	artifact := portableMeetingArtifact{}
	if raw, err := os.ReadFile(artifactPath); err == nil {
		if err := json.Unmarshal(raw, &artifact); err != nil {
			return portableMeetingSource{}, fmt.Errorf("parse meeting artifact manifest: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return portableMeetingSource{}, fmt.Errorf("read meeting artifact manifest: %w", err)
	}

	audioName := strings.TrimSpace(artifact.Files.Audio)
	if audioName == "" {
		audioName = "meeting.webm"
	}
	audioPath := filepath.Join(rootDir, audioName)
	if _, err := os.Stat(audioPath); err != nil {
		return portableMeetingSource{}, fmt.Errorf("meeting audio missing: %s", audioPath)
	}

	transcriptName := strings.TrimSpace(artifact.Files.Transcript)
	if transcriptName == "" {
		transcriptName = "transcript.words.v1.json"
	}
	transcriptPath := filepath.Join(rootDir, transcriptName)
	rawTranscript, err := os.ReadFile(transcriptPath)
	if err != nil {
		return portableMeetingSource{}, fmt.Errorf("read transcript artifact: %w", err)
	}

	var transcript portableTranscriptArtifact
	if err := json.Unmarshal(rawTranscript, &transcript); err != nil {
		return portableMeetingSource{}, fmt.Errorf("parse transcript artifact: %w", err)
	}

	readableTranscript, err := loadPortableReadableTranscript(rootDir, artifact.Files.ReadableTranscript)
	if err != nil {
		return portableMeetingSource{}, err
	}
	displayTranscript, err := loadPortableDisplayTranscript(rootDir, artifact.Files.DisplayTranscript)
	if err != nil {
		return portableMeetingSource{}, err
	}

	return portableMeetingSource{
		MeetingDir:         rootDir,
		AudioPath:          audioPath,
		Transcript:         transcript,
		ReadableTranscript: readableTranscript,
		DisplayTranscript:  displayTranscript,
		Artifact:           artifact,
	}, nil
}

func loadPortableReadableTranscript(rootDir string, artifactPath string) (map[string]any, error) {
	readableName := strings.TrimSpace(artifactPath)
	if readableName == "" {
		defaultPath := filepath.Join(rootDir, "transcript.readable.v1.json")
		if _, err := os.Stat(defaultPath); err == nil {
			readableName = "transcript.readable.v1.json"
		} else if os.IsNotExist(err) {
			return nil, nil
		} else {
			return nil, fmt.Errorf("stat readable transcript artifact: %w", err)
		}
	}

	rawReadable, err := os.ReadFile(filepath.Join(rootDir, readableName))
	if err != nil {
		return nil, fmt.Errorf("read readable transcript artifact: %w", err)
	}

	var readable map[string]any
	if err := json.Unmarshal(rawReadable, &readable); err != nil {
		return nil, fmt.Errorf("parse readable transcript artifact: %w", err)
	}
	return readable, nil
}

func loadPortableDisplayTranscript(rootDir string, artifactPath string) (map[string]any, error) {
	displayName := strings.TrimSpace(artifactPath)
	if displayName == "" {
		defaultPath := filepath.Join(rootDir, "transcript.display.v1.json")
		if _, err := os.Stat(defaultPath); err == nil {
			displayName = "transcript.display.v1.json"
		} else if os.IsNotExist(err) {
			return nil, nil
		} else {
			return nil, fmt.Errorf("stat display transcript artifact: %w", err)
		}
	}

	rawDisplay, err := os.ReadFile(filepath.Join(rootDir, displayName))
	if err != nil {
		return nil, fmt.Errorf("read display transcript artifact: %w", err)
	}

	var display map[string]any
	if err := json.Unmarshal(rawDisplay, &display); err != nil {
		return nil, fmt.Errorf("parse display transcript artifact: %w", err)
	}
	return display, nil
}

func computePortableAudioIntegrity(audioPath string) (portableAudioIntegrity, error) {
	probe, err := probePortableSourceAudio(audioPath)
	if err != nil {
		return portableAudioIntegrity{}, err
	}
	stream, err := firstPortableAudioStream(probe)
	if err != nil {
		return portableAudioIntegrity{}, err
	}
	if !strings.EqualFold(stream.CodecName, "opus") {
		return portableAudioIntegrity{}, fmt.Errorf("portable meeting audio must be Opus, got %s", strings.TrimSpace(stream.CodecName))
	}

	sampleRate := parsePortableInt(stream.SampleRate)
	if sampleRate != 48000 {
		return portableAudioIntegrity{}, fmt.Errorf("portable meeting audio must be 48000 Hz, got %d", sampleRate)
	}
	channels := stream.Channels
	if channels != 1 && channels != 2 {
		return portableAudioIntegrity{}, fmt.Errorf("portable meeting audio must have 1 or 2 channels, got %d", channels)
	}

	pcmBytes, err := decodePortablePCM(audioPath, sampleRate, channels)
	if err != nil {
		return portableAudioIntegrity{}, err
	}
	sum := sha256.Sum256(pcmBytes)
	sampleCount := int64(len(pcmBytes) / (2 * channels))
	durationMS := int64(sampleCount * 1000 / int64(sampleRate))
	return portableAudioIntegrity{
		SampleRate:  sampleRate,
		Channels:    channels,
		SampleCount: sampleCount,
		DurationMS:  durationMS,
		PCMSHA256:   hex.EncodeToString(sum[:]),
	}, nil
}

func buildPortableMeetingManifest(source portableMeetingSource, audio portableAudioIntegrity, outPath string, opts portablePackOptions) (portable.Manifest, error) {
	items := flattenPortableTranscriptItems(source.Transcript)
	title := strings.TrimSpace(opts.Title)
	if title == "" {
		title = titleFromOutputPath(outPath)
	}
	if title == "" {
		title = titleFromSourceName(source.Artifact.Source.Basename)
	}
	if title == "" {
		title = "Cassini Meeting"
	}

	createdAt := strings.TrimSpace(opts.CreatedAtUTC)
	if createdAt == "" {
		createdAt = strings.TrimSpace(source.Artifact.GeneratedAt)
	}
	if createdAt == "" {
		if info, err := os.Stat(source.AudioPath); err == nil {
			createdAt = info.ModTime().UTC().Format(time.RFC3339)
		}
	}
	if createdAt == "" {
		createdAt = time.Now().UTC().Format(time.RFC3339)
	}
	recordedAtLocal := strings.TrimSpace(source.Artifact.Source.RecordedAtLocal)
	if recordedAtLocal == "" {
		recordedAtLocal = meetingtime.InferRecordedAtLocal(source.Artifact.Source.Basename)
	}
	processedAtUTC := strings.TrimSpace(source.Artifact.GeneratedAt)

	manifest := portable.NormalizeManifest(portable.Manifest{
		Meeting: portable.Meeting{
			ID:              portable.MeetingIDFromPCMHash(audio.PCMSHA256),
			Title:           title,
			CreatedAtUTC:    createdAt,
			RecordedAtLocal: recordedAtLocal,
			ProcessedAtUTC:  processedAtUTC,
			DurationMS:      audio.DurationMS,
		},
		Audio: portable.Audio{
			Container:   "ogg",
			Codec:       "opus",
			SampleRate:  audio.SampleRate,
			Channels:    audio.Channels,
			SampleCount: audio.SampleCount,
			DurationMS:  audio.DurationMS,
		},
		Integrity: portable.Integrity{
			MatchPolicy: portable.AudioMatchPolicy,
			PCMFormat:   portable.AudioPCMFormat,
			PCMSHA256:   audio.PCMSHA256,
			SampleRate:  audio.SampleRate,
			Channels:    audio.Channels,
			SampleCount: audio.SampleCount,
			DurationMS:  audio.DurationMS,
		},
		Speakers: source.Transcript.Speakers,
		Transcript: portable.Transcript{
			Format:    "cassini.words.v1",
			WordCount: len(items),
			Items:     items,
		},
		Provenance: source.Artifact.Provenance,
	})
	if source.ReadableTranscript != nil {
		manifest.ReadableTranscript = source.ReadableTranscript
	}
	if source.DisplayTranscript != nil {
		manifest.DisplayTranscript = source.DisplayTranscript
	}
	return manifest, nil
}

func flattenPortableTranscriptItems(transcript portableTranscriptArtifact) []portable.TranscriptItem {
	items := make([]portable.TranscriptItem, 0)
	for _, segment := range transcript.Segments {
		if len(segment.Words) == 0 && strings.TrimSpace(segment.Text) != "" {
			items = append(items, portable.TranscriptItem{
				Speaker: segment.Speaker,
				StartMS: segment.StartMS,
				EndMS:   segment.EndMS,
				Text:    segment.Text,
			})
			continue
		}
		for _, word := range segment.Words {
			if strings.TrimSpace(word.Text) == "" {
				continue
			}
			items = append(items, portable.TranscriptItem{
				Speaker: segment.Speaker,
				StartMS: word.StartMS,
				EndMS:   word.EndMS,
				Text:    word.Text,
			})
		}
	}
	return items
}

func titleFromOutputPath(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return strings.TrimSpace(base)
}

func titleFromSourceName(path string) string {
	base := strings.TrimSpace(path)
	if base == "" {
		return ""
	}
	return strings.TrimSpace(strings.TrimSuffix(filepath.Base(base), filepath.Ext(base)))
}

func writePortableMeetingFile(ctx context.Context, audioPath string, outPath string, tags map[string]string) error {
	args := []string{
		"-y",
		"-v", "error",
		"-i", audioPath,
		"-map_metadata", "-1",
		"-map", "0:a:0",
		"-c:a", "copy",
	}

	keys := make([]string, 0, len(tags))
	for key, value := range tags {
		if strings.TrimSpace(value) == "" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "-metadata", key+"="+tags[key])
	}
	args = append(args, outPath)

	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("write portable meeting file: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}

func createPortableStagePath(outPath string) (string, error) {
	file, err := os.CreateTemp(filepath.Dir(outPath), ".cassini-stage-*.opus")
	if err != nil {
		return "", fmt.Errorf("create staged portable meeting path: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("close staged portable meeting path: %w", err)
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("prepare staged portable meeting path: %w", err)
	}
	return path, nil
}

func verifyPortableMeetingFile(path string, manifest portable.Manifest) error {
	audio, err := computePortableAudioIntegrity(path)
	if err != nil {
		return fmt.Errorf("verify portable meeting file: %w", err)
	}
	if audio.PCMSHA256 != manifest.Integrity.PCMSHA256 {
		return fmt.Errorf("verify portable meeting file: pcm sha256 mismatch")
	}
	if audio.SampleRate != manifest.Integrity.SampleRate {
		return fmt.Errorf("verify portable meeting file: sample rate mismatch")
	}
	if audio.Channels != manifest.Integrity.Channels {
		return fmt.Errorf("verify portable meeting file: channel mismatch")
	}
	if audio.SampleCount != manifest.Integrity.SampleCount {
		return fmt.Errorf("verify portable meeting file: sample count mismatch")
	}
	if audio.DurationMS != manifest.Integrity.DurationMS {
		return fmt.Errorf("verify portable meeting file: duration mismatch")
	}
	return nil
}

func probePortableSourceAudio(path string) (portableAudioProbe, error) {
	raw, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=duration:stream=codec_type,codec_name,sample_rate,channels,duration",
		"-of", "json",
		path,
	).Output()
	if err != nil {
		return portableAudioProbe{}, fmt.Errorf("ffprobe meeting audio: %w", err)
	}

	var probe portableAudioProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		return portableAudioProbe{}, fmt.Errorf("parse ffprobe meeting audio json: %w", err)
	}
	return probe, nil
}

func firstPortableAudioStream(probe portableAudioProbe) (portableAudioProbeStream, error) {
	for _, stream := range probe.Streams {
		if strings.EqualFold(stream.CodecType, "audio") {
			return stream, nil
		}
	}
	return portableAudioProbeStream{}, fmt.Errorf("meeting audio file has no audio stream")
}

func decodePortablePCM(path string, sampleRate int, channels int) ([]byte, error) {
	cmd := exec.Command(
		"ffmpeg",
		"-v", "error",
		"-i", path,
		"-map", "0:a:0",
		"-f", "s16le",
		"-acodec", "pcm_s16le",
		"-ar", strconv.Itoa(sampleRate),
		"-ac", strconv.Itoa(channels),
		"-",
	)
	raw, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg decode meeting audio: %w", err)
	}
	return raw, nil
}

func parsePortableInt(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	i, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return i
}
