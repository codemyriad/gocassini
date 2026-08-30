package cassini

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
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
	// RoomToken and RoomName name the conversation this meeting was recorded in
	// (D-622). Unlike Title they have no fallback chain: a room is either known
	// or it is not, and guessing one from a file name would invent an identity.
	//
	// The TOKEN is the input, not the published value. It is a capability for a
	// public conversation, so what lands in the manifest is a one-way derivation
	// of it (portable.RoomIDForMeeting) and the token itself stops here.
	//
	// RoomName is an INPUT ONLY as of D-640: it feeds the name-domain
	// derivation for a meeting with no token, and is embedded as the Title,
	// but it is no longer written to the manifest's roomName. See
	// portable.Meeting.RoomName for why.
	RoomToken string
	RoomName  string
	// JobID and AttemptNumber record which operator job and attempt produced
	// the artifact (D-640). Optional, like the room — a bundle packed outside
	// the operator has neither, and inventing one would claim a lineage that
	// does not exist.
	JobID         string
	AttemptNumber int
}

type portableMeetingSource struct {
	MeetingDir         string
	AudioPath          string
	Transcript         portableTranscriptArtifact
	ReadableTranscript map[string]any
	DisplayTranscript  map[string]any
	SummaryMarkdown    []byte
	Artifact           portableMeetingArtifact
	// AdditionalTranscripts is populated when the bundle's manifest.json
	// includes files.transcripts[]. Each entry already has its file loaded
	// off disk. Empty in v1 bundles.
	AdditionalTranscripts []portableNamedTranscript
}

// portableNamedTranscript pairs a loaded transcript file with the multi-track metadata
// the producer needs (id, role, default, provenance).
type portableNamedTranscript struct {
	ID         string
	Role       string
	Default    bool
	Language   string
	Provenance *portable.ProcessingStep
	Transcript portableTranscriptArtifact
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
		Audio              string                               `json:"audio"`
		Transcript         string                               `json:"transcript"`
		ReadableTranscript string                               `json:"readableTranscript"`
		DisplayTranscript  string                               `json:"displayTranscript"`
		Summary            string                               `json:"summary"`
		Transcripts        []portableMeetingTranscriptInputFile `json:"transcripts,omitempty"`
	} `json:"files"`
	SpeakerCount int `json:"speakerCount"`
	WordCount    int `json:"wordCount"`
}

// portableMeetingTranscriptInputFile describes one transcript file in a
// multi-transcript bundle. Present in manifest.json under `files.transcripts`.
// When this list is non-empty the packer emits a v3 portable-meeting file with
// one entry per element; the singular `files.transcript` is ignored. When the
// list is empty, a v3 file with one synthesized raw-asr entry is emitted.
type portableMeetingTranscriptInputFile struct {
	ID         string                   `json:"id"`
	Path       string                   `json:"path"`
	Role       string                   `json:"role"`
	Default    bool                     `json:"default,omitempty"`
	Language   string                   `json:"language,omitempty"`
	Provenance *portable.ProcessingStep `json:"provenance,omitempty"`
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
	// Optional cross-track speaker-attribution evidence, carried through to
	// the packed portable transcript items. Pointer/omitempty so an
	// unmeasured word round-trips with neither key present.
	AttributionGapDB     *float64 `json:"attributionGapDb,omitempty"`
	LowConfidenceSpeaker bool     `json:"lowConfidenceSpeaker,omitempty"`
}

type portableAudioIntegrity struct {
	SampleRate  int
	Channels    int
	SampleCount int64
	DurationMS  int64
	OpusSHA256  string
	PCMSHA256   string
}

// maxPortableMeetingIdentityPasses bounds metadata remux convergence. FFmpeg
// can normalize an Ogg Opus final granule on the first Ogg -> Ogg stream copy
// (notably after an amix/alimiter WebM input). The next copy is stable in the
// affected FFmpeg 9 path, but keep this bounded and fail closed rather than
// silently publishing a manifest for different playable audio.
const maxPortableMeetingIdentityPasses = 4

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

	// Build tags from the identity of the file that feeds the metadata remux,
	// then confirm the output has that same identity. FFmpeg normally preserves
	// it on the first pass. For end-trim-sensitive mixed WebM, FFmpeg 9 can
	// normalize the first Ogg final granule by a few samples on the next remux.
	// When that happens, treat the remuxed file as the normalized input, rebuild
	// the manifest/meeting ID, and try again. This preserves end-trim binding in
	// exact-opus-audio-v1 instead of weakening the digest to ignore the change.
	currentAudioPath := stagedAudioPath
	for pass := 1; pass <= maxPortableMeetingIdentityPasses; pass++ {
		audio, err := computePortableAudioIntegrity(currentAudioPath)
		if err != nil {
			return err
		}
		manifest, err := buildPortableMeetingManifest(source, audio, resolvedOut, opts)
		if err != nil {
			return err
		}
		opusTags, err := buildPortableMeetingV3TagsFromSource(manifest, source)
		if err != nil {
			return err
		}

		candidatePath, err := createPortableStagePath(resolvedOut)
		if err != nil {
			return err
		}
		defer func(path string) {
			_ = os.Remove(path)
		}(candidatePath)
		if err := writePortableMeetingFile(ctx, currentAudioPath, candidatePath, opusTags); err != nil {
			return err
		}
		candidateAudio, err := computePortableAudioIntegrity(candidatePath)
		if err != nil {
			return err
		}
		if portableAudioIntegrityEqual(audio, candidateAudio) {
			if err := verifyPortableOpusIntegrity(candidateAudio, manifest.Integrity); err != nil {
				return err
			}
			return commitPortableMeetingOutput(candidatePath, resolvedOut)
		}
		if pass == maxPortableMeetingIdentityPasses {
			return fmt.Errorf(
				"portable Opus identity did not stabilize after %d metadata remuxes: before sha256=%s samples=%d duration_ms=%d; after sha256=%s samples=%d duration_ms=%d",
				pass,
				audio.OpusSHA256, audio.SampleCount, audio.DurationMS,
				candidateAudio.OpusSHA256, candidateAudio.SampleCount, candidateAudio.DurationMS,
			)
		}
		_ = os.Remove(currentAudioPath)
		currentAudioPath = candidatePath
	}
	return fmt.Errorf("portable Opus identity stabilization exhausted unexpectedly")
}

func portableAudioIntegrityEqual(left, right portableAudioIntegrity) bool {
	return left.OpusSHA256 == right.OpusSHA256 &&
		left.SampleRate == right.SampleRate &&
		left.Channels == right.Channels &&
		left.SampleCount == right.SampleCount &&
		left.DurationMS == right.DurationMS
}

// commitPortableMeetingOutput publishes the verified stage file as the portable
// meeting output.
//
// One rename, and nothing before it. rename(2) replaces an existing regular
// file atomically: a reader either sees the whole previous `.opus` or the whole
// new one, never neither. The output used to be removed first, which bought
// nothing and cost the only copy — a crash or an I/O error in that gap left the
// meeting with no portable artifact at all, and the operator now treats that
// artifact as a publish precondition (D-583).
//
// The rename is always same-filesystem: createPortableStagePath puts the stage
// file in the output's own directory. The one destination shape rename cannot
// replace is a directory, which preparePortableMeetingOutput already rejects.
func commitPortableMeetingOutput(stagePath, outPath string) error {
	if err := os.Rename(stagePath, outPath); err != nil {
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
	summaryMarkdown, err := loadPortableSummaryMarkdown(rootDir, artifact.Files.Summary)
	if err != nil {
		return portableMeetingSource{}, err
	}

	additional, err := loadPortableAdditionalTranscripts(rootDir, artifact.Files.Transcripts)
	if err != nil {
		return portableMeetingSource{}, err
	}

	return portableMeetingSource{
		MeetingDir:            rootDir,
		AudioPath:             audioPath,
		Transcript:            transcript,
		ReadableTranscript:    readableTranscript,
		DisplayTranscript:     displayTranscript,
		SummaryMarkdown:       summaryMarkdown,
		Artifact:              artifact,
		AdditionalTranscripts: additional,
	}, nil
}

func loadPortableAdditionalTranscripts(rootDir string, entries []portableMeetingTranscriptInputFile) ([]portableNamedTranscript, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	loaded := make([]portableNamedTranscript, 0, len(entries))
	for _, entry := range entries {
		path := strings.TrimSpace(entry.Path)
		if path == "" {
			return nil, fmt.Errorf("multi-transcript entry %q has empty path", entry.ID)
		}
		raw, err := os.ReadFile(filepath.Join(rootDir, path))
		if err != nil {
			return nil, fmt.Errorf("read transcript file %s: %w", path, err)
		}
		var transcript portableTranscriptArtifact
		if err := json.Unmarshal(raw, &transcript); err != nil {
			return nil, fmt.Errorf("parse transcript file %s: %w", path, err)
		}
		loaded = append(loaded, portableNamedTranscript{
			ID:         entry.ID,
			Role:       entry.Role,
			Default:    entry.Default,
			Language:   entry.Language,
			Provenance: entry.Provenance,
			Transcript: transcript,
		})
	}
	return loaded, nil
}

func loadPortableSummaryMarkdown(rootDir string, artifactPath string) ([]byte, error) {
	name := strings.TrimSpace(artifactPath)
	if name == "" {
		// No summary listed in manifest.json. Don't fall back to a default
		// path: absence of the manifest entry is the signal that the artifact
		// directory was built without summaries.
		return nil, nil
	}
	raw, err := os.ReadFile(filepath.Join(rootDir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read summary artifact: %w", err)
	}
	return raw, nil
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
	file, err := os.Open(audioPath)
	if err != nil {
		return portableAudioIntegrity{}, fmt.Errorf("open portable Opus audio: %w", err)
	}
	defer file.Close()

	integrity, err := portable.ComputeOpusAudioIntegrity(file)
	if err != nil {
		return portableAudioIntegrity{}, fmt.Errorf("hash portable Opus audio: %w", err)
	}
	return portableAudioIntegrity{
		SampleRate:  integrity.SampleRate,
		Channels:    integrity.Channels,
		SampleCount: integrity.SampleCount,
		DurationMS:  integrity.DurationMS,
		OpusSHA256:  integrity.SHA256,
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

	manifest := portable.NormalizeManifestV3(portable.Manifest{
		Meeting: portable.Meeting{
			ID:              portable.MeetingIDFromAudioHash(audio.OpusSHA256),
			Title:           title,
			CreatedAtUTC:    createdAt,
			RecordedAtLocal: recordedAtLocal,
			ProcessedAtUTC:  processedAtUTC,
			DurationMS:      audio.DurationMS,
			// Derived here and nowhere else on this path: the raw token must
			// not reach the manifest, the OpusTags or anything that reads them.
			RoomID: portable.RoomIDForMeeting(portable.RoomIDPepperFromEnv(), opts.RoomToken, opts.RoomName),
			// RoomName is deliberately NOT set (D-640). The room's name at
			// record time is the Title above; its current name belongs in the
			// catalog, where changing it does not mean rewriting a sealed file.
			JobID:         strings.TrimSpace(opts.JobID),
			AttemptNumber: opts.AttemptNumber,
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
			OpusSHA256:  audio.OpusSHA256,
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
	if len(source.SummaryMarkdown) > 0 {
		manifest.Summary = buildPortableSummaryMetadata(source.Artifact.Provenance)
		manifest.Attachments = append(manifest.Attachments, map[string]any{
			"name":          "summary.md",
			"mime":          "text/markdown",
			"contentBase64": base64.StdEncoding.EncodeToString(source.SummaryMarkdown),
		})
	}
	return manifest, nil
}

func buildPortableSummaryMetadata(prov *portable.Provenance) map[string]any {
	meta := map[string]any{
		"format":          "markdown",
		"templateVersion": "v0",
	}
	if prov != nil && prov.MeetingSummary != nil {
		if model := strings.TrimSpace(prov.MeetingSummary.Model); model != "" {
			meta["model"] = model
		}
	}
	return meta
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
				Speaker:              segment.Speaker,
				StartMS:              word.StartMS,
				EndMS:                word.EndMS,
				Text:                 word.Text,
				AttributionGapDB:     word.AttributionGapDB,
				LowConfidenceSpeaker: word.LowConfidenceSpeaker,
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
	policy := strings.ToLower(strings.TrimSpace(manifest.Integrity.MatchPolicy))
	if policy == "" {
		if manifest.Integrity.OpusSHA256 != "" {
			policy = portable.AudioMatchPolicy
		} else {
			policy = portable.LegacyAudioMatchPolicyPCM
		}
	}

	var (
		audio portableAudioIntegrity
		err   error
	)
	switch policy {
	case portable.AudioMatchPolicy:
		audio, err = computePortableAudioIntegrity(path)
		if err != nil {
			return fmt.Errorf("verify portable meeting file: %w", err)
		}
		return verifyPortableOpusIntegrity(audio, manifest.Integrity)
	case portable.LegacyAudioMatchPolicyPCM:
		if manifest.Integrity.PCMFormat != "" && !strings.EqualFold(manifest.Integrity.PCMFormat, portable.AudioPCMFormat) {
			return fmt.Errorf("verify portable meeting file: unsupported pcm format %q", manifest.Integrity.PCMFormat)
		}
		audio, err = computeLegacyPortablePCMIntegrity(path, manifest.Integrity.SampleRate, manifest.Integrity.Channels)
		if err == nil && audio.PCMSHA256 != manifest.Integrity.PCMSHA256 {
			return fmt.Errorf("verify portable meeting file: decoded PCM sha256 mismatch")
		}
	default:
		return fmt.Errorf("verify portable meeting file: unsupported audio match policy %q", policy)
	}
	if err != nil {
		return fmt.Errorf("verify portable meeting file: %w", err)
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

func verifyPortableOpusIntegrity(audio portableAudioIntegrity, integrity portable.Integrity) error {
	if audio.OpusSHA256 != integrity.OpusSHA256 {
		return fmt.Errorf(
			"verify portable meeting file: compressed Opus sha256 mismatch: manifest=%s actual=%s manifest_samples=%d actual_samples=%d manifest_duration_ms=%d actual_duration_ms=%d",
			integrity.OpusSHA256,
			audio.OpusSHA256,
			integrity.SampleCount,
			audio.SampleCount,
			integrity.DurationMS,
			audio.DurationMS,
		)
	}
	if audio.SampleRate != integrity.SampleRate {
		return fmt.Errorf("verify portable meeting file: sample rate mismatch")
	}
	if audio.Channels != integrity.Channels {
		return fmt.Errorf("verify portable meeting file: channel mismatch")
	}
	if audio.SampleCount != integrity.SampleCount {
		return fmt.Errorf("verify portable meeting file: sample count mismatch")
	}
	if audio.DurationMS != integrity.DurationMS {
		return fmt.Errorf("verify portable meeting file: duration mismatch")
	}
	return nil
}

// computeLegacyPortablePCMIntegrity exists only for checking v1/v2 files.
// New files use ComputeOpusAudioIntegrity and never decode their recording for
// identity. Stream stdout into the digest so inspecting a long legacy meeting
// does not buffer hours of PCM in memory.
func computeLegacyPortablePCMIntegrity(path string, sampleRate, channels int) (portableAudioIntegrity, error) {
	if sampleRate <= 0 || channels <= 0 {
		return portableAudioIntegrity{}, fmt.Errorf("legacy PCM integrity needs a positive sample rate and channel count")
	}

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
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return portableAudioIntegrity{}, fmt.Errorf("open ffmpeg PCM output: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return portableAudioIntegrity{}, fmt.Errorf("start ffmpeg PCM decode: %w", err)
	}
	digest := sha256.New()
	byteCount, copyErr := io.Copy(digest, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return portableAudioIntegrity{}, fmt.Errorf("read ffmpeg PCM output: %w", copyErr)
	}
	if waitErr != nil {
		return portableAudioIntegrity{}, fmt.Errorf("ffmpeg decode meeting audio: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	bytesPerSampleFrame := int64(2 * channels)
	if byteCount%bytesPerSampleFrame != 0 {
		return portableAudioIntegrity{}, fmt.Errorf("ffmpeg produced %d PCM bytes, not a whole number of %d-byte frames", byteCount, bytesPerSampleFrame)
	}
	sampleCount := byteCount / bytesPerSampleFrame
	return portableAudioIntegrity{
		SampleRate:  sampleRate,
		Channels:    channels,
		SampleCount: sampleCount,
		DurationMS:  sampleCount * 1000 / int64(sampleRate),
		PCMSHA256:   hex.EncodeToString(digest.Sum(nil)),
	}, nil
}
