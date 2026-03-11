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
	if metadataTag(tags, "CASSINI_FORMAT") == "" {
		printPlainPortableAudio(out, audioSummary, "plain-audio", "")
		return nil
	}
	if !strings.EqualFold(metadataTag(tags, "CASSINI_FORMAT"), portable.Format) {
		printPlainPortableAudio(out, audioSummary, "unknown-cassini-format", fmt.Sprintf("unsupported CASSINI_FORMAT=%s", metadataTag(tags, "CASSINI_FORMAT")))
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
	language := blankDash(firstNonEmpty(manifest.Transcript.Language, manifest.Meeting.Language))
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
		path, title, meetingID, createdAt, len(manifest.Speakers), manifest.Transcript.WordCount, manifest.Meeting.DurationMS, integrity.Status)
	fmt.Fprintf(out, "audio container=%s codec=%s sample_rate=%d channels=%d duration_ms=%d pcm_sha256=%s\n",
		audio.Container, audio.Codec, integrity.SampleRate, integrity.Channels, integrity.DurationMS, blankDash(integrity.PCMHashSHA256))
	fmt.Fprintf(out, "payload encoding=%s schema=%s chunks=%d raw_bytes=%d compressed_bytes=%d sha256=%s language=%s\n",
		payload.Encoding, blankDash(payload.Schema), payload.ChunkCount, payload.RawBytes, payload.CompressedBytes, blankDash(payload.SHA256), language)
	for _, warning := range integrity.Warnings {
		fmt.Fprintf(out, "warning=%s\n", warning)
	}
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
	if manifest.Version != 1 {
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
