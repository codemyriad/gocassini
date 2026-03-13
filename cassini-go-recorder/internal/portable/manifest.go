package portable

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	Format                  = "org.cassini.portable-meeting/1"
	Profile                 = "ogg-opus"
	PayloadMIME             = "application/vnd.cassini.portable-meeting+json"
	PayloadEncoding         = "base64url+gzip+utf8json"
	PayloadSchema           = "https://cassini.local/spec/cassini-portable-meeting-manifest-v1.schema.json"
	AudioPCMFormat          = "s16le"
	AudioMatchPolicy        = "exact-pcm"
	DefaultPayloadChunkSize = 4096
	Description             = "Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON."
	DecodeHint              = "Concatenate CASSINI_PAYLOAD_000..N, base64url decode, gzip decompress, parse UTF-8 JSON."
)

type Manifest struct {
	Kind               string           `json:"kind"`
	Version            int              `json:"version"`
	Profile            string           `json:"profile"`
	Meeting            Meeting          `json:"meeting"`
	Audio              Audio            `json:"audio"`
	Integrity          Integrity        `json:"integrity"`
	Speakers           []Speaker        `json:"speakers"`
	Transcript         Transcript       `json:"transcript"`
	Provenance         *Provenance      `json:"provenance,omitempty"`
	ReadableTranscript map[string]any   `json:"readableTranscript,omitempty"`
	Chapters           []Chapter        `json:"chapters,omitempty"`
	Summary            map[string]any   `json:"summary,omitempty"`
	Attachments        []map[string]any `json:"attachments,omitempty"`
}

type Provenance struct {
	SpeechToText      *ProcessingStep `json:"speechToText,omitempty"`
	ReadableCleanup   *ProcessingStep `json:"readableCleanup,omitempty"`
	DisplayTranscript *ProcessingStep `json:"displayTranscript,omitempty"`
}

type ProcessingStep struct {
	Backend  string `json:"backend,omitempty"`
	Engine   string `json:"engine,omitempty"`
	Model    string `json:"model,omitempty"`
	Device   string `json:"device,omitempty"`
	Language string `json:"language,omitempty"`
	BaseURL  string `json:"baseUrl,omitempty"`
	Host     string `json:"host,omitempty"`
	Source   string `json:"source,omitempty"`
	Version  string `json:"version,omitempty"`
}

type Meeting struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	CreatedAtUTC string `json:"createdAtUtc"`
	DurationMS   int64  `json:"durationMs"`
	Language     string `json:"language,omitempty"`
	Summary      string `json:"summary,omitempty"`
}

type Audio struct {
	Container   string `json:"container"`
	Codec       string `json:"codec"`
	SampleRate  int    `json:"sampleRate"`
	Channels    int    `json:"channels"`
	SampleCount int64  `json:"sampleCount"`
	DurationMS  int64  `json:"durationMs"`
}

type Integrity struct {
	MatchPolicy     string `json:"matchPolicy"`
	PCMFormat       string `json:"pcmFormat"`
	PCMSHA256       string `json:"pcmSha256"`
	ContainerSHA256 string `json:"containerSha256,omitempty"`
	SampleRate      int    `json:"sampleRate"`
	Channels        int    `json:"channels"`
	SampleCount     int64  `json:"sampleCount"`
	DurationMS      int64  `json:"durationMs"`
}

type Speaker struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Transcript struct {
	Format    string           `json:"format"`
	Language  string           `json:"language,omitempty"`
	WordCount int              `json:"wordCount"`
	Items     []TranscriptItem `json:"items"`
}

type TranscriptItem struct {
	Speaker string `json:"speaker"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
	Text    string `json:"text"`
}

type Chapter struct {
	Title   string `json:"title"`
	StartMS int64  `json:"startMs"`
	EndMS   int64  `json:"endMs"`
}

type EncodedPayload struct {
	JSON            []byte
	GZIP            []byte
	Base64URL       string
	SHA256          string
	RawBytes        int
	CompressedBytes int
	Chunks          []string
}

func NormalizeManifest(manifest Manifest) Manifest {
	manifest.Kind = "cassini-portable-meeting"
	manifest.Version = 1
	manifest.Profile = Profile
	manifest.Audio.Container = "ogg"
	manifest.Audio.Codec = "opus"
	manifest.Integrity.MatchPolicy = AudioMatchPolicy
	manifest.Integrity.PCMFormat = AudioPCMFormat
	if manifest.Transcript.Format == "" {
		manifest.Transcript.Format = "cassini.words.v1"
	}
	return manifest
}

func EncodeManifest(manifest Manifest, chunkSize int) (EncodedPayload, error) {
	manifest = NormalizeManifest(manifest)
	rawJSON, err := json.Marshal(manifest)
	if err != nil {
		return EncodedPayload{}, fmt.Errorf("marshal portable meeting manifest: %w", err)
	}

	var compressed bytes.Buffer
	gzw := gzip.NewWriter(&compressed)
	if _, err := gzw.Write(rawJSON); err != nil {
		return EncodedPayload{}, fmt.Errorf("gzip portable meeting manifest: %w", err)
	}
	if err := gzw.Close(); err != nil {
		return EncodedPayload{}, fmt.Errorf("close gzip portable meeting manifest: %w", err)
	}

	sum := sha256.Sum256(rawJSON)
	encoded := base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	if chunkSize <= 0 {
		chunkSize = DefaultPayloadChunkSize
	}

	return EncodedPayload{
		JSON:            rawJSON,
		GZIP:            compressed.Bytes(),
		Base64URL:       encoded,
		SHA256:          hex.EncodeToString(sum[:]),
		RawBytes:        len(rawJSON),
		CompressedBytes: compressed.Len(),
		Chunks:          ChunkString(encoded, chunkSize),
	}, nil
}

func ChunkString(value string, size int) []string {
	if len(value) == 0 {
		return nil
	}
	if size <= 0 {
		size = DefaultPayloadChunkSize
	}
	chunks := make([]string, 0, (len(value)+size-1)/size)
	for len(value) > 0 {
		if len(value) <= size {
			chunks = append(chunks, value)
			break
		}
		chunks = append(chunks, value[:size])
		value = value[size:]
	}
	return chunks
}

func MeetingIDFromPCMHash(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return ""
	}
	return "mtg_" + hash
}

func BuildOpusTags(manifest Manifest, payload EncodedPayload) map[string]string {
	manifest = NormalizeManifest(manifest)

	tags := map[string]string{
		"TITLE":                       manifest.Meeting.Title,
		"DATE":                        manifest.Meeting.CreatedAtUTC,
		"DESCRIPTION":                 Description,
		"ENCODER":                     "Cassini",
		"CASSINI_FORMAT":              Format,
		"CASSINI_PROFILE":             Profile,
		"CASSINI_PAYLOAD_MIME":        PayloadMIME,
		"CASSINI_PAYLOAD_ENCODING":    PayloadEncoding,
		"CASSINI_PAYLOAD_SCHEMA":      PayloadSchema,
		"CASSINI_PAYLOAD_CHUNK_COUNT": fmt.Sprintf("%d", len(payload.Chunks)),
		"CASSINI_PAYLOAD_SHA256":      payload.SHA256,
		"CASSINI_PAYLOAD_RAW_BYTES":   fmt.Sprintf("%d", payload.RawBytes),
		"CASSINI_PAYLOAD_GZIP_BYTES":  fmt.Sprintf("%d", payload.CompressedBytes),
		"CASSINI_AUDIO_PCM_FORMAT":    AudioPCMFormat,
		"CASSINI_AUDIO_SAMPLE_RATE":   fmt.Sprintf("%d", manifest.Audio.SampleRate),
		"CASSINI_AUDIO_CHANNELS":      fmt.Sprintf("%d", manifest.Audio.Channels),
		"CASSINI_AUDIO_SAMPLE_COUNT":  fmt.Sprintf("%d", manifest.Audio.SampleCount),
		"CASSINI_AUDIO_DURATION_MS":   fmt.Sprintf("%d", manifest.Audio.DurationMS),
		"CASSINI_AUDIO_PCM_SHA256":    manifest.Integrity.PCMSHA256,
		"CASSINI_AUDIO_MATCH_POLICY":  AudioMatchPolicy,
		"CASSINI_DECODE_HINT":         DecodeHint,
		"CASSINI_MEETING_ID":          manifest.Meeting.ID,
		"CASSINI_CREATED_AT":          manifest.Meeting.CreatedAtUTC,
		"CASSINI_SPEAKER_COUNT":       fmt.Sprintf("%d", len(manifest.Speakers)),
		"CASSINI_WORD_COUNT":          fmt.Sprintf("%d", manifest.Transcript.WordCount),
	}

	language := firstNonEmpty(manifest.Transcript.Language, manifest.Meeting.Language)
	if language != "" {
		tags["LANGUAGE"] = language
		tags["CASSINI_TRANSCRIPT_LANGUAGE"] = language
	}
	if manifest.Provenance != nil {
		applyProcessingStepTags(tags, "CASSINI_STT", manifest.Provenance.SpeechToText)
		applyProcessingStepTags(tags, "CASSINI_READABLE", manifest.Provenance.ReadableCleanup)
	}

	for idx, chunk := range payload.Chunks {
		tags[fmt.Sprintf("CASSINI_PAYLOAD_%03d", idx)] = chunk
	}
	return tags
}

func applyProcessingStepTags(tags map[string]string, prefix string, step *ProcessingStep) {
	if step == nil {
		return
	}
	if value := firstNonEmpty(step.Backend); value != "" {
		tags[prefix+"_BACKEND"] = value
	}
	if value := firstNonEmpty(step.Engine); value != "" {
		tags[prefix+"_ENGINE"] = value
	}
	if value := firstNonEmpty(step.Model); value != "" {
		tags[prefix+"_MODEL"] = value
	}
	if value := firstNonEmpty(step.Device); value != "" {
		tags[prefix+"_DEVICE"] = value
	}
	if value := firstNonEmpty(step.Language); value != "" {
		tags[prefix+"_LANGUAGE"] = value
	}
	if value := firstNonEmpty(step.Host); value != "" {
		tags[prefix+"_HOST"] = value
	}
	if value := firstNonEmpty(step.Source); value != "" {
		tags[prefix+"_SOURCE"] = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
