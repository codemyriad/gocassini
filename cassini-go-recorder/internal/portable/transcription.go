package portable

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const TranscriptionFormat = "cassini.transcription.v1"
const MaxTranscriptionBytes = 64 << 20
const maxTranscriptionDepth = 64

type TranscriptionPayload struct {
	Encoding   string `json:"encoding"`
	DataBase64 string `json:"dataBase64"`
	SHA256     string `json:"sha256"`
	RawBytes   int    `json:"rawBytes"`
	MIME       string `json:"mime"`
}
type AnnotationCheckpoint struct {
	StateToken string `json:"stateToken"`
	Revision   int64  `json:"revision"`
}
type TranscriptionOptions struct {
	DocumentID     string
	AgeAnchor      time.Time
	AnchorSource   string
	EvictedAt      time.Time
	PolicyRevision int64
	Checkpoint     AnnotationCheckpoint
}
type PreservationEntry struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}
type PreservationReport struct {
	Preserved     []PreservationEntry `json:"preserved"`
	ExcludedAudio []string            `json:"excludedAudio"`
}

func transcriptionDigest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// ReadOpusComments retains duplicate comments and the vendor verbatim. The
// complete stream is validated, including checksums and canonical audio hash.
func ReadOpusComments(reader io.Reader) (OpusAudioIntegrity, []string, string, error) {
	var comments []string
	var vendor string
	integrity, err := computeOpusAudioIntegrity(reader, func(packet []byte) error {
		r := bytes.NewReader(packet[8:])
		readString := func() (string, error) {
			var n uint32
			if err := binary.Read(r, binary.LittleEndian, &n); err != nil {
				return "", err
			}
			if uint64(n) > uint64(r.Len()) {
				return "", fmt.Errorf("invalid Opus comment length")
			}
			b := make([]byte, int(n))
			_, err := io.ReadFull(r, b)
			if !utf8.Valid(b) {
				return "", fmt.Errorf("non-UTF8 Opus metadata is unsupported")
			}
			return string(b), err
		}
		var err error
		vendor, err = readString()
		if err != nil {
			return err
		}
		var count uint32
		if err = binary.Read(r, binary.LittleEndian, &count); err != nil {
			return err
		}
		if count > uint32(r.Len()/4) {
			return fmt.Errorf("invalid Opus comment count")
		}
		for i := uint32(0); i < count; i++ {
			s, err := readString()
			if err != nil {
				return err
			}
			comments = append(comments, s)
		}
		// Unknown nonzero trailing extensions could carry media. Never drop them.
		trailing, _ := io.ReadAll(r)
		for _, b := range trailing {
			if b != 0 {
				return fmt.Errorf("unsupported OpusTags extension")
			}
		}
		return nil
	})
	return integrity, comments, vendor, err
}

func checkedJSON(raw []byte) error {
	if len(raw) > MaxTranscriptionBytes || !utf8.Valid(raw) {
		return fmt.Errorf("JSON exceeds size limit or is not UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var walk func(int) error
	walk = func(depth int) error {
		if depth > maxTranscriptionDepth {
			return fmt.Errorf("JSON nesting exceeds limit")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				k, ok := key.(string)
				if !ok || seen[k] {
					return fmt.Errorf("duplicate or invalid JSON key")
				}
				seen[k] = true
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := walk(depth + 1); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("invalid JSON delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := walk(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func decodeRetentionChunks(tags map[string]string, ref PayloadRef) ([]byte, error) {
	if ref.ChunkCount <= 0 || ref.ChunkCount > MaxTranscriptionBytes/4 || ref.RawBytes < 0 || ref.RawBytes > MaxTranscriptionBytes || ref.GzipBytes <= 0 || ref.GzipBytes > MaxTranscriptionBytes || ref.Encoding != PayloadEncoding {
		return nil, fmt.Errorf("invalid payload bounds/encoding for %s", ref.Prefix)
	}
	var encoded strings.Builder
	for i := 0; i < ref.ChunkCount; i++ {
		s, ok := tags[fmt.Sprintf("%s%03d", ref.Prefix, i)]
		if !ok {
			return nil, fmt.Errorf("missing payload chunk")
		}
		if encoded.Len()+len(s) > MaxTranscriptionBytes {
			return nil, fmt.Errorf("encoded payload too large")
		}
		encoded.WriteString(s)
	}
	compressed, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(encoded.String(), "="))
	if err != nil {
		return nil, err
	}
	if len(compressed) != ref.GzipBytes {
		return nil, fmt.Errorf("compressed size mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, err
	}
	defer gz.Close()
	raw, err := io.ReadAll(io.LimitReader(gz, MaxTranscriptionBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) != ref.RawBytes || transcriptionDigest(raw) != ref.SHA256 {
		return nil, fmt.Errorf("payload size/hash mismatch")
	}
	if err := checkedJSON(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// ExportTranscription accounts for every comment and referenced payload. It
// refuses unsupported embedded media rather than silently discarding metadata.
func ExportTranscription(reader io.Reader, opts TranscriptionOptions) ([]byte, PreservationReport, error) {
	var report PreservationReport
	fail := func(err error) ([]byte, PreservationReport, error) { return nil, report, err }
	if opts.AgeAnchor.IsZero() || opts.EvictedAt.IsZero() || opts.AnchorSource == "" {
		return fail(fmt.Errorf("retention dates and anchor provenance required"))
	}
	integrity, comments, vendor, err := ReadOpusComments(reader)
	if err != nil {
		return fail(err)
	}
	tags := map[string]string{}
	for _, comment := range comments {
		k, v, ok := strings.Cut(comment, "=")
		if !ok {
			continue
		}
		k = strings.ToUpper(k)
		if strings.HasPrefix(k, "CASSINI_") {
			if _, exists := tags[k]; exists {
				return fail(fmt.Errorf("duplicate Cassini tag %s", k))
			}
		}
		tags[k] = v
	}
	if tags["CASSINI_FORMAT"] != Format || tags["CASSINI_AUDIO_OPUS_SHA256"] != integrity.SHA256 {
		return fail(fmt.Errorf("unsupported portable format or incorrect audio identity"))
	}
	integer := func(key string) int { n, _ := strconv.Atoi(tags[key]); return n }
	mainRef := PayloadRef{Prefix: "CASSINI_PAYLOAD_", ChunkCount: integer("CASSINI_PAYLOAD_CHUNK_COUNT"), RawBytes: integer("CASSINI_PAYLOAD_RAW_BYTES"), GzipBytes: integer("CASSINI_PAYLOAD_GZIP_BYTES"), SHA256: tags["CASSINI_PAYLOAD_SHA256"], Encoding: tags["CASSINI_PAYLOAD_ENCODING"]}
	raw, err := decodeRetentionChunks(tags, mainRef)
	if err != nil {
		return fail(err)
	}
	var manifest map[string]json.RawMessage
	if err = json.Unmarshal(raw, &manifest); err != nil {
		return fail(err)
	}
	decoded, err := DecodePublishedManifest(raw)
	if err != nil {
		return fail(err)
	}
	if decoded.Integrity.OpusSHA256 != integrity.SHA256 || decoded.Integrity.SampleCount != integrity.SampleCount || decoded.Audio.SampleCount != integrity.SampleCount || decoded.Audio.DurationMS != integrity.DurationMS {
		return fail(fmt.Errorf("manifest audio identity mismatch"))
	}
	if len(decoded.Annotations) > 0 {
		if _, err := ParseAnnotations(decoded.Annotations); err != nil {
			return fail(err)
		}
	}
	// Only known non-media attachment types are safe to retain. Unknown binary
	// types block conversion; the source remains untouched by the caller.
	var attachments []map[string]json.RawMessage
	if len(manifest["attachments"]) > 0 {
		if err = json.Unmarshal(manifest["attachments"], &attachments); err != nil {
			return fail(err)
		}
	}
	for _, a := range attachments {
		var mime string
		json.Unmarshal(a["mime"], &mime)
		if mime == "" {
			json.Unmarshal(a["mimeType"], &mime)
		}
		if !strings.HasPrefix(mime, "text/") && mime != "application/json" {
			return fail(fmt.Errorf("unsupported attachment media type %q", mime))
		}
		var content string
		json.Unmarshal(a["contentBase64"], &content)
		b, err := base64.StdEncoding.DecodeString(content)
		if err != nil || len(b) > MaxTranscriptionBytes {
			return fail(fmt.Errorf("invalid attachment"))
		}
	}
	payloads := map[string]TranscriptionPayload{}
	consumed := map[string]bool{}
	markChunks := func(ref PayloadRef) {
		for i := 0; i < ref.ChunkCount; i++ {
			consumed[fmt.Sprintf("%s%03d", ref.Prefix, i)] = true
		}
	}
	markChunks(mainRef)
	total := len(raw)
	for _, field := range []string{"transcripts", "readableTranscripts"} {
		var entries []struct {
			PayloadRef PayloadRef `json:"payloadRef"`
		}
		if b := manifest[field]; len(b) > 0 {
			if err := json.Unmarshal(b, &entries); err != nil {
				return fail(err)
			}
		}
		for _, entry := range entries {
			ref := entry.PayloadRef
			if !strings.HasPrefix(ref.Prefix, "CASSINI_TX_") || !strings.HasSuffix(ref.Prefix, "_PAYLOAD_") {
				return fail(fmt.Errorf("unsupported payload prefix"))
			}
			b, err := decodeRetentionChunks(tags, ref)
			if err != nil {
				return fail(err)
			}
			total += len(b)
			if total > MaxTranscriptionBytes {
				return fail(fmt.Errorf("total retained metadata too large"))
			}
			payloads[ref.Prefix] = TranscriptionPayload{"base64", base64.StdEncoding.EncodeToString(b), transcriptionDigest(b), len(b), ref.MIME}
			report.Preserved = append(report.Preserved, PreservationEntry{ref.Prefix, transcriptionDigest(b), len(b)})
			markChunks(ref)
		}
	}
	extra := []string{}
	for _, comment := range comments {
		key, _, _ := strings.Cut(comment, "=")
		key = strings.ToUpper(key)
		if consumed[key] {
			continue
		}
		if key == "METADATA_BLOCK_PICTURE" || key == "COVERART" {
			return fail(fmt.Errorf("unsupported embedded picture"))
		}
		// Unknown chunk sets might carry media; don't hide them among extra tags.
		if strings.Contains(key, "_PAYLOAD_") {
			suffix := key[strings.LastIndex(key, "_PAYLOAD_")+9:]
			if _, err := strconv.Atoi(suffix); err == nil {
				return fail(fmt.Errorf("unreferenced payload chunk %s", key))
			}
		}
		extra = append(extra, comment)
	}
	if opts.DocumentID == "" {
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return fail(err)
		}
		opts.DocumentID = hex.EncodeToString(id[:])
	}
	document := map[string]any{
		"format":    TranscriptionFormat,
		"identity":  map[string]any{"meetingId": decoded.Meeting.ID, "documentId": opts.DocumentID, "originalAudioSha256": integrity.SHA256},
		"media":     map[string]any{"state": "evicted", "reason": "retention", "evictedAt": opts.EvictedAt.UTC().Format(time.RFC3339Nano), "durationMs": decoded.Meeting.DurationMS},
		"retention": map[string]any{"ageAnchor": opts.AgeAnchor.UTC().Format(time.RFC3339Nano), "anchorSource": opts.AnchorSource, "policyRevision": opts.PolicyRevision},
		"current":   map[string]any{"sourceManifest": TranscriptionPayload{"base64", base64.StdEncoding.EncodeToString(raw), transcriptionDigest(raw), len(raw), "application/json"}, "manifest": json.RawMessage(raw), "payloads": payloads, "extraTags": extra, "vendor": vendor, "annotationCheckpoint": opts.Checkpoint},
	}
	output, err := json.Marshal(document)
	if err != nil {
		return fail(err)
	}
	if len(output) > MaxTranscriptionBytes {
		return fail(fmt.Errorf("retained document too large"))
	}
	report.Preserved = append(report.Preserved, PreservationEntry{"manifest", transcriptionDigest(raw), len(raw)})
	report.ExcludedAudio = []string{"OpusHead and all Opus audio packets"}
	return output, report, nil
}

// ReadTranscription validates a retained document without interpreting unknown
// members away. Callers retain Raw for surgical annotation updates.
type TranscriptionDocument struct {
	Raw         map[string]json.RawMessage
	ManifestRaw json.RawMessage
	Manifest    Manifest
	Payloads    map[string]TranscriptionPayload
	Checkpoint  AnnotationCheckpoint
}

func ReadTranscription(raw []byte) (TranscriptionDocument, error) {
	var doc TranscriptionDocument
	if err := checkedJSON(raw); err != nil {
		return doc, err
	}
	if err := json.Unmarshal(raw, &doc.Raw); err != nil {
		return doc, err
	}
	var format string
	json.Unmarshal(doc.Raw["format"], &format)
	if format != TranscriptionFormat {
		return doc, fmt.Errorf("unsupported transcription format")
	}
	var media struct {
		State     string `json:"state"`
		Reason    string `json:"reason"`
		EvictedAt string `json:"evictedAt"`
		Duration  int64  `json:"durationMs"`
	}
	if err := json.Unmarshal(doc.Raw["media"], &media); err != nil {
		return doc, err
	}
	if media.State != "evicted" || media.Reason != "retention" || media.Duration < 0 {
		return doc, fmt.Errorf("invalid retained media state")
	}
	if _, err := time.Parse(time.RFC3339Nano, media.EvictedAt); err != nil {
		return doc, err
	}
	var current struct {
		Manifest       json.RawMessage                 `json:"manifest"`
		Payloads       map[string]TranscriptionPayload `json:"payloads"`
		Checkpoint     AnnotationCheckpoint            `json:"annotationCheckpoint"`
		SourceManifest *TranscriptionPayload           `json:"sourceManifest"`
	}
	if err := json.Unmarshal(doc.Raw["current"], &current); err != nil {
		return doc, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(doc.Raw["current"], &fields); err != nil {
		return doc, err
	}
	for _, key := range []string{"manifest", "payloads", "extraTags", "vendor", "annotationCheckpoint"} {
		if _, ok := fields[key]; !ok {
			return doc, fmt.Errorf("missing retained %s", key)
		}
	}
	var extraTags []string
	var vendor string
	if err := json.Unmarshal(fields["extraTags"], &extraTags); err != nil {
		return doc, err
	}
	if err := json.Unmarshal(fields["vendor"], &vendor); err != nil {
		return doc, err
	}
	if current.Checkpoint.Revision < 0 {
		return doc, fmt.Errorf("invalid annotation checkpoint")
	}
	if current.SourceManifest != nil {
		if _, err := current.SourceManifest.Bytes(); err != nil {
			return doc, err
		}
	}
	manifest, err := DecodePublishedManifest(current.Manifest)
	if err != nil {
		return doc, err
	}
	var identity struct {
		MeetingID  string `json:"meetingId"`
		DocumentID string `json:"documentId"`
		Audio      string `json:"originalAudioSha256"`
	}
	if err := json.Unmarshal(doc.Raw["identity"], &identity); err != nil {
		return doc, err
	}
	if identity.MeetingID != manifest.Meeting.ID || identity.DocumentID == "" || identity.Audio != manifest.Integrity.OpusSHA256 {
		return doc, fmt.Errorf("retained identity disagrees with manifest")
	}
	var retention struct {
		Anchor string `json:"ageAnchor"`
		Source string `json:"anchorSource"`
	}
	if err := json.Unmarshal(doc.Raw["retention"], &retention); err != nil {
		return doc, err
	}
	if _, err := time.Parse(time.RFC3339Nano, retention.Anchor); err != nil || retention.Source == "" {
		return doc, fmt.Errorf("invalid immutable retention anchor")
	}
	total := 0
	for key, p := range current.Payloads {
		b, err := p.Bytes()
		if err != nil {
			return doc, fmt.Errorf("payload %s: %w", key, err)
		}
		total += len(b)
		if total > MaxTranscriptionBytes {
			return doc, fmt.Errorf("payload total exceeds limit")
		}
	}
	var descriptors struct {
		Transcripts []TranscriptEntry `json:"transcripts"`
		Readable    []TranscriptEntry `json:"readableTranscripts"`
	}
	if err := json.Unmarshal(current.Manifest, &descriptors); err != nil {
		return doc, err
	}
	for _, entry := range append(descriptors.Transcripts, descriptors.Readable...) {
		p, ok := current.Payloads[entry.PayloadRef.Prefix]
		if !ok || p.SHA256 != entry.PayloadRef.SHA256 || p.RawBytes != entry.PayloadRef.RawBytes || p.MIME != entry.PayloadRef.MIME {
			return doc, fmt.Errorf("missing or inconsistent payload %s", entry.ID)
		}
	}
	doc.ManifestRaw = current.Manifest
	doc.Manifest = manifest
	doc.Payloads = current.Payloads
	doc.Checkpoint = current.Checkpoint
	return doc, nil
}

func (p TranscriptionPayload) Bytes() ([]byte, error) {
	if p.Encoding != "base64" || p.RawBytes < 0 || p.RawBytes > MaxTranscriptionBytes || len(p.DataBase64) > MaxTranscriptionBytes*4/3+4 {
		return nil, fmt.Errorf("invalid payload bounds/encoding")
	}
	b, err := base64.StdEncoding.DecodeString(p.DataBase64)
	if err != nil {
		return nil, err
	}
	if len(b) != p.RawBytes || transcriptionDigest(b) != p.SHA256 {
		return nil, fmt.Errorf("payload size/hash mismatch")
	}
	if err := checkedJSON(b); err != nil {
		return nil, err
	}
	return b, nil
}

// RewriteTranscriptionAnnotations retains unknown envelope and manifest members,
// payload bytes and original audio binding. Conversion itself never bumps revision.
func RewriteTranscriptionAnnotations(raw, annotations json.RawMessage, checkpoint AnnotationCheckpoint) ([]byte, error) {
	doc, err := ReadTranscription(raw)
	if err != nil {
		return nil, err
	}
	parsed, err := ParseAnnotations(annotations)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, fmt.Errorf("annotations are required")
	}
	var manifest map[string]json.RawMessage
	json.Unmarshal(doc.ManifestRaw, &manifest)
	manifest["annotations"] = annotations
	var current map[string]json.RawMessage
	json.Unmarshal(doc.Raw["current"], &current)
	current["manifest"], err = json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	current["annotationCheckpoint"], err = json.Marshal(checkpoint)
	if err != nil {
		return nil, err
	}
	doc.Raw["current"], err = json.Marshal(current)
	if err != nil {
		return nil, err
	}
	output, err := json.Marshal(doc.Raw)
	if err != nil {
		return nil, err
	}
	if _, err = ReadTranscription(output); err != nil {
		return nil, err
	}
	return output, nil
}

// RefreshTranscription replaces generated meeting data for the same audio while
// preserving the retirement boundary and acknowledged annotations. The previous
// current object remains embedded verbatim as history, including unknown fields.
// A changed-audio publication is not a representation conversion.
func RefreshTranscription(existing, replacement []byte) ([]byte, error) {
	old, err := ReadTranscription(existing)
	if err != nil {
		return nil, err
	}
	fresh, err := ReadTranscription(replacement)
	if err != nil {
		return nil, err
	}
	if old.Manifest.Integrity.OpusSHA256 != fresh.Manifest.Integrity.OpusSHA256 || old.Manifest.Meeting.ID != fresh.Manifest.Meeting.ID {
		return nil, fmt.Errorf("retained publication requires the same meeting and audio identity")
	}
	var manifest, previous map[string]json.RawMessage
	json.Unmarshal(fresh.ManifestRaw, &manifest)
	json.Unmarshal(old.ManifestRaw, &previous)
	for key, value := range previous {
		if _, ok := manifest[key]; !ok {
			manifest[key] = value
		}
	}
	if annotations, ok := previous["annotations"]; ok {
		manifest["annotations"] = annotations
	} else {
		delete(manifest, "annotations")
	}
	var current map[string]json.RawMessage
	json.Unmarshal(fresh.Raw["current"], &current)
	var prior map[string]json.RawMessage
	json.Unmarshal(old.Raw["current"], &prior)
	for key, value := range prior {
		if _, ok := current[key]; !ok {
			current[key] = value
		}
	}
	current["annotationCheckpoint"] = prior["annotationCheckpoint"]
	current["manifest"], err = json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	var history []json.RawMessage
	if raw, ok := old.Raw["publicationHistory"]; ok {
		if err := json.Unmarshal(raw, &history); err != nil {
			return nil, fmt.Errorf("unsupported publication history: %w", err)
		}
	}
	history = append(history, old.Raw["current"])
	old.Raw["publicationHistory"], err = json.Marshal(history)
	if err != nil {
		return nil, err
	}
	old.Raw["current"], err = json.Marshal(current)
	if err != nil {
		return nil, err
	}
	output, err := json.Marshal(old.Raw)
	if err != nil {
		return nil, err
	}
	if _, err = ReadTranscription(output); err != nil {
		return nil, err
	}
	return output, nil
}
