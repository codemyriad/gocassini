package transcribe

import (
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func protoField(field int, payload []byte) []byte {
	out := binary.AppendUvarint(nil, uint64(field<<3|2))
	out = binary.AppendUvarint(out, uint64(len(payload)))
	return append(out, payload...)
}

func protoEntry(key, value string) []byte {
	return protoField(14, append(protoField(1, []byte(key)), protoField(2, []byte(value))...))
}

// writeFakeEncoder lays out a ModelProto the way an exported encoder does: an
// ir_version varint, a large graph, then metadata_props.
func writeFakeEncoder(t *testing.T, entries ...[]byte) string {
	t.Helper()
	b := binary.AppendUvarint(nil, 1<<3|0)
	b = binary.AppendUvarint(b, 8)
	b = append(b, protoField(7, make([]byte, 300000))...)
	for _, e := range entries {
		b = append(b, e...)
	}
	path := filepath.Join(t.TempDir(), "encoder.onnx")
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestEncoderProvenanceReadsURLAndComment(t *testing.T) {
	path := writeFakeEncoder(t,
		protoEntry("model_type", "EncDecRNNTBPEModel"),
		protoEntry("url", "https://huggingface.co/moondream/parakeet-ultra"),
		protoEntry("comment", "Weights: Moondream Parakeet Ultra rev 73175eb7"),
	)
	got := encoderProvenance(path)
	if got == nil || got.URL != "https://huggingface.co/moondream/parakeet-ultra" || got.Comment != "Weights: Moondream Parakeet Ultra rev 73175eb7" {
		t.Fatalf("got %+v", got)
	}
}

func TestEncoderProvenanceBoundsCommentAndToleratesMissing(t *testing.T) {
	path := writeFakeEncoder(t, protoEntry("comment", strings.Repeat("x", 5000)))
	if got := encoderProvenance(path); got == nil || len(got.Comment) != maxEncoderCommentBytes || got.URL != "" {
		t.Fatalf("got %+v", got)
	}
	if got := encoderProvenance(writeFakeEncoder(t, protoEntry("feat_dim", "128"))); got != nil {
		t.Fatalf("no url or comment: got %+v", got)
	}
	if got := encoderProvenance(filepath.Join(t.TempDir(), "missing.onnx")); got != nil {
		t.Fatalf("missing file: got %+v", got)
	}
	if got := encoderProvenance(""); got != nil {
		t.Fatalf("CTC model: got %+v", got)
	}
}

// The manifest's speech-to-text provenance carries the encoder identity, so a
// transcript records which export ran, not only which id was asked for.
func TestManifestRecordsEncoderProvenance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "manifest.json")
	enc := &EncoderProvenance{URL: "https://huggingface.co/moondream/parakeet-ultra", Comment: "c"}
	if err := WriteManifest(path, ManifestInput{STTBackend: "sherpa-onnx", STTModelID: ModelParakeetUltra, STTDevice: "cuda", Encoder: enc}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Provenance struct {
			SpeechToText struct {
				Encoder *EncoderProvenance `json:"encoder"`
			} `json:"speechToText"`
		} `json:"provenance"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if got := doc.Provenance.SpeechToText.Encoder; got == nil || *got != *enc {
		t.Fatalf("manifest encoder provenance = %+v\n%s", got, raw)
	}
}
