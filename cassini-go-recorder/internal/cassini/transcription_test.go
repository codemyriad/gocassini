package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gocassini/internal/inspect"
	"gocassini/internal/portable"
)

func TestTranscriptionRoundtripAndAnnotations(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	audio := packFixtureOpus(t, dir, "meeting")
	tags, err := portableMeetingTags(audio)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := portable.EncodeTranscription(tags)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "meeting.json")
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := inspect.ExtractMeeting(audio)
	if err != nil {
		t.Fatal(err)
	}
	after, err := inspect.ExtractMeeting(file)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("non-audio content changed")
	}
	out := filepath.Join(dir, "annotated.json")
	var stdout, stderr bytes.Buffer
	ops := filepath.Join(dir, "ops.json")
	os.WriteFile(ops, []byte(`{"ops":[{"op":"mark","tag":{"label":"important"},"target":{"kind":"meeting"}}]}`), 0600)
	code := Run(context.Background(), []string{"annotate", "apply", file, "--out", out, "--ops", ops, "--actor-id", "alice", "--json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("annotate: %d %s", code, stderr.String())
	}
	marked, err := inspect.ExtractMeeting(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(marked.Manifest.Annotations) == 0 {
		t.Fatal("missing annotations")
	}
	marked.Manifest.Annotations = nil
	if !reflect.DeepEqual(before, marked) {
		t.Fatal("annotation rewrite changed content")
	}
	if err := inspect.InspectPath(io.Discard, out); err != nil {
		t.Fatal(err)
	}
	var doc portable.Transcription
	json.Unmarshal(raw, &doc)
	for id := range doc.Bodies {
		doc.Bodies[id] = json.RawMessage(`{"corrupted":true}`)
		break
	}
	corrupt, _ := json.Marshal(doc)
	if _, err := portable.DecodeTranscription(corrupt); err == nil {
		t.Fatal("accepted corrupt body")
	}
}
