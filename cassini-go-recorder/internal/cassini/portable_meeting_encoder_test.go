package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gocassini/internal/transcribe"
)

// TestPackedPortableMeetingCarriesEncoderProvenance builds the artifact
// manifest with the build's own writer, packs it, and reads the published
// wire manifest back as raw JSON. The encoder record (which ONNX export
// actually loaded) and the native frontend are written by every Parakeet
// build; the packer reads them through typed structs, so a field
// those structs do not declare is silently dropped at pack time and never
// reaches the .opus the schema says carries it.
func TestPackedPortableMeetingCarriesEncoderProvenance(t *testing.T) {
	requireFFMediaTools(t)
	for _, tc := range []struct {
		name  string
		extra []transcribe.AdditionalTranscript
	}{
		{name: "one transcript"},
		// With a second model's transcript the build writes the primary's
		// step into files.transcripts[] as well, a separate typed path.
		{name: "several transcripts", extra: []transcribe.AdditionalTranscript{{
			ID: "second", Path: "transcript-second.words.v1.json", ModelID: "parakeet-tdt-0.6b-v2-int8",
		}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			outPath := packEncoderProvenanceFixture(t, tc.extra)
			tags, err := portableMeetingTags(outPath)
			if err != nil {
				t.Fatalf("read tags: %v", err)
			}
			rawJSON, err := decodePortableMeetingPayload(tags)
			if err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			var document struct {
				Transcripts []struct {
					ID      string `json:"id"`
					Default bool   `json:"default"`
				} `json:"transcripts"`
				Provenance struct {
					SpeechToText map[string]map[string]any `json:"speechToText"`
				} `json:"provenance"`
			}
			if err := json.Unmarshal(rawJSON, &document); err != nil {
				t.Fatalf("parse payload: %v", err)
			}
			primary := ""
			for _, entry := range document.Transcripts {
				if entry.Default {
					primary = entry.ID
				}
			}
			step := document.Provenance.SpeechToText[primary]
			if step == nil {
				t.Fatalf("no provenance.speechToText for the default transcript %q: %s", primary, rawJSON)
			}
			encoder, _ := step["encoder"].(map[string]any)
			if encoder["url"] != encoderTestURL || encoder["comment"] != "cassini export" {
				t.Errorf("speechToText[%s].encoder = %v, want the encoder's url and comment", primary, step["encoder"])
			}
			if step["nativeFrontend"] != transcribe.NativeFrontendNeMoLogGuard {
				t.Errorf("speechToText[%s].nativeFrontend = %v, want %q", primary, step["nativeFrontend"], transcribe.NativeFrontendNeMoLogGuard)
			}

			typed := decodePortableManifestFromOpus(t, outPath)
			if typed.Provenance == nil || typed.Provenance.SpeechToText == nil ||
				typed.Provenance.SpeechToText.Encoder == nil || typed.Provenance.SpeechToText.Encoder.URL != encoderTestURL {
				t.Errorf("typed read path lost the encoder: %+v", typed.Provenance)
			}
		})
	}
}

const encoderTestURL = "https://example.invalid/parakeet-ultra-v1"

// packEncoderProvenanceFixture writes an Ultra build's manifest with the
// build's own writer and packs the bundle, returning the .opus path.
func packEncoderProvenanceFixture(t *testing.T, extra []transcribe.AdditionalTranscript) string {
	t.Helper()
	tmp := t.TempDir()
	bundleDir := filepath.Join(tmp, "ultra.meeting")
	manifestPath := filepath.Join(tmp, "manifest.json")
	if err := transcribe.WriteManifest(manifestPath, transcribe.ManifestInput{
		SrcBasename:      "source.mkv",
		SrcDurationMS:    250,
		DigestDurationMS: 250,
		STTBackend:       "sherpa-onnx-go",
		STTModelID:       "parakeet-ultra-0.6b-int8",
		STTDevice:        "cpu",
		NativeFrontend:   transcribe.NativeFrontendNeMoLogGuard,
		Encoder:          &transcribe.EncoderProvenance{URL: encoderTestURL, Comment: "cassini export"},
		Additional:       extra,
	}); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	// The build's manifest names captions.vtt as well.
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundleDir, "captions.vtt"), []byte("WEBVTT\n\n00:00:00.000 --> 00:00:00.200\nhello team\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var extraPaths []string
	for _, e := range extra {
		extraPaths = append(extraPaths, e.Path)
	}
	if err := writeMeetingBundleFixtureWithManifest(bundleDir, string(manifest), extraPaths...); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	outPath := filepath.Join(tmp, "ultra.opus")
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"pack", bundleDir, "--out", outPath}, &stdout, &stderr); code != 0 {
		t.Fatalf("pack failed code=%d stderr=%q", code, stderr.String())
	}
	return outPath
}
