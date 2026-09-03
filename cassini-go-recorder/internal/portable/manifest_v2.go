package portable

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The multi-transcript wire format. Defined as a private set of structs so
// the Manifest type, which still models draft 1's inline transcript, stays
// untouched. Draft 2 introduced this layout; the published format reuses it
// unchanged and differs only in the audio-integrity contract.

type multiTranscriptWire struct {
	Kind                string                         `json:"kind"`
	Version             int                            `json:"version"`
	Profile             string                         `json:"profile"`
	Meeting             Meeting                        `json:"meeting"`
	Audio               Audio                          `json:"audio"`
	Integrity           Integrity                      `json:"integrity"`
	Speakers            []Speaker                      `json:"speakers"`
	Transcripts         []TranscriptEntry              `json:"transcripts"`
	ReadableTranscripts []TranscriptEntry              `json:"readableTranscripts,omitempty"`
	Provenance          *multiTranscriptProvenanceWire `json:"provenance,omitempty"`
	Chapters            []Chapter                      `json:"chapters,omitempty"`
	Summary             map[string]any                 `json:"summary,omitempty"`
	Attachments         []map[string]any               `json:"attachments,omitempty"`
}

type multiTranscriptProvenanceWire struct {
	SpeechToText      map[string]*ProcessingStep `json:"speechToText,omitempty"`
	ReadableCleanup   map[string]*ProcessingStep `json:"readableCleanup,omitempty"`
	DisplayTranscript map[string]*ProcessingStep `json:"displayTranscript,omitempty"`
	MeetingSummary    *ProcessingStep            `json:"meetingSummary,omitempty"`
	Attribution       *AttributionProvenance     `json:"attribution,omitempty"`
	WordTimings       *WordTimingProvenance      `json:"wordTimings,omitempty"`
}

// TranscriptEntry describes one transcript body embedded in a multi-transcript
// portable meeting file. The body itself lives in its own OpusTag chunk set
// referenced by PayloadRef.Prefix.
type TranscriptEntry struct {
	ID                 string     `json:"id"`
	Role               string     `json:"role"`
	Default            bool       `json:"default,omitempty"`
	Format             string     `json:"format"`
	Language           string     `json:"language,omitempty"`
	WordCount          int        `json:"wordCount,omitempty"`
	SourceTranscriptID string     `json:"sourceTranscriptId,omitempty"`
	CreatedAtUTC       string     `json:"createdAtUtc,omitempty"`
	PayloadRef         PayloadRef `json:"payloadRef"`
}

// PayloadRef points at one per-transcript OpusTag chunk set and records the
// hashes and sizes the consumer uses for integrity verification.
type PayloadRef struct {
	Prefix     string `json:"prefix"`
	ChunkCount int    `json:"chunkCount"`
	SHA256     string `json:"sha256"`
	RawBytes   int    `json:"rawBytes"`
	GzipBytes  int    `json:"gzipBytes"`
	MIME       string `json:"mime"`
	Encoding   string `json:"encoding"`
}

// TranscriptBody is the JSON shape of a per-transcript chunk set body. It is
// identical to draft 1's inline Transcript object.
type TranscriptBody struct {
	Format    string           `json:"format"`
	Language  string           `json:"language,omitempty"`
	WordCount int              `json:"wordCount"`
	Items     []TranscriptItem `json:"items"`
}

// NamedEncodedPayload is one transcript body, already encoded, paired with
// the OpusTag prefix its chunks will be written under.
type NamedEncodedPayload struct {
	ID      string
	Prefix  string
	Payload EncodedPayload
}

// EncodedMultiTranscriptManifest is the result of encoding a multi-transcript
// manifest: a main payload (index + provenance + speakers + meeting) plus one
// chunk set per transcript.
type EncodedMultiTranscriptManifest struct {
	Main                EncodedPayload
	Transcripts         []NamedEncodedPayload
	ReadableTranscripts []NamedEncodedPayload
}

// TranscriptInput is what producers pass to the encoders: the body plus
// the descriptor metadata for one transcript. The producer also passes one
// optional ProcessingStep that lands in provenance.<group>.<id>.
type TranscriptInput struct {
	ID                 string
	Role               string
	Default            bool
	Language           string
	CreatedAtUTC       string
	SourceTranscriptID string // required for readable-cleanup / display roles
	Body               TranscriptBody
	Provenance         *ProcessingStep
}

var (
	transcriptIDRE        = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	sha256HexRE           = regexp.MustCompile(`^[0-9a-f]{64}$`)
	reservedTranscriptIDs = map[string]struct{}{
		"payload":     {},
		"format":      {},
		"audio":       {},
		"meeting":     {},
		"integrity":   {},
		"transcript":  {},
		"provenance":  {},
		"summary":     {},
		"attachments": {},
		"speakers":    {},
	}
)

// ValidateTranscriptID checks the id against the spec pattern and reserved
// list. Returns nil on success.
func ValidateTranscriptID(id string) error {
	if !transcriptIDRE.MatchString(id) {
		return fmt.Errorf("transcript id %q does not match ^[a-z0-9][a-z0-9-]{0,31}$", id)
	}
	if _, reserved := reservedTranscriptIDs[id]; reserved {
		return fmt.Errorf("transcript id %q is reserved (collides with the descriptor tag namespace)", id)
	}
	return nil
}

// TranscriptIDToTagSuffix converts a kebab/lowercase id into the UPPER_SNAKE
// suffix used inside CASSINI_TX_<UPPER>_PAYLOAD_* tag names.
func TranscriptIDToTagSuffix(id string) string {
	return strings.ToUpper(strings.ReplaceAll(id, "-", "_"))
}

// TranscriptIDToTagPrefix returns the full prefix CASSINI_TX_<UPPER>_PAYLOAD_.
func TranscriptIDToTagPrefix(id string) string {
	return "CASSINI_TX_" + TranscriptIDToTagSuffix(id) + "_PAYLOAD_"
}

// EncodeTranscriptBody compresses and encodes one transcript body and returns
// an EncodedPayload plus a PayloadRef ready to embed in a manifest index.
func EncodeTranscriptBody(body TranscriptBody, id string, role string, chunkSize int) (EncodedPayload, PayloadRef, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultPayloadChunkSize
	}
	rawJSON, err := json.Marshal(body)
	if err != nil {
		return EncodedPayload{}, PayloadRef{}, fmt.Errorf("marshal transcript body %q: %w", id, err)
	}
	var compressed bytes.Buffer
	gzw := gzip.NewWriter(&compressed)
	if _, err := gzw.Write(rawJSON); err != nil {
		return EncodedPayload{}, PayloadRef{}, fmt.Errorf("gzip transcript body %q: %w", id, err)
	}
	if err := gzw.Close(); err != nil {
		return EncodedPayload{}, PayloadRef{}, fmt.Errorf("close gzip transcript body %q: %w", id, err)
	}

	sum := sha256.Sum256(rawJSON)
	encoded := base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	chunks := ChunkString(encoded, chunkSize)
	prefix := TranscriptIDToTagPrefix(id)
	mime := TranscriptBodyMIMEWords
	if role == RoleReadableCleanup || role == RoleDisplay {
		mime = TranscriptBodyMIMEReadable
	}
	return EncodedPayload{
			JSON:            rawJSON,
			GZIP:            compressed.Bytes(),
			Base64URL:       encoded,
			SHA256:          hex.EncodeToString(sum[:]),
			RawBytes:        len(rawJSON),
			CompressedBytes: compressed.Len(),
			Chunks:          chunks,
		}, PayloadRef{
			Prefix:     prefix,
			ChunkCount: len(chunks),
			SHA256:     hex.EncodeToString(sum[:]),
			RawBytes:   len(rawJSON),
			GzipBytes:  compressed.Len(),
			MIME:       mime,
			Encoding:   PayloadEncoding,
		}, nil
}

// EncodeDraft2Manifest derives a draft-2 wire manifest from a Manifest plus a
// list of transcript inputs. Returns the main index payload and one named
// per-transcript payload for each input. Validates ids, default-per-role, and
// derived-role source pointers.
//
// Draft 2 was never published; this stays for reading and for the fixtures
// that stand in for the files it produced.
func EncodeDraft2Manifest(manifest Manifest, transcripts []TranscriptInput, chunkSize int) (EncodedMultiTranscriptManifest, error) {
	manifest.Integrity.MatchPolicy = LegacyAudioMatchPolicyPCM
	manifest.Integrity.OpusSHA256 = ""
	if manifest.Integrity.PCMFormat == "" {
		manifest.Integrity.PCMFormat = AudioPCMFormat
	}
	return encodeMultiTranscriptManifest(manifest, transcripts, chunkSize, Draft2WireVersion)
}

// EncodePublishedManifest encodes the published wire format: the
// multi-transcript layout, with the audio identity contract a
// decoder-independent compressed Opus digest. This is what producers call.
func EncodePublishedManifest(manifest Manifest, transcripts []TranscriptInput, chunkSize int) (EncodedMultiTranscriptManifest, error) {
	manifest = NormalizePublishedManifest(manifest)
	if !sha256HexRE.MatchString(manifest.Integrity.OpusSHA256) {
		return EncodedMultiTranscriptManifest{}, fmt.Errorf("published manifest needs integrity.opusAudioSha256 as 64 lowercase hex characters")
	}
	return encodeMultiTranscriptManifest(manifest, transcripts, chunkSize, WireVersion)
}

func encodeMultiTranscriptManifest(manifest Manifest, transcripts []TranscriptInput, chunkSize, version int) (EncodedMultiTranscriptManifest, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultPayloadChunkSize
	}
	if len(transcripts) == 0 {
		return EncodedMultiTranscriptManifest{}, fmt.Errorf("version %d manifest needs at least one transcript", version)
	}

	if err := validateTranscriptInputs(transcripts); err != nil {
		return EncodedMultiTranscriptManifest{}, err
	}

	rawEntries := make([]TranscriptEntry, 0, len(transcripts))
	readableEntries := make([]TranscriptEntry, 0)
	rawEncoded := make([]NamedEncodedPayload, 0, len(transcripts))
	readableEncoded := make([]NamedEncodedPayload, 0)

	provenance := &multiTranscriptProvenanceWire{}
	if manifest.Provenance != nil {
		provenance.MeetingSummary = manifest.Provenance.MeetingSummary
		// Meeting-level, not keyed by transcript id: the attribution stage
		// runs once against the default raw transcript. In drop mode this
		// record is the only remaining trace of the deleted words, so a wire
		// that sheds it publishes a file with no audit trail at all.
		provenance.Attribution = manifest.Provenance.Attribution
		// Meeting-level for the same reason: one word-end rule produced every
		// word in the file, and its absence is what tells a consumer the ends
		// came from a punctuation mark's timestamp. A wire that sheds it
		// publishes a file consumers must treat as legacy, and the legacy
		// repair clips correct timings back to a guess.
		provenance.WordTimings = manifest.Provenance.WordTimings
	}

	for _, input := range transcripts {
		payload, ref, err := EncodeTranscriptBody(input.Body, input.ID, input.Role, chunkSize)
		if err != nil {
			return EncodedMultiTranscriptManifest{}, err
		}
		entry := TranscriptEntry{
			ID:                 input.ID,
			Role:               input.Role,
			Default:            input.Default,
			Format:             input.Body.Format,
			Language:           input.Language,
			WordCount:          input.Body.WordCount,
			SourceTranscriptID: input.SourceTranscriptID,
			CreatedAtUTC:       input.CreatedAtUTC,
			PayloadRef:         ref,
		}
		named := NamedEncodedPayload{ID: input.ID, Prefix: ref.Prefix, Payload: payload}
		switch input.Role {
		case RoleReadableCleanup, RoleDisplay:
			readableEntries = append(readableEntries, entry)
			readableEncoded = append(readableEncoded, named)
			if input.Provenance != nil {
				if input.Role == RoleDisplay {
					if provenance.DisplayTranscript == nil {
						provenance.DisplayTranscript = map[string]*ProcessingStep{}
					}
					provenance.DisplayTranscript[input.ID] = input.Provenance
				} else {
					if provenance.ReadableCleanup == nil {
						provenance.ReadableCleanup = map[string]*ProcessingStep{}
					}
					provenance.ReadableCleanup[input.ID] = input.Provenance
				}
			}
		default:
			rawEntries = append(rawEntries, entry)
			rawEncoded = append(rawEncoded, named)
			if input.Provenance != nil {
				if provenance.SpeechToText == nil {
					provenance.SpeechToText = map[string]*ProcessingStep{}
				}
				provenance.SpeechToText[input.ID] = input.Provenance
			}
		}
	}

	if !hasAnyProvenance(provenance) {
		provenance = nil
	}

	wire := multiTranscriptWire{
		Kind:                "cassini-portable-meeting",
		Version:             version,
		Profile:             Profile,
		Meeting:             manifest.Meeting,
		Audio:               manifest.Audio,
		Integrity:           manifest.Integrity,
		Speakers:            manifest.Speakers,
		Transcripts:         rawEntries,
		ReadableTranscripts: readableEntries,
		Provenance:          provenance,
		Chapters:            manifest.Chapters,
		Summary:             manifest.Summary,
		Attachments:         manifest.Attachments,
	}

	rawJSON, err := json.Marshal(wire)
	if err != nil {
		return EncodedMultiTranscriptManifest{}, fmt.Errorf("marshal v%d manifest: %w", version, err)
	}
	var compressed bytes.Buffer
	gzw := gzip.NewWriter(&compressed)
	if _, err := gzw.Write(rawJSON); err != nil {
		return EncodedMultiTranscriptManifest{}, fmt.Errorf("gzip v%d manifest: %w", version, err)
	}
	if err := gzw.Close(); err != nil {
		return EncodedMultiTranscriptManifest{}, fmt.Errorf("close gzip v%d manifest: %w", version, err)
	}
	sum := sha256.Sum256(rawJSON)
	encoded := base64.RawURLEncoding.EncodeToString(compressed.Bytes())
	mainPayload := EncodedPayload{
		JSON:            rawJSON,
		GZIP:            compressed.Bytes(),
		Base64URL:       encoded,
		SHA256:          hex.EncodeToString(sum[:]),
		RawBytes:        len(rawJSON),
		CompressedBytes: compressed.Len(),
		Chunks:          ChunkString(encoded, chunkSize),
	}

	return EncodedMultiTranscriptManifest{
		Main:                mainPayload,
		Transcripts:         rawEncoded,
		ReadableTranscripts: readableEncoded,
	}, nil
}

func hasAnyProvenance(p *multiTranscriptProvenanceWire) bool {
	if p == nil {
		return false
	}
	return len(p.SpeechToText) > 0 || len(p.ReadableCleanup) > 0 || len(p.DisplayTranscript) > 0 ||
		p.MeetingSummary != nil || p.Attribution != nil || p.WordTimings != nil
}

func validateTranscriptInputs(transcripts []TranscriptInput) error {
	seenID := map[string]struct{}{}
	rawDefaults := 0
	readableDefaults := 0
	displayDefaults := 0
	for _, input := range transcripts {
		if err := ValidateTranscriptID(input.ID); err != nil {
			return err
		}
		if _, dup := seenID[input.ID]; dup {
			return fmt.Errorf("duplicate transcript id %q", input.ID)
		}
		seenID[input.ID] = struct{}{}

		switch input.Role {
		case RoleRawASR, RoleHumanCorrected, RoleTranslation, RoleScripted:
			if input.Default {
				rawDefaults++
			}
			// raw-asr came from the audio and scripted is what the audio
			// performs; neither is derived from another transcript. The other
			// two are, and MUST name their source.
			switch input.Role {
			case RoleRawASR, RoleScripted:
				if input.SourceTranscriptID != "" {
					return fmt.Errorf("transcript %q (role %q) must not set sourceTranscriptId", input.ID, input.Role)
				}
			default:
				if strings.TrimSpace(input.SourceTranscriptID) == "" {
					return fmt.Errorf("transcript %q (role %q) requires sourceTranscriptId", input.ID, input.Role)
				}
			}
		case RoleReadableCleanup:
			if strings.TrimSpace(input.SourceTranscriptID) == "" {
				return fmt.Errorf("transcript %q (role readable-cleanup) requires sourceTranscriptId", input.ID)
			}
			if input.Default {
				readableDefaults++
			}
		case RoleDisplay:
			if strings.TrimSpace(input.SourceTranscriptID) == "" {
				return fmt.Errorf("transcript %q (role display) requires sourceTranscriptId", input.ID)
			}
			if input.Default {
				displayDefaults++
			}
		default:
			return fmt.Errorf("transcript %q has unknown role %q", input.ID, input.Role)
		}
	}
	if rawDefaults > 1 {
		return fmt.Errorf("more than one default raw-ASR transcript declared")
	}
	if readableDefaults > 1 {
		return fmt.Errorf("more than one default readable-cleanup transcript declared")
	}
	if displayDefaults > 1 {
		return fmt.Errorf("more than one default display transcript declared")
	}

	// validate that every sourceTranscriptId points at a declared id
	for _, input := range transcripts {
		if input.SourceTranscriptID == "" {
			continue
		}
		if _, ok := seenID[input.SourceTranscriptID]; !ok {
			return fmt.Errorf("transcript %q has sourceTranscriptId %q that is not in this file", input.ID, input.SourceTranscriptID)
		}
	}

	return nil
}

// BuildDraft2OpusTags emits the OpusTags map for a draft-2 portable meeting
// file: human-readable summary tags, transcript descriptor tags, the main
// manifest chunk set, and one chunk set per transcript body.
func BuildDraft2OpusTags(manifest Manifest, encoded EncodedMultiTranscriptManifest, defaultRawID string) map[string]string {
	manifest.Integrity.MatchPolicy = LegacyAudioMatchPolicyPCM
	manifest.Integrity.OpusSHA256 = ""
	if manifest.Integrity.PCMFormat == "" {
		manifest.Integrity.PCMFormat = AudioPCMFormat
	}
	return buildMultiTranscriptOpusTags(manifest, encoded, defaultRawID, FormatDraft2, PayloadMIME, PayloadSchemaDraft2, DecodeHint)
}

// BuildPublishedOpusTags emits the OpusTags map of a published portable
// meeting file: format tag org.cassini.portable-meeting/1 and the schema URL
// that resolves.
func BuildPublishedOpusTags(manifest Manifest, encoded EncodedMultiTranscriptManifest, defaultRawID string) map[string]string {
	manifest = NormalizePublishedManifest(manifest)
	return buildMultiTranscriptOpusTags(manifest, encoded, defaultRawID, Format, PayloadMIME, PayloadSchema, DecodeHint)
}

func buildMultiTranscriptOpusTags(manifest Manifest, encoded EncodedMultiTranscriptManifest, defaultRawID, format, payloadMIME, payloadSchema, decodeHint string) map[string]string {
	tags := map[string]string{
		"TITLE":                       manifest.Meeting.Title,
		"DATE":                        manifest.Meeting.CreatedAtUTC,
		"DESCRIPTION":                 Description,
		"ENCODER":                     "Cassini",
		"CASSINI_FORMAT":              format,
		"CASSINI_PROFILE":             Profile,
		"CASSINI_PAYLOAD_MIME":        payloadMIME,
		"CASSINI_PAYLOAD_ENCODING":    PayloadEncoding,
		"CASSINI_PAYLOAD_SCHEMA":      payloadSchema,
		"CASSINI_PAYLOAD_CHUNK_COUNT": fmt.Sprintf("%d", len(encoded.Main.Chunks)),
		"CASSINI_PAYLOAD_SHA256":      encoded.Main.SHA256,
		"CASSINI_PAYLOAD_RAW_BYTES":   fmt.Sprintf("%d", encoded.Main.RawBytes),
		"CASSINI_PAYLOAD_GZIP_BYTES":  fmt.Sprintf("%d", encoded.Main.CompressedBytes),
		"CASSINI_AUDIO_SAMPLE_RATE":   fmt.Sprintf("%d", manifest.Audio.SampleRate),
		"CASSINI_AUDIO_CHANNELS":      fmt.Sprintf("%d", manifest.Audio.Channels),
		"CASSINI_AUDIO_SAMPLE_COUNT":  fmt.Sprintf("%d", manifest.Audio.SampleCount),
		"CASSINI_AUDIO_DURATION_MS":   fmt.Sprintf("%d", manifest.Audio.DurationMS),
		"CASSINI_DECODE_HINT":         decodeHint,
		"CASSINI_MEETING_ID":          manifest.Meeting.ID,
		"CASSINI_CREATED_AT":          manifest.Meeting.CreatedAtUTC,
		"CASSINI_SPEAKER_COUNT":       fmt.Sprintf("%d", len(manifest.Speakers)),
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

	// Main payload chunks
	for idx, chunk := range encoded.Main.Chunks {
		tags[fmt.Sprintf("CASSINI_PAYLOAD_%03d", idx)] = chunk
	}

	// Per-transcript chunk sets, sorted for deterministic output
	allEncoded := append([]NamedEncodedPayload{}, encoded.Transcripts...)
	allEncoded = append(allEncoded, encoded.ReadableTranscripts...)
	sort.Slice(allEncoded, func(i, j int) bool { return allEncoded[i].ID < allEncoded[j].ID })
	for _, named := range allEncoded {
		suffix := TranscriptIDToTagSuffix(named.ID)
		descriptorBase := "CASSINI_TX_" + suffix + "_PAYLOAD_"
		tags[descriptorBase+"MIME"] = transcriptMIMEFor(named.ID, encoded)
		tags[descriptorBase+"ENCODING"] = PayloadEncoding
		tags[descriptorBase+"CHUNK_COUNT"] = fmt.Sprintf("%d", len(named.Payload.Chunks))
		tags[descriptorBase+"SHA256"] = named.Payload.SHA256
		tags[descriptorBase+"RAW_BYTES"] = fmt.Sprintf("%d", named.Payload.RawBytes)
		tags[descriptorBase+"GZIP_BYTES"] = fmt.Sprintf("%d", named.Payload.CompressedBytes)
		for idx, chunk := range named.Payload.Chunks {
			tags[fmt.Sprintf("%s%03d", descriptorBase, idx)] = chunk
		}
	}

	// Discoverable summary list
	ids := make([]string, 0, len(allEncoded))
	for _, named := range allEncoded {
		ids = append(ids, named.ID)
	}
	tags["CASSINI_TRANSCRIPT_IDS"] = strings.Join(ids, ",")
	if defaultRawID != "" {
		tags["CASSINI_TRANSCRIPT_DEFAULT"] = defaultRawID
	}

	return tags
}

// transcriptMIMEFor looks up the MIME label by walking both lists. Readable
// and display bodies use the readable MIME; raw-ASR / human-corrected /
// translation use the words MIME.
func transcriptMIMEFor(id string, encoded EncodedMultiTranscriptManifest) string {
	for _, named := range encoded.ReadableTranscripts {
		if named.ID == id {
			return TranscriptBodyMIMEReadable
		}
	}
	return TranscriptBodyMIMEWords
}
