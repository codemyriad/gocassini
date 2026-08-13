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
	Status        string
	Warnings      []string
	SampleRate    int
	Channels      int
	SampleCount   int64
	DurationMS    int64
	PCMHashSHA256 string
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
	if !strings.EqualFold(formatTag, portable.Format) && !strings.EqualFold(formatTag, portable.FormatV2) {
		printPlainPortableAudio(out, audioSummary, "unknown-cassini-format", fmt.Sprintf("unsupported CASSINI_FORMAT=%s", formatTag))
		return nil
	}

	payload, manifest, err := decodePortableMeeting(tags)
	if err != nil {
		printPlainPortableAudio(out, audioSummary, "invalid-cassini-metadata", err.Error())
		return nil
	}

	integrity, verifyErr := verifyPortableAudioIntegrity(path, stream, tags, manifest)
	if verifyErr != nil {
		printPortableMeeting(out, path, audioSummary, payload, manifest, portableIntegrityResult{
			Status:   "integrity-unverified",
			Warnings: []string{verifyErr.Error()},
		})
		fmt.Fprintln(out, "fallback=plain-audio")
		return nil
	}

	printPortableMeeting(out, path, audioSummary, payload, manifest, integrity)
	if integrity.Status != "ok" {
		fmt.Fprintln(out, "fallback=plain-audio")
	}
	return nil
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

func printPortableMeeting(out io.Writer, path string, audio portableAudioSummary, payload portablePayloadInfo, manifest portable.Manifest, integrity portableIntegrityResult) {
	title := blankDash(manifest.Meeting.Title)
	meetingID := blankDash(manifest.Meeting.ID)
	createdAt := blankDash(manifest.Meeting.CreatedAtUTC)
	wordCount := manifest.Transcript.WordCount
	language := firstNonEmpty(manifest.Transcript.Language, manifest.Meeting.Language)
	if manifest.Version == 2 {
		// v2 stores transcript bodies in separate chunk sets; the main payload
		// only carries descriptors. Sum word counts from those descriptors and
		// fall back to the default raw-ASR transcript's language tag.
		wordCount = 0
		for _, entry := range manifest.Transcripts {
			wordCount += entry.WordCount
		}
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
	fmt.Fprintf(out, "audio container=%s codec=%s sample_rate=%d channels=%d duration_ms=%d pcm_sha256=%s\n",
		audio.Container, audio.Codec, integrity.SampleRate, integrity.Channels, integrity.DurationMS, blankDash(integrity.PCMHashSHA256))
	fmt.Fprintf(out, "payload encoding=%s schema=%s chunks=%d raw_bytes=%d compressed_bytes=%d sha256=%s language=%s\n",
		payload.Encoding, blankDash(payload.Schema), payload.ChunkCount, payload.RawBytes, payload.CompressedBytes, blankDash(payload.SHA256), blankDash(language))
	if manifest.Version == 2 {
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
	for _, warning := range integrity.Warnings {
		fmt.Fprintf(out, "warning=%s\n", warning)
	}
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

	compressed, err := base64.RawURLEncoding.DecodeString(encoded.String())
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
	if manifest.Version != 1 && manifest.Version != 2 {
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

	pcmBytes, err := decodeAudioPCM(path, sampleRate, channels)
	if err != nil {
		return portableIntegrityResult{}, err
	}
	sum := sha256.Sum256(pcmBytes)
	pcmSHA := hex.EncodeToString(sum[:])
	sampleCount := int64(len(pcmBytes) / (2 * channels))
	durationMS := int64(sampleCount * 1000 / int64(sampleRate))

	warnings := []string{}
	status := "ok"
	expect := strings.ToLower(strings.TrimSpace(manifest.Integrity.PCMSHA256))
	if expect == "" {
		expect = strings.ToLower(strings.TrimSpace(metadataTag(tags, "CASSINI_AUDIO_PCM_SHA256")))
	}
	if expect != "" && expect != pcmSHA {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("decoded PCM sha256 mismatch: manifest=%s actual=%s", expect, pcmSHA))
	}
	if manifest.Integrity.SampleRate > 0 && manifest.Integrity.SampleRate != sampleRate {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("sample rate mismatch: manifest=%d actual=%d", manifest.Integrity.SampleRate, sampleRate))
	}
	if manifest.Integrity.Channels > 0 && manifest.Integrity.Channels != channels {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("channel count mismatch: manifest=%d actual=%d", manifest.Integrity.Channels, channels))
	}
	if manifest.Integrity.SampleCount > 0 && manifest.Integrity.SampleCount != sampleCount {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("sample count mismatch: manifest=%d actual=%d", manifest.Integrity.SampleCount, sampleCount))
	}
	if manifest.Integrity.DurationMS > 0 && deltaAbs(manifest.Integrity.DurationMS, durationMS) > 1 {
		status = "stale-audio"
		warnings = append(warnings, fmt.Sprintf("duration mismatch: manifest=%d actual=%d", manifest.Integrity.DurationMS, durationMS))
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

func decodeAudioPCM(path string, sampleRate int, channels int) ([]byte, error) {
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
		return nil, fmt.Errorf("ffmpeg decode portable audio: %w", err)
	}
	return raw, nil
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
			Text:    item.Text,
			StartMS: item.StartMS,
			EndMS:   item.EndMS,
			Speaker: item.Speaker,
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
// from a decoded v2 manifest. Preference: the entry named by the
// CASSINI_TRANSCRIPT_DEFAULT tag, then any entry flagged Default, then the
// first raw transcript entry.
func defaultWordsTranscriptEntry(tags map[string]string, manifest portable.Manifest) (portable.TranscriptEntry, bool) {
	if len(manifest.Transcripts) == 0 {
		return portable.TranscriptEntry{}, false
	}
	if defaultID := strings.TrimSpace(metadataTag(tags, "CASSINI_TRANSCRIPT_DEFAULT")); defaultID != "" {
		for _, entry := range manifest.Transcripts {
			if entry.ID == defaultID {
				return entry, true
			}
		}
	}
	for _, entry := range manifest.Transcripts {
		if entry.Default {
			return entry, true
		}
	}
	return manifest.Transcripts[0], true
}

// decodeTranscriptBody reverses portable.EncodeTranscriptBody for one v2
// transcript: it reads CASSINI_TX_<ID>_PAYLOAD_CHUNK_COUNT +
// CASSINI_TX_<ID>_PAYLOAD_000..N, concatenates them, base64url-decodes,
// gzip-decompresses, validates the optional sha256/byte-count tags, and parses
// the TranscriptBody JSON.
func decodeTranscriptBody(tags map[string]string, id string) (portable.TranscriptBody, error) {
	prefix := portable.TranscriptIDToTagPrefix(id)
	chunkCount := parseIntOrZero(metadataTag(tags, prefix+"CHUNK_COUNT"))
	if chunkCount <= 0 {
		return portable.TranscriptBody{}, fmt.Errorf("missing or invalid %sCHUNK_COUNT", prefix)
	}
	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("%s%03d", prefix, idx)
		part := metadataTag(tags, key)
		if part == "" {
			return portable.TranscriptBody{}, fmt.Errorf("missing transcript chunk %s", key)
		}
		encoded.WriteString(part)
	}

	compressed, err := base64.RawURLEncoding.DecodeString(encoded.String())
	if err != nil {
		return portable.TranscriptBody{}, fmt.Errorf("decode base64url transcript payload: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return portable.TranscriptBody{}, fmt.Errorf("open gzip transcript payload: %w", err)
	}
	defer func() { _ = reader.Close() }()
	rawJSON, err := io.ReadAll(reader)
	if err != nil {
		return portable.TranscriptBody{}, fmt.Errorf("decompress gzip transcript payload: %w", err)
	}

	if declared := parseIntOrZero(metadataTag(tags, prefix+"RAW_BYTES")); declared > 0 && declared != len(rawJSON) {
		return portable.TranscriptBody{}, fmt.Errorf("transcript raw byte count mismatch: tags=%d decoded=%d", declared, len(rawJSON))
	}
	if declared := parseIntOrZero(metadataTag(tags, prefix+"GZIP_BYTES")); declared > 0 && declared != len(compressed) {
		return portable.TranscriptBody{}, fmt.Errorf("transcript compressed byte count mismatch: tags=%d decoded=%d", declared, len(compressed))
	}
	if declared := strings.TrimSpace(strings.ToLower(metadataTag(tags, prefix+"SHA256"))); declared != "" {
		sum := sha256.Sum256(rawJSON)
		if hex.EncodeToString(sum[:]) != declared {
			return portable.TranscriptBody{}, fmt.Errorf("transcript payload sha256 mismatch")
		}
	}

	var body portable.TranscriptBody
	if err := json.Unmarshal(rawJSON, &body); err != nil {
		return portable.TranscriptBody{}, fmt.Errorf("parse transcript body JSON: %w", err)
	}
	return body, nil
}

// WriteTranscriptWordsV1JSON renders an extracted transcript as a
// transcript.words.v1.json-shaped document (one segment carrying the ordered
// words) so existing consumers — including the Talk roundtrip phase-9 check —
// can read it without bespoke parsing.
func WriteTranscriptWordsV1JSON(out io.Writer, extracted ExtractedTranscript) error {
	type wordsV1Word struct {
		Text    string `json:"text"`
		StartMS int64  `json:"startMs"`
		EndMS   int64  `json:"endMs"`
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
		words = append(words, wordsV1Word{Text: w.Text, StartMS: w.StartMS, EndMS: w.EndMS})
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
