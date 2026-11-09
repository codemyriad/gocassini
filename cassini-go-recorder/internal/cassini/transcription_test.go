package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gocassini/internal/inspect"
	"gocassini/internal/portable"
	"gocassini/internal/transcribe"
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

// Exercise the Talk recording start through a delayed build, both pack
// formats, and audio eviction: the MKV's wall-clock anchor becomes
// meeting.recordedAtUtc, its CASSINI_RECORDED_AT_UTC mirror and DATE, while
// the zone-unknown recorded_at_local label is not propagated.
func TestRecordingTimeSurvivesBuildPackAndTranscriptionExport(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "meeting.meeting")
	if err := writeReadyMeetingBundleFixture(bundle, "source.mkv"); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(dir, "recording.mkv")
	origin := time.Date(2026, 3, 10, 14, 0, 0, 125e6, time.UTC)
	const recorded = "2026-03-10T14:00:00.125Z"
	if raw, err := exec.Command("ffmpeg", "-y", "-v", "error", "-i", filepath.Join(bundle, "meeting.webm"), "-c:a", "copy",
		"-metadata", "session_started_at=2026-03-10T13:59:57Z",
		"-metadata", "recorded_at_local=2026-03-10T14:00:00",
		"-metadata:s:a:0", fmt.Sprintf("first_packet_wall_ms=%d", origin.UnixMilli()),
		"-metadata:s:a:0", fmt.Sprintf("first_timeline_ns=%d", origin.UnixNano()),
		"-metadata:s:a:0", "clock_rate=48000",
		input).CombinedOutput(); err != nil {
		t.Fatalf("mkv fixture: %v: %s", err, raw)
	}
	if err := transcribe.BuildMeetingArtifact(context.Background(), input, bundle, transcribe.BuildConfig{TranscriptionMode: "off"}, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{".opus", ".json"} {
		path := packAnnotateBundle(t, bundle, filepath.Join(dir, "published"+ext))
		meeting, err := inspect.ExtractMeeting(path)
		if err != nil {
			t.Fatal(err)
		}
		if meeting.Manifest.Meeting.RecordedAtUTC != recorded || meeting.Manifest.Meeting.RecordedAtLocal != "" {
			t.Fatalf("%s: recordedAtUtc lost or recordedAtLocal propagated: %+v", ext, meeting.Manifest.Meeting)
		}
		if meeting.Manifest.Meeting.CreatedAtUTC == recorded {
			t.Fatalf("%s: createdAtUtc must stay the pack/build time: %+v", ext, meeting.Manifest.Meeting)
		}
		if ext == ".opus" {
			tags, err := portableMeetingTags(path)
			if err != nil {
				t.Fatal(err)
			}
			if tags["CASSINI_RECORDED_AT_UTC"] != recorded || tags["DATE"] != recorded {
				t.Fatalf("mirror/DATE lost recording start: %+v", tags)
			}
			if _, ok := tags["CASSINI_RECORDED_AT_LOCAL"]; ok {
				t.Fatalf("zone-unknown label mirrored: %+v", tags)
			}
			if tags["CASSINI_CREATED_AT"] != meeting.Manifest.Meeting.CreatedAtUTC {
				t.Fatalf("CASSINI_CREATED_AT moved off createdAtUtc: %+v", tags)
			}
			raw, err := portable.EncodeTranscription(tags)
			if err != nil {
				t.Fatal(err)
			}
			var doc struct {
				Source struct {
					Meeting portable.Meeting `json:"meeting"`
				} `json:"source"`
				Tags map[string]string `json:"tags"`
			}
			if err := json.Unmarshal(raw, &doc); err != nil {
				t.Fatal(err)
			}
			if doc.Source.Meeting.RecordedAtUTC != recorded || doc.Tags["CASSINI_RECORDED_AT_UTC"] != recorded {
				t.Fatalf("transcription conversion lost recording time: %s", raw)
			}
		}
	}
}

func TestTranscriptionPolicySurvivesSourceDeletion(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	bundle := filepath.Join(dir, "meeting.meeting")
	if err := writeReadyMeetingBundleFixture(bundle, "source.mkv"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundle, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest["provenance"] = map[string]any{"recording": map[string]string{"captureMode": "audio-only", "sourceRetention": "delete-after-processing"}}
	source := manifest["source"].(map[string]any)
	source["recordedAtLocal"] = "2026-03-10T14:00:00"
	raw, _ = json.Marshal(manifest)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	out := packAnnotateBundle(t, bundle, filepath.Join(dir, "meeting.json"))
	if err = os.RemoveAll(bundle); err != nil {
		t.Fatal(err)
	}
	meeting, err := inspect.ExtractMeeting(out)
	if err != nil {
		t.Fatal(err)
	}
	if meeting.Manifest.Meeting.RecordedAtLocal != "2026-03-10T14:00:00" {
		t.Fatal("lost recording time")
	}
	if meeting.Manifest.Provenance == nil || meeting.Manifest.Provenance.Recording == nil || meeting.Manifest.Provenance.Recording.SourceRetention != "delete-after-processing" {
		t.Fatalf("lost policy: %+v", meeting.Manifest.Provenance)
	}
}
