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
	PCMHashSHA256  string
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
	if !knownPortableFormatTag(formatTag) {
		printPlainPortableAudio(out, audioSummary, "unknown-cassini-format", fmt.Sprintf("unsupported CASSINI_FORMAT=%s", formatTag))
		return nil
	}

	payload, manifest, err := decodePortableMeeting(tags)
	if err != nil {
		printPlainPortableAudio(out, audioSummary, "invalid-cassini-metadata", err.Error())
		return nil
	}

	bodies := readPortableTranscriptBodies(tags, manifest)

	integrity, verifyErr := verifyPortableAudioIntegrity(path, stream, tags, manifest)
	if verifyErr != nil {
		integrity = portableIntegrityResult{
			Status:   "integrity-unverified",
			Warnings: []string{verifyErr.Error()},
		}
	}
	audioStatus := integrity.Status
	// A transcript body that cannot be read makes that transcript unavailable,
	// not the file: the state describes the manifest and the audio, and the
	// meeting, the speakers and the other transcripts are still good. The
	// warning names the transcript, `words=` counts only what was read, and
	// the command still exits non-zero so a script notices.

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
	Warnings   []string
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

// readPortableTranscriptBodies decodes every transcript body a
// multi-transcript file declares. A draft-1 file declares none: its words are
// inline in the main manifest.
//
// inspect used to print the manifest index's declared wordCount and label it
// `words=`, which meant the one number it reported about the transcript was the
// only one it had not checked: a file whose chunk set had a hole in it still
// printed `cassini=ok words=900` while the transcript was unreachable. Reading
// the bodies costs one gzip inflate each against metadata already in memory,
// and it is the only way `words=` can mean anything.
func readPortableTranscriptBodies(tags map[string]string, manifest portable.Manifest) portableTranscriptBodies {
	bodies := portableTranscriptBodies{WordCounts: map[string]int{}}
	if !manifest.IsMultiTranscript() {
		return bodies
	}
	if entry, warnings, ok := defaultWordsTranscriptEntry(tags, manifest); ok {
		bodies.DefaultID = entry.ID
		bodies.Warnings = append(bodies.Warnings, warnings...)
	}
	for _, entry := range manifest.Transcripts {
		body, warnings, err := decodeTranscriptBody(tags, entry)
		bodies.Warnings = append(bodies.Warnings, warnings...)
		if err != nil {
			bodies.Unreadable = append(bodies.Unreadable, entry.ID)
			bodies.Warnings = append(bodies.Warnings, fmt.Sprintf("transcript %s body could not be read: %v", entry.ID, err))
			continue
		}
		count := body.WordCount
		if count == 0 {
			count = len(body.Items)
		}
		bodies.WordCounts[entry.ID] = count
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
	wordCount := manifest.Transcript.WordCount
	language := firstNonEmpty(manifest.Transcript.Language, manifest.Meeting.Language)
	if manifest.IsMultiTranscript() {
		// A multi-transcript file stores its bodies in separate chunk sets; the main payload
		// only carries descriptors. Two things follow for the meeting's word
		// count. It is the words that came back out of a chunk set, not the
		// ones a descriptor claims, so a file whose bodies are unreachable
		// reports nothing read rather than the index's figure. And it is the
		// default transcript's alone: a second raw transcript is another pass
		// over the same speech, so adding the two together describes no meeting
		// that ever happened — three words spoken twice were reported as six.
		// The per-transcript lines below still carry every transcript.
		wordCount = bodies.WordCounts[bodies.DefaultID]
		if language == "" {
			for _, entry := range manifest.Transcripts {
				if entry.Default && entry.Language != "" {
					language = entry.Language
					break
				}
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
	if integrity.PCMHashSHA256 != "" {
		fmt.Fprintf(out, " pcm_sha256=%s", integrity.PCMHashSHA256)
	}
	fmt.Fprintln(out)
	fmt.Fprintf(out, "payload encoding=%s schema=%s chunks=%d raw_bytes=%d compressed_bytes=%d sha256=%s language=%s\n",
		payload.Encoding, blankDash(payload.Schema), payload.ChunkCount, payload.RawBytes, payload.CompressedBytes, blankDash(payload.SHA256), blankDash(language))
	printPortableOrigin(out, manifest.Meeting)
	if manifest.IsMultiTranscript() {
		for _, entry := range manifest.Transcripts {
			printPortableTranscriptEntry(out, "transcript", entry)
		}
		for _, entry := range manifest.ReadableTranscripts {
			printPortableTranscriptEntry(out, "readable_transcript", entry)
		}
	}
	if manifest.Provenance != nil {
		printProcessingStep(out, "speech_to_text", manifest.Provenance.SpeechToText)
		printProcessingStep(out, "readable_cleanup", manifest.Provenance.ReadableCleanup)
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
	if meeting.RoomID == "" && meeting.RoomName == "" && meeting.JobID == "" && meeting.AttemptNumber == 0 {
		return
	}
	attempt := "-"
	if meeting.AttemptNumber > 0 {
		attempt = fmt.Sprintf("%d", meeting.AttemptNumber)
	}
	// room_name is shown because a file may still carry a legacy one; it is no
	// longer written, and the catalog is where a room's current name lives.
	fmt.Fprintf(out, "origin room_id=%s room_name=%s job_id=%s attempt=%s\n",
		blankDash(meeting.RoomID), blankDash(meeting.RoomName), blankDash(meeting.JobID), attempt)
}

func printPortableTranscriptEntry(out io.Writer, label string, entry portable.TranscriptEntry) {
	defaultMarker := "no"
	if entry.Default {
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

// knownPortableFormatTag reports whether CASSINI_FORMAT names a portable
// meeting shape this build can read: the published format, or any of the three
// pre-publication drafts. The published format and draft 1 share a string, so
// this answers "is it one of ours", not "which one is it" — what a file
// actually contains is settled by Manifest.IsMultiTranscript.
func knownPortableFormatTag(formatTag string) bool {
	for _, known := range []string{portable.Format, portable.FormatDraft1, portable.FormatDraft2, portable.FormatDraft3} {
		if strings.EqualFold(formatTag, known) {
			return true
		}
	}
	return false
}

// knownPortableWireVersion reports whether the manifest's own version number is
// one this build reads. Version 1 covers both the published format and draft 1.
func knownPortableWireVersion(version int) bool {
	switch version {
	case portable.WireVersion, portable.Draft2WireVersion, portable.Draft3WireVersion:
		return true
	}
	return false
}

func decodePortableMeeting(tags map[string]string) (portablePayloadInfo, portable.Manifest, error) {
	chunkCount := parseIntOrZero(metadataTag(tags, "CASSINI_PAYLOAD_CHUNK_COUNT"))
	if chunkCount <= 0 {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT")
	}
	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("CASSINI_PAYLOAD_%03d", idx)
		part := metadataTag(tags, key)
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

	if declared := parseIntOrZero(metadataTag(tags, "CASSINI_PAYLOAD_RAW_BYTES")); declared > 0 && declared != len(rawJSON) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("payload raw byte count mismatch: tags=%d decoded=%d", declared, len(rawJSON))
	}
	if declared := parseIntOrZero(metadataTag(tags, "CASSINI_PAYLOAD_GZIP_BYTES", "CASSINI_PAYLOAD_XZ_BYTES")); declared > 0 && declared != len(compressed) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("payload compressed byte count mismatch: tags=%d decoded=%d", declared, len(compressed))
	}
	if declared := strings.TrimSpace(strings.ToLower(metadataTag(tags, "CASSINI_PAYLOAD_SHA256"))); declared != "" {
		sum := sha256.Sum256(rawJSON)
		if hex.EncodeToString(sum[:]) != declared {
			return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("payload sha256 mismatch")
		}
	}

	var manifest portable.Manifest
	if err := json.Unmarshal(rawJSON, &manifest); err != nil {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("parse Cassini payload JSON: %w", err)
	}
	if manifest.Kind != "cassini-portable-meeting" {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("unexpected payload kind %q", manifest.Kind)
	}
	if !knownPortableWireVersion(manifest.Version) {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("unsupported payload version %d", manifest.Version)
	}
	if manifest.Profile != portable.Profile {
		return portablePayloadInfo{}, portable.Manifest{}, fmt.Errorf("unsupported payload profile %q", manifest.Profile)
	}

	return portablePayloadInfo{
		Encoding:        firstNonEmpty(metadataTag(tags, "CASSINI_PAYLOAD_ENCODING"), "base64url+gzip+utf8json"),
		Schema:          metadataTag(tags, "CASSINI_PAYLOAD_SCHEMA"),
		ChunkCount:      chunkCount,
		RawBytes:        len(rawJSON),
		CompressedBytes: len(compressed),
		SHA256:          strings.ToLower(metadataTag(tags, "CASSINI_PAYLOAD_SHA256")),
		Compressed:      compressed,
		JSON:            rawJSON,
	}, manifest, nil
}

func verifyPortableAudioIntegrity(path string, stream probedPortableAudioStream, tags map[string]string, manifest portable.Manifest) (portableIntegrityResult, error) {
	manifestPolicy := strings.ToLower(strings.TrimSpace(manifest.Integrity.MatchPolicy))
	tagPolicy := strings.ToLower(strings.TrimSpace(metadataTag(tags, "CASSINI_AUDIO_MATCH_POLICY")))
	if manifestPolicy != "" && tagPolicy != "" && manifestPolicy != tagPolicy {
		return portableIntegrityResult{}, fmt.Errorf("audio integrity policy mismatch: manifest=%q tag=%q", manifestPolicy, tagPolicy)
	}
	policy := manifestPolicy
	if policy == "" {
		policy = tagPolicy
	}
	if policy == "" {
		if manifest.Integrity.OpusSHA256 != "" || metadataTag(tags, "CASSINI_AUDIO_OPUS_SHA256") != "" {
			policy = portable.AudioMatchPolicy
		} else {
			policy = portable.LegacyAudioMatchPolicyPCM
		}
	}

	switch policy {
	case portable.AudioMatchPolicy:
		return verifyPortableOpusIntegrity(path, tags, manifest)
	case portable.LegacyAudioMatchPolicyPCM:
		return verifyPortableLegacyPCMIntegrity(path, stream, tags, manifest)
	default:
		return portableIntegrityResult{}, fmt.Errorf("unsupported audio integrity matchPolicy %q", policy)
	}
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

func verifyPortableLegacyPCMIntegrity(path string, stream probedPortableAudioStream, tags map[string]string, manifest portable.Manifest) (portableIntegrityResult, error) {
	if manifest.Integrity.PCMFormat != "" && !strings.EqualFold(manifest.Integrity.PCMFormat, "s16le") {
		return portableIntegrityResult{}, fmt.Errorf("unsupported integrity pcmFormat %q", manifest.Integrity.PCMFormat)
	}

	sampleRate := parseIntOrZero(stream.SampleRate)
	if sampleRate == 0 {
		sampleRate = parseIntOrZero(metadataTag(tags, "CASSINI_AUDIO_SAMPLE_RATE"))
	}
	channels := stream.Channels
	if channels == 0 {
		channels = parseIntOrZero(metadataTag(tags, "CASSINI_AUDIO_CHANNELS"))
	}
	if sampleRate <= 0 || channels <= 0 {
		return portableIntegrityResult{}, fmt.Errorf("missing actual audio sample rate or channel count")
	}

	pcmSHA, pcmByteCount, err := hashDecodedAudioPCM(path, sampleRate, channels)
	if err != nil {
		return portableIntegrityResult{}, err
	}
	bytesPerSampleFrame := int64(2 * channels)
	if pcmByteCount%bytesPerSampleFrame != 0 {
		return portableIntegrityResult{}, fmt.Errorf("ffmpeg produced %d PCM bytes, not a whole number of %d-byte frames", pcmByteCount, bytesPerSampleFrame)
	}
	sampleCount := pcmByteCount / bytesPerSampleFrame
	durationMS := int64(sampleCount * 1000 / int64(sampleRate))

	status, warnings := comparePortableAudioShape(manifest.Integrity, sampleRate, channels, sampleCount, durationMS)
	expect := strings.ToLower(strings.TrimSpace(manifest.Integrity.PCMSHA256))
	tagExpect := strings.ToLower(strings.TrimSpace(metadataTag(tags, "CASSINI_AUDIO_PCM_SHA256")))
	if expect != "" && tagExpect != "" && expect != tagExpect {
		return portableIntegrityResult{}, fmt.Errorf("decoded PCM sha256 mismatch between manifest and tag")
	}
	if expect == "" {
		expect = tagExpect
	}
	if expect == "" {
		return portableIntegrityResult{}, fmt.Errorf("decoded PCM integrity policy has no pcmSha256")
	}
	if expect != pcmSHA {
		status = "stale-audio"
		warnings = append([]string{fmt.Sprintf("decoded PCM sha256 mismatch: manifest=%s actual=%s", expect, pcmSHA)}, warnings...)
	}
	return portableIntegrityResult{
		Status:        status,
		Warnings:      warnings,
		SampleRate:    sampleRate,
		Channels:      channels,
		SampleCount:   sampleCount,
		DurationMS:    durationMS,
		PCMHashSHA256: pcmSHA,
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

func hashDecodedAudioPCM(path string, sampleRate int, channels int) (string, int64, error) {
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
		return "", 0, fmt.Errorf("open ffmpeg portable PCM output: %w", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", 0, fmt.Errorf("start ffmpeg portable PCM decode: %w", err)
	}
	digest := sha256.New()
	byteCount, copyErr := io.Copy(digest, stdout)
	waitErr := cmd.Wait()
	if copyErr != nil {
		return "", 0, fmt.Errorf("read ffmpeg portable PCM output: %w", copyErr)
	}
	if waitErr != nil {
		return "", 0, fmt.Errorf("ffmpeg decode portable audio: %w: %s", waitErr, strings.TrimSpace(stderr.String()))
	}
	return hex.EncodeToString(digest.Sum(nil)), byteCount, nil
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
// finds the default transcript id from the decoded manifest (v2) or the inline
// transcript (v1), gathers that transcript's CASSINI_TX_<ID>_PAYLOAD_* chunk
// set, concatenates + base64url-decodes + gzip-decompresses + parses the
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
// out of a decoded v2/v3 manifest, and reports any disagreement it had to
// resolve on the way.
//
// The manifest decides. An entry's `default` flag is the record;
// CASSINI_TRANSCRIPT_DEFAULT is the copy that exists so a reader holding only
// ffprobe can see something before it decodes anything, and once the manifest
// is decoded the copy is spent. This used to run the other way round, which
// meant a tool that rewrote the tag and not the manifest silently moved which
// transcript every consumer read.
//
// Preference: the entry flagged Default, then the entry the tag names, then the
// first raw transcript entry.
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
	// No flagged default: array order decides. The tag is a copy and never
	// the resolver, so a tag naming a different entry is only a warning.
	first := manifest.Transcripts[0]
	var warnings []string
	if taggedID != "" && taggedID != first.ID {
		warnings = append(warnings, fmt.Sprintf(
			"default transcript disagrees between manifest and tag: manifest=%s CASSINI_TRANSCRIPT_DEFAULT=%s",
			first.ID, taggedID))
	}
	return first, warnings, true
}

// decodeTranscriptBody reverses portable.EncodeTranscriptBody for one v2/v3
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
	prefix := portable.TranscriptIDToTagPrefix(entry.ID)
	var warnings []string
	chunkCount := entry.PayloadRef.ChunkCount
	tagged := parseIntOrZero(metadataTag(tags, prefix+"CHUNK_COUNT"))
	switch {
	case chunkCount <= 0:
		// A manifest with no payloadRef.chunkCount leaves the tag as the only
		// count there is; a file like that is already outside the format, so
		// read what it offers rather than refusing it outright.
		chunkCount = tagged
	case tagged > 0 && tagged != chunkCount:
		warnings = append(warnings, fmt.Sprintf(
			"transcript %s chunk count disagrees between manifest and tag: payloadRef.chunkCount=%d %sCHUNK_COUNT=%d",
			entry.ID, chunkCount, prefix, tagged))
	}
	if chunkCount <= 0 {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("missing or invalid %sCHUNK_COUNT", prefix)
	}
	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("%s%03d", prefix, idx)
		part := metadataTag(tags, key)
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

	if declared := parseIntOrZero(metadataTag(tags, prefix+"RAW_BYTES")); declared > 0 && declared != len(rawJSON) {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("transcript raw byte count mismatch: tags=%d decoded=%d", declared, len(rawJSON))
	}
	if declared := parseIntOrZero(metadataTag(tags, prefix+"GZIP_BYTES")); declared > 0 && declared != len(compressed) {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("transcript compressed byte count mismatch: tags=%d decoded=%d", declared, len(compressed))
	}
	if declared := strings.TrimSpace(strings.ToLower(metadataTag(tags, prefix+"SHA256"))); declared != "" {
		sum := sha256.Sum256(rawJSON)
		if hex.EncodeToString(sum[:]) != declared {
			return portable.TranscriptBody{}, warnings, fmt.Errorf("transcript payload sha256 mismatch")
		}
	}

	var body portable.TranscriptBody
	if err := json.Unmarshal(rawJSON, &body); err != nil {
		return portable.TranscriptBody{}, warnings, fmt.Errorf("parse transcript body JSON: %w", err)
	}
	return body, warnings, nil
}

// WriteTranscriptWordsV1JSON renders an extracted transcript as a
// transcript.words.v1.json-shaped document (one segment carrying the ordered
// words) so existing consumers — including the Talk roundtrip phase-9 check —
// can read it without bespoke parsing.
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

	words := make([]wordsV1Word, 0, len(extracted.Words))
	for _, w := range extracted.Words {
		words = append(words, wordsV1Word{
			Text:                 w.Text,
			StartMS:              w.StartMS,
			EndMS:                w.EndMS,
			AttributionGapDB:     w.AttributionGapDB,
			LowConfidenceSpeaker: w.LowConfidenceSpeaker,
		})
	}
	doc := wordsV1Doc{
		Version:   "transcript.words.v1",
		Language:  extracted.Language,
		WordCount: extracted.WordCount,
		Segments:  []wordsV1Segment{{Words: words}},
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}
