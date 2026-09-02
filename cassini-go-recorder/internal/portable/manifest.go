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
	// Format is the published portable meeting wire format: version 1 of the
	// Cassini portable meeting specification, the one anyone outside Cassini
	// can implement against. Producers write this, and this only.
	Format = "org.cassini.portable-meeting/1"

	// FormatDraft1, FormatDraft2 and FormatDraft3 are the three shapes this
	// producer wrote before the format was published. They were never
	// documented outside Cassini and no file carrying them left our own
	// storage, so they are read but never written.
	//
	// FormatDraft1 holds the same string as Format on purpose. The published
	// format took version 1 because it is the first version there is anything
	// to be compatible with; the number the drafts happened to use carries no
	// claim on it. The version number therefore cannot tell a published file
	// from a draft-1 file, and nothing in this package asks it to: the two are
	// told apart by shape. A published file indexes its transcripts in a
	// `transcripts` array and states integrity.matchPolicy
	// exact-opus-audio-v1; a draft-1 file carries one inline `transcript`
	// object and matchPolicy exact-pcm. Manifest.IsMultiTranscript is the test.
	FormatDraft1 = "org.cassini.portable-meeting/1"
	FormatDraft2 = "org.cassini.portable-meeting/2"
	FormatDraft3 = "org.cassini.portable-meeting/3"

	// WireVersion is the manifest `version` a producer writes, matching Format.
	WireVersion = 1
	// Draft1WireVersion, Draft2WireVersion and Draft3WireVersion are the
	// numbers the drafts wrote. Draft1WireVersion equals WireVersion for the
	// reason spelled out on FormatDraft1.
	Draft1WireVersion = 1
	Draft2WireVersion = 2
	Draft3WireVersion = 3

	Profile         = "ogg-opus"
	PayloadMIME     = "application/vnd.cassini.portable-meeting+json"
	PayloadEncoding = "base64url+gzip+utf8json"

	// PayloadSchema is the published JSON Schema for a version 1 manifest. It
	// resolves: a reader that fetches it gets the schema the file was written
	// against.
	PayloadSchema = "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json"
	// The draft schema URLs never resolved — cassini.local is not a host. They
	// are kept so a reader can recognise what an old file pointed at, and are
	// never written.
	PayloadSchemaDraft1 = "https://cassini.local/spec/cassini-portable-meeting-manifest-v1.schema.json"
	PayloadSchemaDraft2 = "https://cassini.local/spec/cassini-portable-meeting-manifest-v2.schema.json"
	PayloadSchemaDraft3 = "https://cassini.local/spec/cassini-portable-meeting-manifest-v3.schema.json"

	AudioPCMFormat            = "s16le"
	AudioMatchPolicy          = "exact-opus-audio-v1"
	LegacyAudioMatchPolicyPCM = "exact-pcm"
	DefaultPayloadChunkSize   = 4096
	Description               = "Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON."

	// DecodeHint describes the multi-transcript layout the published format
	// uses; drafts 2 and 3 share it. DecodeHintDraft1 describes draft 1's
	// single inline transcript.
	DecodeHint       = "Concatenate CASSINI_PAYLOAD_000..N for the manifest; for a transcript body concatenate CASSINI_TX_<ID>_PAYLOAD_000..N. Each chunk set: base64url decode, gzip decompress, parse UTF-8 JSON."
	DecodeHintDraft1 = "Concatenate CASSINI_PAYLOAD_000..N, base64url decode, gzip decompress, parse UTF-8 JSON."

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
	// Draft 1 carried the whole transcript inline here and required the field;
	// no multi-transcript wire includes it (omitempty on a non-pointer struct
	// is a no-op, so draft-1 writes always render this — fine).
	Transcript Transcript `json:"transcript"`
	// The multi-transcript descriptor arrays, introduced by draft 2 and kept
	// by the published format. Populated when reading such a file; ignored on
	// draft-1 writes. Multi-transcript writes happen through
	// multiTranscriptWire (see manifest_v2.go), so these fields never appear
	// in that JSON either.
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
	// Attribution is meeting-level, not a per-transcript ProcessingStep: the
	// cross-track attribution stage runs once against the default raw
	// transcript. Nil for legacy files and attribution-less builds, and
	// omitted from the wire in that case.
	Attribution *AttributionProvenance `json:"attribution,omitempty"`
	// WordTimings is meeting-level for the same reason: one decode rule
	// produced every word in the file. Nil for every file built before the
	// rule changed, and for any file whose decoder never claimed to measure
	// word ends; omitted from the wire in both cases — its absence is the
	// signal.
	WordTimings *WordTimingProvenance `json:"wordTimings,omitempty"`
}

// WordTimingProvenance records how the producer decided where a word ends,
// restated field-for-field from the build artifact manifest (internal/transcribe
// writes that record; this package deliberately does not import it).
//
// It exists because the rule changed and the timings do not say which one
// produced them. Files built before D-690 ended a word at its last token
// including a trailing punctuation mark, which the ASR stamps at the *next*
// acoustic onset, so a sentence-final word could run for seconds over silence;
// consumers reasonably grew repairs that clip an over-long word back towards
// the meeting's median. Files carrying this record ended each word where the
// speaker's own audio ended, so an over-long word is now a measurement and
// clipping it corrupts correct timing.
//
// A consumer keys off presence, not value: absent means the ends may have come
// from a punctuation mark's timestamp — either an older build, or a decoder
// that never made the claim — and the legacy repair still applies.
type WordTimingProvenance struct {
	// EndsBoundedByAudio is true when each word's end was measured against its
	// speaker's own track rather than taken from its last token's timestamp.
	EndsBoundedByAudio bool `json:"endsBoundedByAudio"`
}

// AttributionProvenance is the cross-track attribution stage's record for the
// file's default raw transcript, restated field-for-field from the build
// artifact manifest (internal/transcribe writes that record; this package
// deliberately does not import it). It exists because drop mode deletes
// flagged words, and deleted words carry their per-word evidence away with
// them: once the file is published, this record is the only remaining trace
// that the words existed and why they are gone.
type AttributionProvenance struct {
	Ran bool `json:"ran"`
	// Mode is "annotate" (per-word evidence kept on the words), "drop"
	// (flagged words deleted before publication) or "disabled".
	Mode string `json:"mode"`
	// Reason says why the stage did not run; empty when Ran is true.
	Reason        string `json:"reason,omitempty"`
	WordsMeasured int    `json:"wordsMeasured"`
	WordsFlagged  int    `json:"wordsFlagged"`
	WordsDropped  int    `json:"wordsDropped"`
	// ThresholdDB is the meeting's estimated crosstalk threshold; absent when
	// the meeting showed no crosstalk population.
	ThresholdDB *float64 `json:"thresholdDb,omitempty"`
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
	OpusSHA256      string `json:"opusAudioSha256,omitempty"`
	PCMFormat       string `json:"pcmFormat,omitempty"`
	PCMSHA256       string `json:"pcmSha256,omitempty"`
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
	// AttributionGapDB and LowConfidenceSpeaker mirror the per-word
	// cross-track speaker-attribution evidence of transcript.words.v1.json
	// (see internal/transcribe wordEntry). AttributionGapDB is present
	// exactly on the words the attribution stage measured;
	// LowConfidenceSpeaker is emitted only when true. Unmeasured words carry
	// neither key, so a file packed without the stage is byte-identical to
	// one packed before these fields existed.
	AttributionGapDB     *float64 `json:"attributionGapDb,omitempty"`
	LowConfidenceSpeaker bool     `json:"lowConfidenceSpeaker,omitempty"`
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

// IsMultiTranscript reports whether this manifest indexes its transcripts in
// a `transcripts` array rather than carrying one inline.
//
// This is how a reader tells the published format from draft 1, and it has to
// be, because the two share a version number: the published format is version
// 1 and so was the first draft. The shape does not collide — a draft-1
// manifest has no `transcripts` key at all — so every branch that used to ask
// which version a file claimed now asks this instead.
func (m Manifest) IsMultiTranscript() bool {
	return len(m.Transcripts) > 0 || len(m.ReadableTranscripts) > 0
}

// NormalizeDraft1Manifest fills in the draft-1 wire's required fields. Draft 1
// was never published and is not written any more; this remains so the
// draft-1 encoder can build fixtures of files that still exist.
func NormalizeDraft1Manifest(manifest Manifest) Manifest {
	manifest.Kind = "cassini-portable-meeting"
	manifest.Version = Draft1WireVersion
	manifest.Profile = Profile
	manifest.Audio.Container = "ogg"
	manifest.Audio.Codec = "opus"
	manifest.Integrity.MatchPolicy = LegacyAudioMatchPolicyPCM
	manifest.Integrity.OpusSHA256 = ""
	if manifest.Integrity.PCMFormat == "" {
		manifest.Integrity.PCMFormat = AudioPCMFormat
	}
	if manifest.Transcript.Format == "" {
		manifest.Transcript.Format = "cassini.words.v1"
	}
	return manifest
}

// NormalizePublishedManifest fills in the published wire format: the
// multi-transcript layout whose recording identity is the canonical
// compressed Opus audio essence. Drafts 1 and 2 remain exact-PCM formats so
// readers of those never misinterpret a missing PCM digest as a successfully
// verified file.
func NormalizePublishedManifest(manifest Manifest) Manifest {
	manifest.Kind = "cassini-portable-meeting"
	manifest.Version = WireVersion
	manifest.Profile = Profile
	manifest.Audio.Container = "ogg"
	manifest.Audio.Codec = "opus"
	manifest.Integrity.MatchPolicy = AudioMatchPolicy
	manifest.Integrity.OpusSHA256 = strings.ToLower(strings.TrimSpace(manifest.Integrity.OpusSHA256))
	manifest.Integrity.PCMFormat = ""
	manifest.Integrity.PCMSHA256 = ""
	return manifest
}

// EncodeDraft1Manifest encodes a draft-1 manifest, whose transcript travels
// inline in the main payload. Reading path only: no producer writes draft 1.
func EncodeDraft1Manifest(manifest Manifest, chunkSize int) (EncodedPayload, error) {
	manifest = NormalizeDraft1Manifest(manifest)
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
// Split out of EncodeDraft1Manifest for the editors rather than the producers.
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
	return MeetingIDFromAudioHash(hash)
}

// MeetingIDFromAudioHash derives the stable meeting identity from the
// canonical compressed Opus digest. MeetingIDFromPCMHash remains as a legacy
// alias for callers reading pre-opus-digest manifests.
func MeetingIDFromAudioHash(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return ""
	}
	return "mtg_" + hash
}

// BuildDraft1OpusTags emits the OpusTag map of a draft-1 file. Reading path
// only, like EncodeDraft1Manifest: it is what builds fixtures of the files
// this producer wrote before the format was published.
func BuildDraft1OpusTags(manifest Manifest, payload EncodedPayload) map[string]string {
	manifest = NormalizeDraft1Manifest(manifest)

	tags := map[string]string{
		"TITLE":                       manifest.Meeting.Title,
		"DATE":                        manifest.Meeting.CreatedAtUTC,
		"DESCRIPTION":                 Description,
		"ENCODER":                     "Cassini",
		"CASSINI_FORMAT":              FormatDraft1,
		"CASSINI_PROFILE":             Profile,
		"CASSINI_PAYLOAD_MIME":        PayloadMIME,
		"CASSINI_PAYLOAD_ENCODING":    PayloadEncoding,
		"CASSINI_PAYLOAD_SCHEMA":      PayloadSchemaDraft1,
		"CASSINI_PAYLOAD_CHUNK_COUNT": fmt.Sprintf("%d", len(payload.Chunks)),
		"CASSINI_PAYLOAD_SHA256":      payload.SHA256,
		"CASSINI_PAYLOAD_RAW_BYTES":   fmt.Sprintf("%d", payload.RawBytes),
		"CASSINI_PAYLOAD_GZIP_BYTES":  fmt.Sprintf("%d", payload.CompressedBytes),
		"CASSINI_AUDIO_SAMPLE_RATE":   fmt.Sprintf("%d", manifest.Audio.SampleRate),
		"CASSINI_AUDIO_CHANNELS":      fmt.Sprintf("%d", manifest.Audio.Channels),
		"CASSINI_AUDIO_SAMPLE_COUNT":  fmt.Sprintf("%d", manifest.Audio.SampleCount),
		"CASSINI_AUDIO_DURATION_MS":   fmt.Sprintf("%d", manifest.Audio.DurationMS),
		"CASSINI_DECODE_HINT":         DecodeHintDraft1,
		"CASSINI_MEETING_ID":          manifest.Meeting.ID,
		"CASSINI_CREATED_AT":          manifest.Meeting.CreatedAtUTC,
		"CASSINI_SPEAKER_COUNT":       fmt.Sprintf("%d", len(manifest.Speakers)),
		"CASSINI_WORD_COUNT":          fmt.Sprintf("%d", manifest.Transcript.WordCount),
	}
	applyAudioIntegrityTags(tags, manifest.Integrity)
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
		applyAttributionProvenanceTags(tags, manifest.Provenance.Attribution)
	}

	for idx, chunk := range payload.Chunks {
		tags[fmt.Sprintf("CASSINI_PAYLOAD_%03d", idx)] = chunk
	}
	return tags
}

func applyAudioIntegrityTags(tags map[string]string, integrity Integrity) {
	policy := integrity.MatchPolicy
	if policy == "" {
		if integrity.OpusSHA256 != "" {
			policy = AudioMatchPolicy
		} else if integrity.PCMSHA256 != "" {
			policy = LegacyAudioMatchPolicyPCM
		}
	}
	if policy != "" {
		tags["CASSINI_AUDIO_MATCH_POLICY"] = policy
	}
	if integrity.OpusSHA256 != "" {
		tags["CASSINI_AUDIO_OPUS_SHA256"] = integrity.OpusSHA256
	}
	if integrity.PCMSHA256 != "" {
		pcmFormat := integrity.PCMFormat
		if pcmFormat == "" {
			pcmFormat = AudioPCMFormat
		}
		tags["CASSINI_AUDIO_PCM_FORMAT"] = pcmFormat
		tags["CASSINI_AUDIO_PCM_SHA256"] = integrity.PCMSHA256
	}
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

// applyAttributionProvenanceTags mirrors the attribution stage's record onto
// plain OpusTags, following the same convention as the CASSINI_STT_* and
// CASSINI_READABLE_* mirrors above it: the payload manifest's
// provenance.attribution is the record, these tags are the one-ffprobe-call
// convenience. In drop mode that record is the only remaining trace of the
// deleted words, so the mirror matters more here, not less.
func applyAttributionProvenanceTags(tags map[string]string, attribution *AttributionProvenance) {
	if attribution == nil {
		return
	}
	tags["CASSINI_ATTRIBUTION_RAN"] = fmt.Sprintf("%t", attribution.Ran)
	if attribution.Mode != "" {
		tags["CASSINI_ATTRIBUTION_MODE"] = attribution.Mode
	}
	if attribution.Reason != "" {
		tags["CASSINI_ATTRIBUTION_REASON"] = attribution.Reason
	}
	tags["CASSINI_ATTRIBUTION_WORDS_MEASURED"] = fmt.Sprintf("%d", attribution.WordsMeasured)
	tags["CASSINI_ATTRIBUTION_WORDS_FLAGGED"] = fmt.Sprintf("%d", attribution.WordsFlagged)
	tags["CASSINI_ATTRIBUTION_WORDS_DROPPED"] = fmt.Sprintf("%d", attribution.WordsDropped)
	if attribution.ThresholdDB != nil {
		tags["CASSINI_ATTRIBUTION_THRESHOLD_DB"] = fmt.Sprintf("%g", *attribution.ThresholdDB)
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
