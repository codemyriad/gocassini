package portable

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	// Format is the published portable meeting wire format.
	Format = "org.cassini.portable-meeting/1"

	// WireVersion is the manifest version paired with Format.
	WireVersion = 1

	Profile         = "ogg-opus"
	PayloadMIME     = "application/vnd.cassini.portable-meeting+json"
	PayloadEncoding = "base64url+gzip+utf8json"

	// PayloadSchema is the published JSON Schema for a version 1 manifest. It
	// resolves: a reader that fetches it gets the schema the file was written
	// against.
	PayloadSchema           = "https://cassini-format.codemyriad.io/schema/cassini-portable-meeting-manifest-v1.schema.json"
	AudioMatchPolicy        = "exact-opus-audio-v1"
	DefaultPayloadChunkSize = 4096
	Description             = "Cassini portable meeting file. Decode CASSINI_PAYLOAD_*: base64url -> gzip -> UTF-8 JSON."
	DecodeHint              = "Concatenate CASSINI_PAYLOAD_000..N for the manifest; for a transcript body concatenate CASSINI_TX_<ID>_PAYLOAD_000..N. Each chunk set: base64url decode, gzip decompress, parse UTF-8 JSON."

	TranscriptBodyMIMEWords    = "application/vnd.cassini.transcript-words+json"
	TranscriptBodyMIMEReadable = "application/vnd.cassini.transcript-readable+json"

	RoleRawASR = "raw-asr"
	// RoleWithdrawnReadableCleanup is the role the deleted LLM cleanup step used
	// to write. Nothing produces it any more, and no published file in the
	// archive carries one, but the name survives so a reader can recognise such
	// an entry and skip it instead of failing the whole file.
	RoleWithdrawnReadableCleanup = "readable-cleanup"
	RoleDisplay                  = "display"
	RoleHumanCorrected           = "human-corrected"
	RoleTranslation              = "translation"
	// RoleScripted is authored text the recording is a performance of: a
	// script, a song's lyrics. Not a transcription, so it never names a
	// source transcript.
	RoleScripted = "scripted"
)

type Manifest struct {
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	Profile   string    `json:"profile"`
	Meeting   Meeting   `json:"meeting"`
	Audio     Audio     `json:"audio"`
	Integrity Integrity `json:"integrity"`
	Speakers  []Speaker `json:"speakers"`
	// Transcript bodies live in independent OpusTag chunk sets referenced by
	// these descriptors. Keeping the index separate lets one meeting carry
	// multiple raw, cleaned, corrected, or translated transcripts.
	Transcripts         []TranscriptEntry `json:"transcripts,omitempty"`
	ReadableTranscripts []TranscriptEntry `json:"readableTranscripts,omitempty"`
	Provenance          *Provenance       `json:"provenance,omitempty"`
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
	// Hints records decoder biasing for a speech-to-text step. It mirrors the
	// build manifest's shape field for field so the packer can carry it into
	// the published file by plain JSON round-trip. Without this the record
	// would be written by the build and then silently dropped at pack time,
	// which is worse than not recording it at all.
	Hints *HintsProvenance `json:"hints,omitempty"`
}

// HintsProvenance says what decoder biasing a speech-to-text pass actually
// applied, and when it could not, why. Absent means the pass ran unbiased.
type HintsProvenance struct {
	TermCount      int     `json:"termCount"`
	Score          float32 `json:"score,omitempty"`
	DecodingMethod string  `json:"decodingMethod,omitempty"`
	Applied        bool    `json:"applied"`
	Reason         string  `json:"reason,omitempty"`
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
	MatchPolicy string `json:"matchPolicy"`
	OpusSHA256  string `json:"opusAudioSha256"`
	SampleRate  int    `json:"sampleRate"`
	Channels    int    `json:"channels"`
	SampleCount int64  `json:"sampleCount"`
	DurationMS  int64  `json:"durationMs"`
}

type Speaker struct {
	ID    string `json:"id"`
	Label string `json:"label"`
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

// NormalizePublishedManifest fills in the published wire format: the
// multi-transcript layout whose recording identity is the canonical
// compressed Opus audio essence.
func NormalizePublishedManifest(manifest Manifest) Manifest {
	manifest.Kind = "cassini-portable-meeting"
	manifest.Version = WireVersion
	manifest.Profile = Profile
	manifest.Audio.Container = "ogg"
	manifest.Audio.Codec = "opus"
	manifest.Integrity.MatchPolicy = AudioMatchPolicy
	manifest.Integrity.OpusSHA256 = strings.ToLower(strings.TrimSpace(manifest.Integrity.OpusSHA256))
	return manifest
}

// ValidatePublishedManifest rejects payloads outside the single portable
// meeting contract understood by this build. Callers should run this after
// decoding and before acting on any manifest field.
func ValidatePublishedManifest(manifest Manifest) error {
	if manifest.Kind != "cassini-portable-meeting" {
		return fmt.Errorf("unexpected payload kind %q", manifest.Kind)
	}
	if manifest.Version != WireVersion {
		return fmt.Errorf("unsupported payload version %d", manifest.Version)
	}
	if manifest.Profile != Profile {
		return fmt.Errorf("unsupported payload profile %q", manifest.Profile)
	}
	if len(manifest.Transcripts) == 0 {
		return fmt.Errorf("invalid portable meeting manifest: transcripts must contain at least one entry")
	}
	if policy := strings.ToLower(strings.TrimSpace(manifest.Integrity.MatchPolicy)); policy != AudioMatchPolicy {
		return fmt.Errorf("unsupported audio integrity matchPolicy %q", policy)
	}
	if !sha256HexRE.MatchString(manifest.Integrity.OpusSHA256) {
		return fmt.Errorf("invalid integrity.opusAudioSha256: expected 64 lowercase hex characters")
	}

	seen := make(map[string]struct{}, len(manifest.Transcripts)+len(manifest.ReadableTranscripts))
	wordIDs := make(map[string]struct{}, len(manifest.Transcripts))
	for _, entry := range manifest.Transcripts {
		if err := validatePublishedTranscriptEntry(entry, false); err != nil {
			return err
		}
		if _, exists := seen[entry.ID]; exists {
			return fmt.Errorf("duplicate transcript id %q", entry.ID)
		}
		seen[entry.ID] = struct{}{}
		wordIDs[entry.ID] = struct{}{}
	}
	for _, entry := range manifest.ReadableTranscripts {
		// A withdrawn readable-cleanup entry is skipped, not rejected. The
		// format contract says a reader fails closed on a layout it cannot
		// understand, but this one it understands perfectly well: it is a body
		// this build no longer writes. Failing here would turn an older file
		// into an error and take its perfectly good raw transcript with it.
		if entry.Role == RoleWithdrawnReadableCleanup {
			continue
		}
		if err := validatePublishedTranscriptEntry(entry, true); err != nil {
			return err
		}
		if _, exists := seen[entry.ID]; exists {
			return fmt.Errorf("duplicate transcript id %q", entry.ID)
		}
		if _, exists := wordIDs[entry.SourceTranscriptID]; !exists {
			return fmt.Errorf("transcript %q has unknown sourceTranscriptId %q", entry.ID, entry.SourceTranscriptID)
		}
		seen[entry.ID] = struct{}{}
	}
	for _, entry := range manifest.Transcripts {
		if entry.SourceTranscriptID == "" {
			continue
		}
		if _, exists := wordIDs[entry.SourceTranscriptID]; !exists {
			return fmt.Errorf("transcript %q has unknown sourceTranscriptId %q", entry.ID, entry.SourceTranscriptID)
		}
	}
	return nil
}

func validatePublishedTranscriptEntry(entry TranscriptEntry, readable bool) error {
	if err := ValidateTranscriptID(entry.ID); err != nil {
		return err
	}
	if strings.TrimSpace(entry.Format) == "" {
		return fmt.Errorf("transcript %q has an empty format", entry.ID)
	}
	if readable {
		if entry.Role != RoleDisplay {
			return fmt.Errorf("transcript %q has unsupported readable role %q", entry.ID, entry.Role)
		}
		if strings.TrimSpace(entry.SourceTranscriptID) == "" {
			return fmt.Errorf("transcript %q requires sourceTranscriptId", entry.ID)
		}
	} else {
		switch entry.Role {
		case RoleRawASR, RoleScripted:
			if entry.SourceTranscriptID != "" {
				return fmt.Errorf("transcript %q (role %q) must not set sourceTranscriptId", entry.ID, entry.Role)
			}
		case RoleHumanCorrected, RoleTranslation:
			if strings.TrimSpace(entry.SourceTranscriptID) == "" {
				return fmt.Errorf("transcript %q (role %q) requires sourceTranscriptId", entry.ID, entry.Role)
			}
		default:
			return fmt.Errorf("transcript %q has unsupported role %q", entry.ID, entry.Role)
		}
	}
	if !payloadPrefixRE.MatchString(entry.PayloadRef.Prefix) || entry.PayloadRef.ChunkCount < 1 {
		return fmt.Errorf("transcript %q has invalid payloadRef", entry.ID)
	}
	if entry.PayloadRef.Encoding != PayloadEncoding {
		return fmt.Errorf("transcript %q has unsupported payload encoding %q", entry.ID, entry.PayloadRef.Encoding)
	}
	if !sha256HexRE.MatchString(entry.PayloadRef.SHA256) {
		return fmt.Errorf("transcript %q has invalid payload sha256", entry.ID)
	}
	if strings.TrimSpace(entry.PayloadRef.MIME) == "" || entry.PayloadRef.RawBytes < 0 || entry.PayloadRef.GzipBytes < 0 {
		return fmt.Errorf("transcript %q has invalid payload metadata", entry.ID)
	}
	return nil
}

// EncodePayloadBytes runs already-serialised manifest JSON through the payload
// pipeline: gzip, base64url, chunk, and the digest and byte counts the tags
// declare.
//
// Split out of the typed encoder for editors rather than producers.
// `cassini retag` rewrites one field of an existing file's manifest and must
// re-emit everything else exactly as it found it, so it edits the JSON document
// and hands the bytes here. Marshalling a portable.Manifest instead would
// silently drop any extension field the struct does not model.
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

// MeetingIDFromAudioHash derives the stable meeting identity from the
// canonical compressed Opus digest.
func MeetingIDFromAudioHash(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if hash == "" {
		return ""
	}
	return "mtg_" + hash
}

func applyAudioIntegrityTags(tags map[string]string, integrity Integrity) {
	policy := integrity.MatchPolicy
	if policy == "" {
		policy = AudioMatchPolicy
	}
	if policy != "" {
		tags["CASSINI_AUDIO_MATCH_POLICY"] = policy
	}
	if integrity.OpusSHA256 != "" {
		tags["CASSINI_AUDIO_OPUS_SHA256"] = integrity.OpusSHA256
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
