package cassini

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"gocassini/internal/portable"
)

// Reading a portable meeting file back.
//
// `cassini pack` writes one; `cassini inspect` prints one; `cassini retag`
// edits one. All three need the same two steps — pull the OpusTags off the
// container, then turn the CASSINI_PAYLOAD_* chunk set back into the manifest —
// and this is the one implementation of them on the recorder side.
//
// The payload is decoded twice, into two different shapes, on purpose:
//
//   - portable.Manifest, for anything that wants to READ a named field.
//   - the raw JSON document, for anything that wants to WRITE one.
//
// The second exists because the file is not necessarily the shape this binary
// would write today. A v2 file carries per-transcript descriptor arrays and a
// provenance map that portable.Manifest flattens; a file written by a future
// producer may carry keys this build has never heard of. Re-marshalling a
// decoded struct would silently drop every one of them. Editing the JSON
// document in place drops nothing, and is the same technique the operator uses
// on a bundle manifest for the same reason (setMeetingBundleFields).

// portableMeetingTags reads every OpusTag off a portable meeting file.
//
// Ogg carries its comments on the stream and other muxers on the format, so
// both are read and merged. Format tags win a collision, matching the ordering
// `cassini inspect` already uses.
func portableMeetingTags(path string) (map[string]string, error) {
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-show_entries", "format_tags:stream_tags",
		"-of", "json",
		path,
	)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("probe portable meeting tags: %w", err)
	}
	var probed struct {
		Format struct {
			Tags map[string]string `json:"tags"`
		} `json:"format"`
		Streams []struct {
			Tags map[string]string `json:"tags"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(output, &probed); err != nil {
		return nil, fmt.Errorf("parse portable meeting tags: %w", err)
	}
	tags := map[string]string{}
	for _, stream := range probed.Streams {
		for key, value := range stream.Tags {
			tags[key] = value
		}
	}
	for key, value := range probed.Format.Tags {
		tags[key] = value
	}
	if len(tags) == 0 {
		return nil, fmt.Errorf("no metadata tags in %s", path)
	}
	return tags, nil
}

// portableTagValue looks a tag up case-insensitively.
//
// Not a convenience: ffprobe reports Vorbis comment keys in whatever case the
// muxer wrote them, and different ffmpeg builds disagree. A case-sensitive
// lookup here reads a present tag as absent.
func portableTagValue(tags map[string]string, name string) string {
	if value, ok := tags[name]; ok {
		return strings.TrimSpace(value)
	}
	for key, value := range tags {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

// decodePortableMeetingPayload turns the CASSINI_PAYLOAD_* chunk set back into
// the manifest JSON exactly as it was written.
//
// Returns the raw bytes rather than a parsed value, because its two callers
// want different things from them and only one of them wants a struct.
func decodePortableMeetingPayload(tags map[string]string) ([]byte, error) {
	rawCount := portableTagValue(tags, "CASSINI_PAYLOAD_CHUNK_COUNT")
	chunkCount, err := strconv.Atoi(rawCount)
	if err != nil || chunkCount <= 0 {
		return nil, fmt.Errorf("not a portable meeting file: missing or invalid CASSINI_PAYLOAD_CHUNK_COUNT (%q)", rawCount)
	}

	var encoded strings.Builder
	for idx := 0; idx < chunkCount; idx++ {
		key := fmt.Sprintf("CASSINI_PAYLOAD_%03d", idx)
		chunk := portableTagValue(tags, key)
		if chunk == "" {
			return nil, fmt.Errorf("portable meeting payload is incomplete: %s is missing", key)
		}
		encoded.WriteString(chunk)
	}

	compressed, err := base64.RawURLEncoding.DecodeString(encoded.String())
	if err != nil {
		return nil, fmt.Errorf("decode base64url portable meeting payload: %w", err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("open gzip portable meeting payload: %w", err)
	}
	defer func() { _ = reader.Close() }()
	rawJSON, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("decompress gzip portable meeting payload: %w", err)
	}

	// The digest is checked when the file claims one. A payload that survived
	// base64 and gzip but does not match its own digest is a corrupted file,
	// and reading fields out of it would be reading fields out of noise.
	if declared := strings.ToLower(portableTagValue(tags, "CASSINI_PAYLOAD_SHA256")); declared != "" {
		sum := sha256.Sum256(rawJSON)
		if actual := hex.EncodeToString(sum[:]); actual != declared {
			return nil, fmt.Errorf("portable meeting payload sha256 mismatch: file says %s, contents are %s", declared, actual)
		}
	}
	return rawJSON, nil
}

// readPortableMeetingManifest is the read-a-field path: tags, payload, struct.
func readPortableMeetingManifest(path string) (portable.Manifest, map[string]string, error) {
	tags, err := portableMeetingTags(path)
	if err != nil {
		return portable.Manifest{}, nil, err
	}
	rawJSON, err := decodePortableMeetingPayload(tags)
	if err != nil {
		return portable.Manifest{}, nil, err
	}
	var manifest portable.Manifest
	if err := json.Unmarshal(rawJSON, &manifest); err != nil {
		return portable.Manifest{}, nil, fmt.Errorf("parse portable meeting manifest: %w", err)
	}
	return manifest, tags, nil
}

// decodePortableMeetingDocument is the edit-a-field path: the payload as a
// generic JSON document, with every number kept as the literal text it was
// written as.
//
// UseNumber is load-bearing rather than tidy. Without it every number in the
// document becomes a float64 and is re-marshalled from that float — so a
// sample count, a duration or a byte offset comes back as whatever the round
// trip produced. The manifest's whole purpose is to be checkable against the
// audio, and a rewrite that quietly perturbs its integrity numbers defeats it.
func decodePortableMeetingDocument(rawJSON []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(rawJSON))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("parse portable meeting manifest: %w", err)
	}
	if document == nil {
		return nil, fmt.Errorf("portable meeting manifest is not a JSON object")
	}
	return document, nil
}
