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
	manifest, err := DecodePublishedManifest(compact.Bytes())
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
	entries := append(append([]TranscriptEntry{}, manifest.Transcripts...), manifest.ReadableTranscripts...)
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
		// Reproduce the original chunk layout, including files using a custom chunk
		// size. Descriptor counts/bytes describe the original compressed payload.
		prefix := entry.PayloadRef.Prefix
		if err := putJSONPayload(tags, prefix, compact.Bytes()); err != nil {
			return nil, err
		}
		if encoded.CompressedBytes != entry.PayloadRef.GzipBytes {
			return nil, fmt.Errorf("transcript %s compressed size mismatch", entry.ID)
		}
		if entry.PayloadRef.ChunkCount != len(encoded.Chunks) {
			return nil, fmt.Errorf("unsupported transcript chunk layout")
		}
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
	manifest, err := DecodePublishedManifest(raw)
	if err != nil {
		return nil, err
	}
	doc := Transcription{Kind: TranscriptionKind, Version: 1, Source: raw, Bodies: map[string]json.RawMessage{}, Tags: map[string]string{}}
	for k, v := range normalized {
		if !strings.HasPrefix(k, "CASSINI_PAYLOAD_") && !(strings.HasPrefix(k, "CASSINI_TX_") && strings.Contains(k, "_PAYLOAD_")) {
			doc.Tags[k] = v
		}
	}
	for _, entry := range append(append([]TranscriptEntry{}, manifest.Transcripts...), manifest.ReadableTranscripts...) {
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
	if _, err = DecodeTranscription(result); err != nil {
		return nil, err
	}
	return append(result, '\n'), nil
}
