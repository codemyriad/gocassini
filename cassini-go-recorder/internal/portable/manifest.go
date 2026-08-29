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
	FormatV2                = "org.cassini.portable-meeting/2"
	Profile                 = "ogg-opus"
	PayloadMIME             = "application/vnd.cassini.portable-meeting+json"
	PayloadMIMEV2           = "application/vnd.cassini.portable-meeting+json"
	PayloadEncoding         = "base64url+gzip+utf8json"
	PayloadSchema           = "https://cassini.local/spec/cassini-portable-meeting-manifest-v1.schema.json"
	PayloadSchemaV2         = "https://cassini.local/spec/cassini-portable-meeting-manifest-v2.schema.json"
	AudioPCMFormat          = "s16le"
	AudioMatchPolicy        = "exact-pcm"
	DefaultPayloadChunkSize = 4096
	Description             = "Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON."
	DecodeHint              = "Concatenate CASSINI_PAYLOAD_000..N, base64url decode, gzip decompress, parse UTF-8 JSON."
	DecodeHintV2            = "Concatenate CASSINI_PAYLOAD_000..N for the manifest; for a transcript body concatenate CASSINI_TX_<ID>_PAYLOAD_000..N. Each chunk set: base64url decode, gzip decompress, parse UTF-8 JSON."

	TranscriptBodyMIMEWords    = "application/vnd.cassini.transcript-words+json"
	TranscriptBodyMIMEReadable = "application/vnd.cassini.transcript-readable+json"

	RoleRawASR          = "raw-asr"
	RoleReadableCleanup = "readable-cleanup"
	RoleDisplay         = "display"
	RoleHumanCorrected  = "human-corrected"
	RoleTranslation     = "translation"
)

type Manifest struct {
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	Profile   string    `json:"profile"`
	Meeting   Meeting   `json:"meeting"`
	Audio     Audio     `json:"audio"`
	Integrity Integrity `json:"integrity"`
	Speakers  []Speaker `json:"speakers"`
	// v1 wire: required field; v2 wire never includes it (omitempty on a
	// non-pointer struct is a no-op, so v1 writes always render this — fine).
	Transcript Transcript `json:"transcript"`
	// V2-only descriptor arrays. Populated when reading v2 files; ignored on
	// v1 writes. v2 writes happen through manifestV2Wire (see manifest_v2.go),
	// so these fields never appear in v2-produced JSON either.
	Transcripts         []TranscriptEntry `json:"transcripts,omitempty"`
	ReadableTranscripts []TranscriptEntry `json:"readableTranscripts,omitempty"`
	Provenance          *Provenance       `json:"provenance,omitempty"`
	ReadableTranscript  map[string]any    `json:"readableTranscript,omitempty"`
	DisplayTranscript   map[string]any    `json:"displayTranscript,omitempty"`
	Chapters            []Chapter         `json:"chapters,omitempty"`
	// Summary holds metadata about the meeting summary artifact (model,
	// templateVersion, format). Schema is intentionally open: map[string]any
	// lets future producers add keys without breaking decoders. The actual
	// summary.md content lives in Attachments, not here.
	//
	// Distinct from Meeting.Summary — see the doc comment on that field for
	// the split. Background: planning/initiatives/mvp/slices/V4-summary-generation/followup-plan.md.
	Summary     map[string]any   `json:"summary,omitempty"`
	Attachments []map[string]any `json:"attachments,omitempty"`
}

type Provenance struct {
	SpeechToText      *ProcessingStep `json:"speechToText,omitempty"`
	ReadableCleanup   *ProcessingStep `json:"readableCleanup,omitempty"`
	DisplayTranscript *ProcessingStep `json:"displayTranscript,omitempty"`
	MeetingSummary    *ProcessingStep `json:"meetingSummary,omitempty"`
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
	ID              string `json:"id"`
	Title           string `json:"title"`
	CreatedAtUTC    string `json:"createdAtUtc"`
	RecordedAtLocal string `json:"recordedAtLocal,omitempty"`
	ProcessedAtUTC  string `json:"processedAtUtc,omitempty"`
	DurationMS      int64  `json:"durationMs"`
	Language        string `json:"language,omitempty"`
	// RoomID identifies the conversation the meeting was recorded in: a
	// deterministic one-way derivation of the room's identity (D-622), never
	// the Talk token itself. Optional — a meeting packed from a file, a dev
	// run, or a Talk job whose room lookup failed has no room, and an absent
	// room is not an error anywhere downstream.
	RoomID string `json:"roomId,omitempty"`
	// RoomName is LEGACY and is no longer written (D-640). Producers before
	// D-640 froze the room's display name into every artifact; a display name
	// is editable, so honouring a rename meant rewriting every file that room
	// ever produced — and under D-612 a published recording cannot even be
	// deleted.
	//
	// The room's name at record time is still in the file, as the Title: that
	// is what a player shows and what the operator has always stamped there.
	// The room's CURRENT name lives in the catalog entry, which is mutable and
	// which the operator restamps on every publish.
	//
	// Kept on the struct, in both schemas and in every reader, because files
	// written before D-640 still carry it and `cassini retag` preserves what a
	// file already has rather than stripping it.
	RoomName string `json:"roomName,omitempty"`
	// JobID and AttemptNumber record which operator job and which of its
	// attempts produced this artifact (D-640). Both optional: a meeting packed
	// by hand, or by any producer that is not the operator, has neither.
	//
	// JobID discloses nothing new — the operator publishes the artifact as
	// meetings/<jobID>.opus and the catalog entry's id is the same value, so
	// it is already the file's name. AttemptNumber is genuinely new: a rerun
	// produces a different attempt of the same job, and nothing else in the
	// file says which one this is.
	//
	// There is deliberately no "attempt id": an attempt's identity in the
	// operator is the composite (job id, attempt number), and inventing a
	// single string for it here would invent an identifier the operator does
	// not have. AttemptNumber is 1-based, so the omitempty zero is
	// unambiguously "unknown" rather than a legal value.
	JobID         string `json:"jobId,omitempty"`
	AttemptNumber int    `json:"attemptNumber,omitempty"`
	// Summary is reserved for surfacing summary content as a *meeting attribute*
	// (e.g. a TL;DR readable without unpacking the gzipped payload). Currently
	// left empty — picking a meaning (TL;DR? full markdown? first heading?)
	// commits the schema, and no consumer has asked yet. Distinct from
	// Manifest.Summary which is metadata about the summary artifact.
	//
	// Background: planning/initiatives/mvp/slices/V4-summary-generation/followup-plan.md.
	Summary string `json:"summary,omitempty"`
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
	return EncodePayloadBytes(rawJSON, chunkSize)
}

// EncodePayloadBytes runs already-serialised manifest JSON through the payload
// pipeline: gzip, base64url, chunk, and the digest and byte counts the tags
// declare.
//
// Split out of EncodeManifest for the editors rather than the producers.
// `cassini retag` rewrites one field of an existing file's manifest and must
// re-emit everything else exactly as it found it — including whatever wire
// version that file uses and any key this build has never heard of — so it
// edits the JSON document and hands the bytes here. Marshalling a
// portable.Manifest instead would silently drop every field the struct does not
// model, which on a v2 file is its entire transcript descriptor set.
func EncodePayloadBytes(rawJSON []byte, chunkSize int) (EncodedPayload, error) {
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
	if manifest.Meeting.RecordedAtLocal != "" {
		tags["CASSINI_RECORDED_AT_LOCAL"] = manifest.Meeting.RecordedAtLocal
	}
	if manifest.Meeting.ProcessedAtUTC != "" {
		tags["CASSINI_PROCESSED_AT"] = manifest.Meeting.ProcessedAtUTC
	}
	applyRoomTags(tags, manifest.Meeting)
	applyProvenanceTags(tags, manifest.Meeting)

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

// applyRoomTags mirrors the meeting's room onto plain OpusTags, and only when
// there is one to mirror.
//
// The room is already in the gzipped CASSINI_PAYLOAD_* manifest, so these tags
// are strictly redundant for a Go or Node reader. They exist for the readers
// that are neither: reading the room out of the payload means concatenating N
// chunks, base64url-decoding, gunzipping and parsing JSON, which is a program
// — while these tags fall out of one `ffprobe -show_entries format_tags` call.
// The catalog backfill (D-622) is a shell script for exactly that reason, and
// so is anything an operator writes at a terminal.
//
// Absent rather than empty when unknown: an empty CASSINI_ROOM_ID would read as
// "this meeting has a room whose id is the empty string", and a consumer
// checking presence would have to know to also check emptiness.
func applyRoomTags(tags map[string]string, meeting Meeting) {
	if meeting.RoomID != "" {
		tags["CASSINI_ROOM_ID"] = meeting.RoomID
	}
	if meeting.RoomName != "" {
		tags["CASSINI_ROOM_NAME"] = meeting.RoomName
	}
}

// applyProvenanceTags mirrors which job and attempt produced the file, for the
// same reason applyRoomTags exists: one `ffprobe -show_entries format_tags`
// call, no program required.
//
// Both are absent rather than empty when unknown. CASSINI_ATTEMPT_NUMBER is
// omitted for any non-positive value — attempts are 1-based, so a zero is
// "nobody told us" and writing it would assert an attempt that cannot exist.
func applyProvenanceTags(tags map[string]string, meeting Meeting) {
	if meeting.JobID != "" {
		tags["CASSINI_JOB_ID"] = meeting.JobID
	}
	if meeting.AttemptNumber > 0 {
		tags["CASSINI_ATTEMPT_NUMBER"] = fmt.Sprintf("%d", meeting.AttemptNumber)
	}
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
