package portable

// A transcription document contains the complete metadata of a portable meeting,
// but no media. Source describes the recording from which its timeline was
// derived; its audio digest is lineage, not verification of available audio.
import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const TranscriptionKind = "cassini-transcription"

type Transcription struct {
	Kind    string                     `json:"kind"`
	Version int                        `json:"version"`
	Source  json.RawMessage            `json:"source"`
	Bodies  map[string]json.RawMessage `json:"bodies"`
	Tags    map[string]string          `json:"tags"`
}

func ReadTranscriptionTags(path string) (map[string]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DecodeTranscription(raw)
}

// DecodeTranscription restores the metadata transport used by existing readers.
// Payload hashes still verify each inline body after insignificant whitespace is
// removed. The outer kind distinguishes source audio metadata from playable media.
func DecodeTranscription(raw []byte) (map[string]string, error) {
	var doc Transcription
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	if doc.Kind != TranscriptionKind || doc.Version != 1 {
		return nil, fmt.Errorf("unsupported transcription document")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc.Source); err != nil {
		return nil, err
	}
	_, err := DecodePublishedManifest(compact.Bytes())
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	for k, v := range doc.Tags {
		tags[k] = v
	}
	if err := putJSONPayload(tags, "CASSINI_PAYLOAD_", compact.Bytes()); err != nil {
		return nil, err
	}
	entries, err := transcriptionEntries(doc.Source)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		raw, ok := doc.Bodies[entry.ID]
		if !ok {
			return nil, fmt.Errorf("missing transcript body %s", entry.ID)
		}
		compact.Reset()
		if err := json.Compact(&compact, raw); err != nil {
			return nil, err
		}
		encoded, err := EncodePayloadBytes(compact.Bytes(), DefaultPayloadChunkSize)
		if err != nil {
			return nil, err
		}
		if encoded.SHA256 != entry.PayloadRef.SHA256 || encoded.RawBytes != entry.PayloadRef.RawBytes {
			return nil, fmt.Errorf("transcript %s integrity mismatch", entry.ID)
		}
		// The JSON body hash is authoritative. Rebuild transport descriptors
		// for this adapter's compressor; JSON does not depend on gzip bytes.
		prefix := entry.PayloadRef.Prefix
		if err := putJSONPayload(tags, prefix, compact.Bytes()); err != nil {
			return nil, err
		}
		entry.PayloadRef.GzipBytes = encoded.CompressedBytes
		entry.PayloadRef.ChunkCount = len(encoded.Chunks)
		if err := updateTranscriptionRef(&doc.Source, entry); err != nil {
			return nil, err
		}
	}
	if err := putJSONPayload(tags, "CASSINI_PAYLOAD_", doc.Source); err != nil {
		return nil, err
	}
	return tags, nil
}

func putJSONPayload(tags map[string]string, prefix string, raw []byte) error {
	encoded, err := EncodePayloadBytes(raw, DefaultPayloadChunkSize)
	if err != nil {
		return err
	}
	tags[prefix+"SHA256"] = encoded.SHA256
	tags[prefix+"RAW_BYTES"] = strconv.Itoa(encoded.RawBytes)
	tags[prefix+"GZIP_BYTES"] = strconv.Itoa(encoded.CompressedBytes)
	tags[prefix+"CHUNK_COUNT"] = strconv.Itoa(len(encoded.Chunks))
	for i, chunk := range encoded.Chunks {
		tags[fmt.Sprintf("%s%03d", prefix, i)] = chunk
	}
	return nil
}

func payloadJSON(tags map[string]string, prefix string) ([]byte, error) {
	n, err := strconv.Atoi(tags[prefix+"CHUNK_COUNT"])
	if err != nil || n < 1 || n > 100000 {
		return nil, fmt.Errorf("invalid payload count")
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(tags[fmt.Sprintf("%s%03d", prefix, i)])
	}
	zipped, err := base64.RawURLEncoding.DecodeString(b.String())
	if err != nil {
		return nil, err
	}
	r, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

func EncodeTranscription(tags map[string]string) ([]byte, error) {
	normalized := map[string]string{}
	for k, v := range tags {
		normalized[strings.ToUpper(k)] = v
	}
	raw, err := payloadJSON(normalized, "CASSINI_PAYLOAD_")
	if err != nil {
		return nil, err
	}
	_, err = DecodePublishedManifest(raw)
	if err != nil {
		return nil, err
	}
	doc := Transcription{Kind: TranscriptionKind, Version: 1, Source: raw, Bodies: map[string]json.RawMessage{}, Tags: map[string]string{}}
	for k, v := range normalized {
		if !strings.HasPrefix(k, "CASSINI_PAYLOAD_") && !(strings.HasPrefix(k, "CASSINI_TX_") && strings.Contains(k, "_PAYLOAD_")) {
			doc.Tags[k] = v
		}
	}
	entries, err := transcriptionEntries(raw)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		body, err := payloadJSON(normalized, entry.PayloadRef.Prefix)
		if err != nil {
			return nil, err
		}
		doc.Bodies[entry.ID] = body
		for _, suffix := range []string{"MIME", "ENCODING"} {
			doc.Tags[entry.PayloadRef.Prefix+suffix] = normalized[entry.PayloadRef.Prefix+suffix]
		}
	}
	for _, suffix := range []string{"MIME", "ENCODING", "SCHEMA"} {
		doc.Tags["CASSINI_PAYLOAD_"+suffix] = normalized["CASSINI_PAYLOAD_"+suffix]
	}
	result, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	normalized, err = DecodeTranscription(result)
	if err != nil {
		return nil, err
	}
	doc.Source, err = payloadJSON(normalized, "CASSINI_PAYLOAD_")
	if err != nil {
		return nil, err
	}
	result, err = json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}

// IsTranscriptionFile recognizes the document by content, including a renamed
// Nextcloud share. File extensions are a discovery hint, not the wire identity.
func IsTranscriptionFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var header struct {
		Kind string `json:"kind"`
	}
	return json.NewDecoder(f).Decode(&header) == nil && header.Kind == TranscriptionKind
}

// Include unknown readable roles in the container round trip, even though the
// display reader does not render them. Their bodies must survive an edit.
func transcriptionEntries(raw []byte) ([]TranscriptEntry, error) {
	var source struct {
		Transcripts []json.RawMessage `json:"transcripts"`
		Readable    []json.RawMessage `json:"readableTranscripts"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	var entries []TranscriptEntry
	for _, raw := range append(source.Transcripts, source.Readable...) {
		var ref struct {
			ID         string     `json:"id"`
			PayloadRef PayloadRef `json:"payloadRef"`
		}
		if err := json.Unmarshal(raw, &ref); err != nil {
			return nil, err
		}
		if ref.ID != "" && ref.PayloadRef.Prefix != "" {
			entries = append(entries, TranscriptEntry{ID: ref.ID, PayloadRef: ref.PayloadRef})
		}
	}
	return entries, nil
}
func updateTranscriptionRef(raw *json.RawMessage, entry TranscriptEntry) error {
	var source map[string]json.RawMessage
	if err := json.Unmarshal(*raw, &source); err != nil {
		return err
	}
	for _, key := range []string{"transcripts", "readableTranscripts"} {
		if source[key] == nil {
			continue
		}
		var entries []map[string]json.RawMessage
		if err := json.Unmarshal(source[key], &entries); err != nil {
			return err
		}
		for _, fields := range entries {
			var id string
			if json.Unmarshal(fields["id"], &id) == nil && id == entry.ID {
				fields["payloadRef"], _ = json.Marshal(entry.PayloadRef)
			}
		}
		source[key], _ = json.Marshal(entries)
	}
	var err error
	*raw, err = json.Marshal(source)
	return err
}
