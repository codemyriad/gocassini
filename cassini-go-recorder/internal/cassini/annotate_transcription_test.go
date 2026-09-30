package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"gocassini/internal/portable"
)

func TestAnnotateRetainedTranscriptionCheckpoint(t *testing.T) {
	input := filepath.Join("..", "..", "..", "spec", "fixtures", "retained-meeting.json")
	result, err := annotateShow(input, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if result.AudioOpusSHA256 == "" {
		t.Fatal("lost historical audio identity")
	}
	doc := portable.Annotations{Format: portable.AnnotationsFormatV1, Revision: 1, TagNamespace: "urn:uuid:12345678-1234-4234-8234-123456789abc", AudioOpusSHA256: result.AudioOpusSHA256}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "out.cassini.transcription.json")
	var stdout, stderr bytes.Buffer
	if code := runAnnotateSnapshot(context.Background(), []string{"--out", output, "--state-token", "same-generation:99", "--json", input}, bytes.NewReader(raw), &stdout, &stderr); code != 0 {
		t.Fatalf("code %d: %s", code, stderr.String())
	}
	written, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := portable.ReadTranscription(written)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Checkpoint.StateToken != "same-generation:99" || parsed.Checkpoint.Revision != 1 {
		t.Fatalf("checkpoint: %+v", parsed.Checkpoint)
	}
	if !bytes.Contains(written, []byte("preserve me")) || len(parsed.Payloads) != 2 {
		t.Fatal("metadata lost")
	}
}
