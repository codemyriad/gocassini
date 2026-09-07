package inspect

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gocassini/internal/portable"
)

type probedPortableAudio struct {
	Streams []probedPortableAudioStream `json:"streams"`
	Format  struct {
		FormatName string            `json:"format_name"`
		Duration   string            `json:"duration"`
		Tags       map[string]string `json:"tags"`
	} `json:"format"`
}

type probedPortableAudioStream struct {
	Index      int               `json:"index"`
	CodecType  string            `json:"codec_type"`
	CodecName  string            `json:"codec_name"`
	SampleRate string            `json:"sample_rate"`
	Channels   int               `json:"channels"`
	Duration   string            `json:"duration"`
	Tags       map[string]string `json:"tags"`
}

type portablePayloadInfo struct {
	Encoding        string
	Schema          string
	ChunkCount      int
	RawBytes        int
	CompressedBytes int
	SHA256          string
	Compressed      []byte
	JSON            []byte
}

type portableIntegrityResult struct {
	Status         string
	Warnings       []string
	SampleRate     int
	Channels       int
	SampleCount    int64
	DurationMS     int64
	OpusHashSHA256 string
}

func detectPortableAudioPath(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	if strings.EqualFold(filepath.Ext(path), ".opus") {
		return path, true
	}
	return "", false
}

func inspectPortableAudio(out io.Writer, path string) error {
	meta, err := probePortableAudio(path)
	if err != nil {
		return err
	}
	stream, err := firstAudioStream(meta)
	if err != nil {
		return err
	}
	audioSummary := portableAudioSummary{
		Path:       path,
		Container:  blankDash(meta.Format.FormatName),
		Codec:      blankDash(stream.CodecName),
		SampleRate: parseIntOrZero(stream.SampleRate),
		Channels:   stream.Channels,
		DurationMS: durationStringToMS(firstNonEmpty(stream.Duration, meta.Format.Duration)),
	}

	tags := mergePortableTags(meta.Format.Tags, stream.Tags)
	formatTag := metadataTag(tags, "CASSINI_FORMAT")
	if formatTag == "" {
		printPlainPortableAudio(out, audioSummary, "plain-audio", "")
		return nil
	}
	if !isPublishedPortableFormat(formatTag) {
		err := fmt.Errorf("unsupported CASSINI_FORMAT=%s", formatTag)
		printPlainPortableAudio(out, audioSummary, "unknown-cassini-format", err.Error())
		return err
	}

	payload, manifest, err := decodePortableMeeting(tags)
	if err != nil {
		// Nothing from an unusable manifest is safe to display.
		printPlainPortableAudio(out, audioSummary, "invalid-cassini-metadata", err.Error())
		return fmt.Errorf("%s: %w", path, err)
	}

	bodies := readPortableTranscriptBodies(tags, manifest)

	integrity, verifyErr := verifyPortableAudioIntegrity(path, tags, manifest)
	if verifyErr != nil {
		integrity = portableIntegrityResult{
			Status:   "integrity-unverified",
			Warnings: []string{verifyErr.Error()},
		}
	}
	// A repeated load-bearing tag means the file was edited and no manifest
	// field is trustworthy. A missing body affects only that transcript.
	if bodies.Repeated {
		printPlainPortableAudio(out, audioSummary, "invalid-cassini-metadata", strings.Join(bodies.Warnings, "; "))
		return fmt.Errorf("%s: a load-bearing tag appears twice", path)
	}

	audioStatus := integrity.Status
	printPortableMeeting(out, path, audioSummary, payload, manifest, bodies, integrity)
	if audioStatus != "ok" {
		fmt.Fprintln(out, "fallback=plain-audio")
	}
	return bodies.err(path)
}

// portableTranscriptBodies is what a reader could actually get out of one
// file's per-transcript chunk sets, as against what its manifest index says is
// in there.
type portableTranscriptBodies struct {
	// DefaultID is the transcript a viewer opens first, resolved from the
	// manifest. It is the one whose word count stands for the meeting.
	DefaultID string
	// WordCounts holds the decoded word count per transcript id. A transcript
	// whose body could not be read is absent from the map, not zero in it.
	WordCounts map[string]int
	// Unreadable names every transcript whose body could not be read, in
	// manifest order.
	Unreadable []string
	// Repeated marks a repeated chunk comment, which invalidates the file.
	Repeated bool
	Warnings []string
}

// err reports the transcripts whose bodies could not be read, so `cassini
// inspect` exits non-zero on a file it could not read the meeting out of. A
// command that prints a summary and exits 0 tells a script the file is fine.
func (b portableTranscriptBodies) err(path string) error {
	if len(b.Unreadable) == 0 {
		return nil
	}
	return fmt.Errorf("%s: could not read the transcript body of %s", path, strings.Join(b.Unreadable, ", "))
}

// readPortableTranscriptBodies decodes every transcript body the manifest
// declares.
//
// inspect used to print the manifest index's declared wordCount and label it
// `words=`, which meant the one number it reported about the transcript was the
// only one it had not checked: a file whose chunk set had a hole in it still
// printed `cassini=ok words=900` while the transcript was unreachable. Reading
// the bodies costs one gzip inflate each against metadata already in memory,
// and it is the only way `words=` can mean anything.
func readPortableTranscriptBodies(tags map[string]string, manifest portable.Manifest) portableTranscriptBodies {
	bodies := portableTranscriptBodies{WordCounts: map[string]int{}}
	if entry, warnings, ok := defaultWordsTranscriptEntry(tags, manifest); ok {
		bodies.DefaultID = entry.ID
		bodies.Warnings = append(bodies.Warnings, warnings...)
	}
	for _, entry := range manifest.Transcripts {
		body, warnings, err := decodeTranscriptBody(tags, entry)
		bodies.Warnings = append(bodies.Warnings, warnings...)
		if err != nil {
			if errors.Is(err, errRepeatedTag) {
				bodies.Repeated = true
			}
			bodies.Unreadable = append(bodies.Unreadable, entry.ID)
			bodies.Warnings = append(bodies.Warnings, fmt.Sprintf("transcript %s body could not be read: %v", entry.ID, err))
			continue
		}
		if body.WordCount != 0 && body.WordCount != len(body.Items) {
			bodies.Warnings = append(bodies.Warnings, fmt.Sprintf(
				"transcript %s declares wordCount=%d and carries %d items; using the items",
				entry.ID, body.WordCount, len(body.Items)))
		}
		bodies.WordCounts[entry.ID] = len(body.Items)
	}
	for _, entry := range manifest.ReadableTranscripts {
		// A withdrawn readable-cleanup body is skipped, not decoded. Its shape
		// is not a words transcript, so validating it would report a perfectly
		// good legacy file as carrying an unreadable body.
		if entry.Role == portable.RoleWithdrawnReadableCleanup {
			continue
		}
		_, warnings, err := decodeTranscriptBody(tags, entry)
		bodies.Warnings = append(bodies.Warnings, warnings...)
		if err != nil {
			if errors.Is(err, errRepeatedTag) {
				bodies.Repeated = true
			}
			bodies.Unreadable = append(bodies.Unreadable, entry.ID)
			bodies.Warnings = append(bodies.Warnings, fmt.Sprintf("transcript %s body could not be read: %v", entry.ID, err))
		}
	}
	return bodies
}

type portableAudioSummary struct {
	Path       string
	Container  string
	Codec      string
	SampleRate int
	Channels   int
	DurationMS int64
}

func printPlainPortableAudio(out io.Writer, audio portableAudioSummary, cassiniStatus string, warning string) {
	fmt.Fprintf(out, "audio=%s container=%s codec=%s sample_rate=%d channels=%d duration_ms=%d cassini=%s\n",
		audio.Path, audio.Container, audio.Codec, audio.SampleRate, audio.Channels, audio.DurationMS, cassiniStatus)
	if strings.TrimSpace(warning) != "" {
		fmt.Fprintf(out, "warning=%s\n", warning)
	}
}

func printPortableMeeting(out io.Writer, path string, audio portableAudioSummary, payload portablePayloadInfo, manifest portable.Manifest, bodies portableTranscriptBodies, integrity portableIntegrityResult) {
	title := blankDash(manifest.Meeting.Title)
	meetingID := blankDash(manifest.Meeting.ID)
	createdAt := blankDash(manifest.Meeting.CreatedAtUTC)
	// The main payload carries descriptors; the word count comes from the body
	// that was actually decoded, not the descriptor's claim. Alternative raw
	// transcripts are additional passes over the same speech and are not summed.
	wordCount := bodies.WordCounts[bodies.DefaultID]
	language := manifest.Meeting.Language
	if language == "" {
		for _, entry := range manifest.Transcripts {
			if entry.ID == bodies.DefaultID {
				language = entry.Language
				break
			}
		}
	}
	if integrity.SampleRate == 0 {
		integrity.SampleRate = audio.SampleRate
	}
	if integrity.Channels == 0 {
		integrity.Channels = audio.Channels
	}
	if integrity.DurationMS == 0 {
		integrity.DurationMS = audio.DurationMS
	}
	fmt.Fprintf(out, "portable_meeting=%s title=%s meeting_id=%s created_at=%s speakers=%d words=%d duration_ms=%d cassini=%s\n",
		path, title, meetingID, createdAt, len(manifest.Speakers), wordCount, manifest.Meeting.DurationMS, integrity.Status)
	fmt.Fprintf(out, "audio container=%s codec=%s sample_rate=%d channels=%d duration_ms=%d",
		audio.Container, audio.Codec, integrity.SampleRate, integrity.Channels, integrity.DurationMS)
	if integrity.OpusHashSHA256 != "" {
		fmt.Fprintf(out, " opus_sha256=%s", integrity.OpusHashSHA256)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "payload encoding=%s schema=%s chunks=%d raw_bytes=%d compressed_bytes=%d sha256=%s language=%s\n",
		payload.Encoding, blankDash(payload.Schema), payload.ChunkCount, payload.RawBytes, payload.CompressedBytes, blankDash(payload.SHA256), blankDash(language))
	printPortableOrigin(out, manifest.Meeting)
	for _, entry := range manifest.Transcripts {
		printPortableTranscriptEntry(out, "transcript", entry, bodies.DefaultID)
	}
	for _, entry := range manifest.ReadableTranscripts {
		printPortableTranscriptEntry(out, "derived_transcript", entry, "")
	}
	if manifest.Provenance != nil {
		printProcessingStep(out, "speech_to_text", manifest.Provenance.SpeechToText)
		printProcessingStep(out, "meeting_summary", manifest.Provenance.MeetingSummary)
	}
	printSummaryMetadata(out, manifest.Summary)
	printAttachments(out, manifest.Attachments)
	for _, warning := range bodies.Warnings {
		fmt.Fprintf(out, "warning=%s\n", warning)
	}
	for _, warning := range integrity.Warnings {
		fmt.Fprintf(out, "warning=%s\n", warning)
	}
}

// printPortableOrigin prints where a meeting came from: the room it was
// recorded in, and the operator job and attempt that produced the file.
//
// It is the by-hand verification path for the whole producer chain, and until
// D-640 it did not exist — `cassini inspect` decoded the room and dropped it, so
// checking that a recording carried its room meant a raw `ffprobe` against tag
// names the docs did not list. That is a poor answer for the command the docs
// point at, and a worse one now that a maintenance tool can rewrite these
// fields and an operator needs to confirm the rewrite landed.
//
// The whole line is omitted when a meeting has none of them, rather than
// printing four dashes: a file packed by hand genuinely has no origin, and a
// row of dashes reads like a lookup that failed.
func printPortableOrigin(out io.Writer, meeting portable.Meeting) {
	if meeting.RoomID == "" && meeting.JobID == "" && meeting.AttemptNumber == 0 {
		return
	}
	attempt := "-"
	if meeting.AttemptNumber > 0 {
		attempt = fmt.Sprintf("%d", meeting.AttemptNumber)
	}
	fmt.Fprintf(out, "origin room_id=%s job_id=%s attempt=%s\n",
		blankDash(meeting.RoomID), blankDash(meeting.JobID), attempt)
}

func printPortableTranscriptEntry(out io.Writer, label string, entry portable.TranscriptEntry, defaultID string) {
	defaultMarker := "no"
	if entry.ID != "" && entry.ID == defaultID {
		defaultMarker = "yes"
	}
	source := entry.SourceTranscriptID
	if source == "" {
		source = "-"
	}
	fmt.Fprintf(out, "%s id=%s role=%s default=%s format=%s language=%s word_count=%d source=%s sha256=%s\n",
		label,
		blankDash(entry.ID),
		blankDash(entry.Role),
		defaultMarker,
		blankDash(entry.Format),
		blankDash(entry.Language),
		entry.WordCount,
		source,
		blankDash(entry.PayloadRef.SHA256),
	)
}

func printSummaryMetadata(out io.Writer, summary map[string]any) {
	if len(summary) == 0 {
		return
	}
	keys := make([]string, 0, len(summary))
	for k := range summary {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprint(out, "summary")
	for _, k := range keys {
		fmt.Fprintf(out, " %s=%s", k, blankDash(fmt.Sprintf("%v", summary[k])))
	}
	fmt.Fprintln(out)
}

func printAttachments(out io.Writer, attachments []map[string]any) {
	for _, att := range attachments {
		name, _ := att["name"].(string)
		mime, _ := att["mime"].(string)
		size := -1
		if encoded, ok := att["contentBase64"].(string); ok {
			if raw, err := base64.StdEncoding.DecodeString(encoded); err == nil {
				size = len(raw)
			}
		}
		bytesField := "-"
		if size >= 0 {
			bytesField = fmt.Sprintf("%d", size)
		}
		fmt.Fprintf(out, "attachment name=%s mime=%s bytes=%s\n",
			blankDash(name), blankDash(mime), bytesField)
	}
}

func printProcessingStep(out io.Writer, label string, step *portable.ProcessingStep) {
	if step == nil {
		return
	}
	fmt.Fprintf(out, "%s backend=%s engine=%s model=%s device=%s language=%s source=%s host=%s\n",
		label,
		blankDash(step.Backend),
		blankDash(step.Engine),
		blankDash(step.Model),
		blankDash(step.Device),
		blankDash(step.Language),
		blankDash(step.Source),
		blankDash(step.Host),
	)
}

func probePortableAudio(path string) (probedPortableAudio, error) {
	raw, err := exec.Command(
		"ffprobe",
		"-v", "error",
		"-show_entries", "format=format_name,duration:format_tags:stream=index,codec_type,codec_name,sample_rate,channels,duration:stream_tags",
		"-of", "json",
		path,
	).Output()
	if err != nil {
		return probedPortableAudio{}, fmt.Errorf("ffprobe portable audio: %w", err)
	}

	var meta probedPortableAudio
	if err := json.Unmarshal(raw, &meta); err != nil {
		return probedPortableAudio{}, fmt.Errorf("parse ffprobe portable audio json: %w", err)
	}
	return meta, nil
}

func firstAudioStream(meta probedPortableAudio) (probedPortableAudioStream, error) {
	for _, stream := range meta.Streams {
		if strings.EqualFold(stream.CodecType, "audio") {
			return stream, nil
		}
	}
	return probedPortableAudioStream{}, errors.New("portable audio file has no audio stream")
}

// decodeCassiniBase64URL decodes one concatenated chunk set.
//
// Producers write the unpadded base64url alphabet of RFC 4648 section 5, and so
// does the packer in internal/portable, but a reader is not the producer: a
// padded chunk set is a cosmetic difference, not a damaged file, and refusing
// it turns a readable meeting into a lost one. SPEC.md's own worked example
// pipes the chunks through a base64 tool that pads, and most languages' encoders
// pad by default, so files written by hand and by other implementations arrive
// both ways.
//
// ASCII whitespace is discarded before the padding is considered, not after: a
// Vorbis comment value is arbitrary UTF-8 and a tool that rewrapped a long
// chunk may legally have left a newline in it, so deciding on padding from the
// unstripped text is a bug that only shows on some inputs.
func decodeCassiniBase64URL(encoded string) ([]byte, error) {
	stripped := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, encoded)
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(stripped, "="))
}

func isPublishedPortableFormat(formatTag string) bool {
	return strings.TrimSpace(formatTag) == portable.Format
}

var errRepeatedTag = errors.New("repeated tag")

func chunkValue(tags map[string]string, key string) (string, error) {
	part := metadataTag(tags, key)
	if strings.Contains(part, ";") {
		return "", fmt.Errorf("%w %s", errRepeatedTag, key)
	}
	return part, nil
}

func decodePortableMeeting(tags map[string]string) (portablePayloadInfo, portable.Manifest, error) {
	formatTag := metadataTag(tags, "CASSINI_FORMAT")
	if formatTag == "" {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("missing CASSINI_FORMAT")
	}
	if !isPublishedPortableFormat(formatTag) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("unsupported CASSINI_FORMAT=%s", formatTag)
	}
	for _, required := range []struct {
		name string
		want string
	}{
		{name: "CASSINI_PROFILE", want: portable.Profile},
		{name: "CASSINI_PAYLOAD_MIME", want: portable.PayloadMIME},
		{name: "CASSINI_PAYLOAD_ENCODING", want: portable.PayloadEncoding},
		{name: "CASSINI_PAYLOAD_SCHEMA", want: portable.PayloadSchema},
		{name: "CASSINI_AUDIO_MATCH_POLICY", want: portable.AudioMatchPolicy},
	} {
		if got := strings.TrimSpace(metadataTag(tags, required.name)); got != required.want {
			return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("unsupported %s=%q", required.name, got)
		}
	}
	if digest := strings.TrimSpace(metadataTag(tags, "CASSINI_AUDIO_OPUS_SHA256")); !validPortableDigest(digest) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("missing or invalid CASSINI_AUDIO_OPUS_SHA256")
	}
	chunkCount := parseIntOrZero(metadataTag(tags, "CASSINI_PAYLOAD_CHUNK_COUNT"))
	if chunkCount <= 0 {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT")
	}
	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("CASSINI_PAYLOAD_%03d", idx)
		part, err := chunkValue(tags, key)
		if err != nil {
			return portablePayloadInfo{}, portable.Manifest{}, err
		}
		if part == "" {
			return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("missing payload chunk %s", key)
		}
		encoded.WriteString(part)
	}

	compressed, err := decodeCassiniBase64URL(encoded.String())
	if err != nil {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("decode base64url Cassini payload: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("open gzip Cassini payload: %w", err)
	}
	defer func() {
		_ = reader.Close()
	}()
	rawJSON, err := io.ReadAll(reader)
	if err != nil {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("decompress gzip Cassini payload: %w", err)
	}

	if declared := parseIntOrZero(metadataTag(tags, "CASSINI_PAYLOAD_RAW_BYTES")); declared <= 0 || declared != len(rawJSON) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("payload raw byte count mismatch: tags=%d decoded=%d", declared, len(rawJSON))
	}
	if declared := parseIntOrZero(metadataTag(tags, "CASSINI_PAYLOAD_GZIP_BYTES")); declared <= 0 || declared != len(compressed) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("payload compressed byte count mismatch: tags=%d decoded=%d", declared, len(compressed))
	}
	declared := strings.TrimSpace(metadataTag(tags, "CASSINI_PAYLOAD_SHA256"))
	if !validPortableDigest(declared) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("missing or invalid CASSINI_PAYLOAD_SHA256")
	}
	sum := sha256.Sum256(rawJSON)
	if hex.EncodeToString(sum[:]) != declared {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("payload sha256 mismatch")
	}

	manifest, err := portable.DecodePublishedManifest(rawJSON)
	if err != nil {
		return portablePayloadInfo{}, portable.Manifest{}, err
	}
	if tagged := strings.TrimSpace(metadataTag(tags, "CASSINI_AUDIO_OPUS_SHA256")); tagged != manifest.Integrity.OpusSHA256 {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf(
			"compressed Opus sha256 disagrees between tag and manifest: tag=%s manifest=%s",
			tagged, manifest.Integrity.OpusSHA256,
		)
	}

	return portablePayloadInfo{
		Encoding:        metadataTag(tags, "CASSINI_PAYLOAD_ENCODING"),
		Schema:          metadataTag(tags, "CASSINI_PAYLOAD_SCHEMA"),
		ChunkCount:      chunkCount,
		RawBytes:        len(rawJSON),
		CompressedBytes: len(compressed),
		SHA256:          strings.ToLower(metadataTag(tags, "CASSINI_PAYLOAD_SHA256")),
		Compressed:      compressed,
		JSON:            rawJSON,
	}, manifest, nil
}

func validPortableDigest(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func verifyPortableAudioIntegrity(path string, tags map[string]string, manifest portable.Manifest) (portableIntegrityResult, error) {
	manifestPolicy := strings.ToLower(strings.TrimSpace(manifest.Integrity.MatchPolicy))
	tagPolicy := strings.ToLower(strings.TrimSpace(metadataTag(tags, "CASSINI_AUDIO_MATCH_POLICY")))
	if manifestPolicy != "" && tagPolicy != "" && manifestPolicy != tagPolicy {
		return portableIntegrityResult{}, fmt.Errorf("audio integrity policy mismatch: manifest=%q tag=%q", manifestPolicy, tagPolicy)
	}
	policy := manifestPolicy
	if policy == "" {
		policy = tagPolicy
	}
	if policy != portable.AudioMatchPolicy {
		return portableIntegrityResult{}, fmt.Errorf("unsupported audio integrity matchPolicy %q", policy)
	}
	return verifyPortableOpusIntegrity(path, tags, manifest)
}

func verifyPortableOpusIntegrity(path string, tags map[string]string, manifest portable.Manifest) (portableIntegrityResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return portableIntegrityResult{}, fmt.Errorf("open portable Opus audio: %w", err)
	}
	defer file.Close()

	audio, err := portable.ComputeOpusAudioIntegrity(file)
	if err != nil {
		return portableIntegrityResult{}, fmt.Errorf("hash portable Opus audio: %w", err)
	}
	expect := strings.ToLower(strings.TrimSpace(manifest.Integrity.OpusSHA256))
	tagExpect := strings.ToLower(strings.TrimSpace(metadataTag(tags, "CASSINI_AUDIO_OPUS_SHA256")))
	if expect != "" && tagExpect != "" && expect != tagExpect {
		return portableIntegrityResult{}, fmt.Errorf("compressed Opus sha256 mismatch between manifest and tag")
	}
	if expect == "" {
		expect = tagExpect
	}
	if expect == "" {
		return portableIntegrityResult{}, fmt.Errorf("compressed Opus integrity policy has no opusSha256")
	}

	status, warnings := comparePortableAudioShape(manifest.Integrity, audio.SampleRate, audio.Channels, audio.SampleCount, audio.DurationMS)
	if expect != audio.SHA256 {
		status = "stale-audio"
		warnings = append([]string{fmt.Sprintf("compressed Opus sha256 mismatch: manifest=%s actual=%s", expect, audio.SHA256)}, warnings...)
	}
	return portableIntegrityResult{
		Status:         status,
		Warnings:       warnings,
		SampleRate:     audio.SampleRate,
		Channels:       audio.Channels,
		SampleCount:    audio.SampleCount,
		DurationMS:     audio.DurationMS,
		OpusHashSHA256: audio.SHA256,
	}, nil
}

func comparePortableAudioShape(expected portable.Integrity, sampleRate, channels int, sampleCount, durationMS int64) (string, []string) {
	status := "ok"
	warnings := []string{}
	if expected.SampleRate > 0 && expected.SampleRate != sampleRate {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("sample rate mismatch: manifest=%d actual=%d", expected.SampleRate, sampleRate))
	}
	if expected.Channels > 0 && expected.Channels != channels {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("channel count mismatch: manifest=%d actual=%d", expected.Channels, channels))
	}
	if expected.SampleCount > 0 && expected.SampleCount != sampleCount {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("sample count mismatch: manifest=%d actual=%d", expected.SampleCount, sampleCount))
	}
	if expected.DurationMS > 0 && deltaAbs(expected.DurationMS, durationMS) > 1 {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("duration mismatch: manifest=%d actual=%d", expected.DurationMS, durationMS))
	}
	return status, warnings
}

func durationStringToMS(value string) int64 {
	if strings.TrimSpace(value) == "" || value == "N/A" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(seconds * 1000.0)
}

func parseIntOrZero(value string) int {
	if strings.TrimSpace(value) == "" {
		return 0
	}
	i, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return i
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func deltaAbs(a int64, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

func mergePortableTags(sets ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, set := range sets {
		for key, value := range set {
			merged[key] = value
		}
	}
	return merged
}

// TranscriptWord is one decoded word read back out of a published portable
// .opus. It mirrors the per-word shape of a transcript.words.v1.json segment.
type TranscriptWord struct {
	Text    string `json:"text"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Speaker string `json:"speaker,omitempty"`
	// Optional cross-track speaker-attribution evidence, read back from the
	// packed transcript items so a re-rendered words document keeps it.
	AttributionGapDB     *float64 `json:"attributionGapDb,omitempty"`
	LowConfidenceSpeaker bool     `json:"lowConfidenceSpeaker,omitempty"`
}

// ExtractedTranscript is the default words transcript recovered from a
// portable .opus, plus the descriptor metadata needed to render a
// transcript.words.v1.json-shaped document for downstream text checks.
type ExtractedTranscript struct {
	TranscriptID string
	Role         string
	Format       string
	Language     string
	WordCount    int
	Words        []TranscriptWord
}

// ExtractTranscriptWords reads the default raw-ASR ("words") transcript back
// out of a published portable .opus. It is the inverse of
// portable.EncodeTranscriptBody: it reuses the same ffprobe tag read and main
// manifest decode as inspect (probePortableAudio + decodePortableMeeting),
// finds the default transcript id, gathers that transcript's
// CASSINI_TX_<ID>_PAYLOAD_* chunk set, concatenates + base64url-decodes +
// gzip-decompresses + parses the
// TranscriptBody JSON, and reconstructs the ordered words.
//
// It is a projection of ExtractMeeting, which does that whole decode and also
// keeps the manifest and the summary attachment. The decode lives in exactly
// one place so a reader that needs more than the words does not grow a second
// copy of the tag layout.
func ExtractTranscriptWords(path string) (ExtractedTranscript, error) {
	meeting, err := ExtractMeeting(path)
	if err != nil {
		return ExtractedTranscript{}, err
	}
	return meeting.Transcript, nil
}

func extractedFromTranscriptBody(id, format, language string, wordCount int, items []portable.TranscriptItem) ExtractedTranscript {
	words := make([]TranscriptWord, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Text) == "" {
			continue
		}
		words = append(words, TranscriptWord{
			Text:                 item.Text,
			StartMS:              item.StartMS,
			EndMS:                item.EndMS,
			Speaker:              item.Speaker,
			AttributionGapDB:     item.AttributionGapDB,
			LowConfidenceSpeaker: item.LowConfidenceSpeaker,
		})
	}
	if wordCount == 0 {
		wordCount = len(words)
	}
	return ExtractedTranscript{
		TranscriptID: id,
		Format:       format,
		Language:     language,
		WordCount:    wordCount,
		Words:        words,
	}
}

// defaultWordsTranscriptEntry picks the default raw-words transcript descriptor
// out of a decoded manifest, and reports any disagreement it had to resolve on
// the way.
//
// The manifest decides. An entry's `default` flag is the record;
// CASSINI_TRANSCRIPT_DEFAULT is the copy that exists so a reader holding only
// ffprobe can see something before it decodes anything, and once the manifest
// is decoded the copy is spent. This used to run the other way round, which
// meant a tool that rewrote the tag and not the manifest silently moved which
// transcript every consumer read.
//
// Preference: the entry flagged Default, then the first words transcript.
// CASSINI_TRANSCRIPT_DEFAULT is a discoverability copy and disagreement is a
// warning, never a resolver.
func defaultWordsTranscriptEntry(tags map[string]string, manifest portable.Manifest) (portable.TranscriptEntry, []string, bool) {
	if len(manifest.Transcripts) == 0 {
		return portable.TranscriptEntry{}, nil, false
	}
	taggedID := strings.TrimSpace(metadataTag(tags, "CASSINI_TRANSCRIPT_DEFAULT"))
	for _, entry := range manifest.Transcripts {
		if !entry.Default {
			continue
		}
		var warnings []string
		if taggedID != "" && taggedID != entry.ID {
			warnings = append(warnings, fmt.Sprintf(
				"default transcript disagrees between manifest and tag: manifest=%s CASSINI_TRANSCRIPT_DEFAULT=%s",
				entry.ID, taggedID))
		}
		return entry, warnings, true
	}
	first := manifest.Transcripts[0]
	var warnings []string
	if taggedID != "" && taggedID != first.ID {
		warnings = append(warnings, fmt.Sprintf(
			"default transcript disagrees between manifest and tag: manifest=%s CASSINI_TRANSCRIPT_DEFAULT=%s",
			first.ID, taggedID))
	}
	return first, warnings, true
}

// decodeTranscriptBody reverses portable.EncodeTranscriptBody for one
// transcript: it gathers CASSINI_TX_<ID>_PAYLOAD_000..N, concatenates them,
// base64url-decodes, gzip-decompresses, validates the optional sha256/byte-count
// tags, and parses the TranscriptBody JSON. It returns any tag/manifest
// disagreement it resolved alongside the body.
//
// How many chunks to gather comes from the entry's payloadRef, not from
// CASSINI_TX_<ID>_PAYLOAD_CHUNK_COUNT. The manifest is the record and the
// descriptor tags are a copy of it, so once the manifest is decoded the copy no
// longer decides anything: believing the tag meant a file whose two layers had
// drifted by one chunk failed to decode a transcript that was all there, in one
// direction, and silently returned a truncated one in the other. The tag is
// still read, because a disagreement is worth reporting — but it is a warning,
// not a failure.
func decodeTranscriptBody(tags map[string]string, entry portable.TranscriptEntry) (portable.TranscriptBody, []string, error) {
	// payloadRef is authoritative. Its prefix is not reconstructed from the id.
	prefix := strings.TrimSpace(entry.PayloadRef.Prefix)
	var warnings []string
	chunkCount := entry.PayloadRef.ChunkCount
	tagged := parseIntOrZero(metadataTag(tags, prefix+"CHUNK_COUNT"))
	if tagged > 0 && tagged != chunkCount {
		warnings = append(warnings, fmt.Sprintf(
			"transcript %s chunk count disagrees between manifest and tag: payloadRef.chunkCount=%d %sCHUNK_COUNT=%d",
			entry.ID, chunkCount, prefix, tagged))
	}
	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("%s%03d", prefix, idx)
		part, err := chunkValue(tags, key)
		if err != nil {
			return portable.TranscriptBody{}, warnings, err
		}
		if part == "" {
			return portable.TranscriptBody{}, warnings, fmt.Errorf("missing transcript chunk %s", key)
		}
		encoded.WriteString(part)
	}

	compressed, err := decodeCassiniBase64URL(encoded.String())
	if err != nil {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("decode base64url transcript payload: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("open gzip transcript payload: %w", err)
	}
	defer func() { _ = reader.Close() }()
	rawJSON, err := io.ReadAll(reader)
	if err != nil {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("decompress gzip transcript payload: %w", err)
	}

	if entry.PayloadRef.RawBytes != len(rawJSON) {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("transcript raw byte count mismatch: manifest=%d decoded=%d", entry.PayloadRef.RawBytes, len(rawJSON))
	}
	if entry.PayloadRef.GzipBytes != len(compressed) {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("transcript compressed byte count mismatch: manifest=%d decoded=%d", entry.PayloadRef.GzipBytes, len(compressed))
	}
	sum := sha256.Sum256(rawJSON)
	actualSHA := hex.EncodeToString(sum[:])
	if actualSHA != entry.PayloadRef.SHA256 {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("transcript payload sha256 mismatch: manifest=%s decoded=%s", entry.PayloadRef.SHA256, actualSHA)
	}

	for _, mirror := range []struct {
		name string
		want string
	}{
		{name: "RAW_BYTES", want: strconv.Itoa(entry.PayloadRef.RawBytes)},
		{name: "GZIP_BYTES", want: strconv.Itoa(entry.PayloadRef.GzipBytes)},
		{name: "SHA256", want: entry.PayloadRef.SHA256},
		{name: "ENCODING", want: entry.PayloadRef.Encoding},
	} {
		tagged := strings.TrimSpace(metadataTag(tags, prefix+mirror.name))
		if mirror.name == "SHA256" {
			tagged = strings.ToLower(tagged)
		}
		if tagged != "" && mirror.want != "" && tagged != mirror.want {
			warnings = append(warnings, fmt.Sprintf(
				"transcript %s %s%s disagrees with payloadRef: tag=%s payloadRef=%s",
				entry.ID, prefix, mirror.name, tagged, mirror.want,
			))
		}
	}

	var body portable.TranscriptBody
	if err := json.Unmarshal(rawJSON, &body); err != nil {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("parse transcript body JSON: %w", err)
	}
	if entry.Role != portable.RoleDisplay {
		if err := portable.ValidateTranscriptBody(body); err != nil {
			return portable.TranscriptBody{}, warnings, fmt.Errorf("invalid published transcript body: %w", err)
		}
	}
	return body, warnings, nil
}

// WriteTranscriptWordsV1JSON renders an extracted transcript as a
// transcript.words.v1.json-shaped document, preserving each maximal speaker
// turn as a segment.
func WriteTranscriptWordsV1JSON(out io.Writer, extracted ExtractedTranscript) error {
	type wordsV1Word struct {
		Text                 string   `json:"text"`
		StartMS              int64    `json:"startMs"`
		EndMS                int64    `json:"endMs"`
		AttributionGapDB     *float64 `json:"attributionGapDb,omitempty"`
		LowConfidenceSpeaker bool     `json:"lowConfidenceSpeaker,omitempty"`
	}
	type wordsV1Segment struct {
		Speaker string        `json:"speaker,omitempty"`
		Words   []wordsV1Word `json:"words"`
	}
	type wordsV1Doc struct {
		Version   string           `json:"version"`
		Language  string           `json:"language,omitempty"`
		WordCount int              `json:"wordCount"`
		Segments  []wordsV1Segment `json:"segments"`
	}

	segments := make([]wordsV1Segment, 0, 8)
	emitted := 0
	for _, w := range extracted.Words {
		word := wordsV1Word{
			Text:                 w.Text,
			StartMS:              w.StartMS,
			EndMS:                w.EndMS,
			AttributionGapDB:     w.AttributionGapDB,
			LowConfidenceSpeaker: w.LowConfidenceSpeaker,
		}
		if n := len(segments); n > 0 && segments[n-1].Speaker == w.Speaker {
			segments[n-1].Words = append(segments[n-1].Words, word)
		} else {
			segments = append(segments, wordsV1Segment{Speaker: w.Speaker, Words: []wordsV1Word{word}})
		}
		emitted++
	}
	if len(segments) == 0 {
		segments = []wordsV1Segment{{Words: []wordsV1Word{}}}
	}
	doc := wordsV1Doc{
		Version:   "transcript.words.v1",
		Language:  extracted.Language,
		WordCount: emitted,
		Segments:  segments,
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
