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
	renamed := filepath.Join(dir, "renamed.opus")
	if err := os.WriteFile(renamed, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := inspect.InspectPath(io.Discard, renamed); err != nil {
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

func TestTranscriptionPackSnapshotAndRepublishPreserveDisplayAndSummary(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	bundle := writeAnnotateBundle(t, dir, "meeting", 0)
	delivered := packAnnotateBundle(t, bundle, filepath.Join(dir, "delivered.json"))
	sealed := packAnnotateBundle(t, bundle, filepath.Join(dir, "sealed.json"), "--title", "Rerun")
	before, err := inspect.ExtractMeeting(delivered)
	if err != nil {
		t.Fatal(err)
	}
	if len(before.Manifest.ReadableTranscripts) == 0 || len(before.SummaryMarkdown) == 0 {
		t.Fatal("fixture must carry display and summary")
	}
	unchanged := manifestWithoutAnnotations(t, delivered)
	applied := applyInPlace(t, delivered, annotateTwoMarks)
	if !reflect.DeepEqual(unchanged, manifestWithoutAnnotations(t, delivered)) {
		t.Fatal("marking changed meeting content")
	}
	snapshot := filepath.Join(dir, "snapshot.json")
	annotateOK(t, string(applied.Annotations), "snapshot", "--out", snapshot, "--json", delivered)
	output := filepath.Join(dir, "outgoing.json")
	carried := annotateOK(t, "", "carry", snapshot, sealed, "--out", output, "--json")
	if carried.Carried != 2 || carried.Resolved == nil || !*carried.Resolved {
		t.Fatalf("marks lost: %+v", carried)
	}
	if !reflect.DeepEqual(manifestWithoutAnnotations(t, sealed), manifestWithoutAnnotations(t, output)) {
		t.Fatal("republish changed sealed content")
	}
	// Nextcloud recipients may rename a mount without changing its content.
	renamed := filepath.Join(dir, "renamed.opus")
	data, _ := os.ReadFile(output)
	os.WriteFile(renamed, data, 0600)
	meeting, err := inspect.ExtractMeeting(renamed)
	if err != nil {
		t.Fatal(err)
	}
	if meeting.Manifest.Meeting.Title != "Rerun" {
		t.Fatal("renamed JSON was not recognized")
	}
}
