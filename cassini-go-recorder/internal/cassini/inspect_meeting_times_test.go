package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	inspectpkg "gocassini/internal/inspect"
)

func TestInspectMeetingTimes(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	packed := packFixtureOpus(t, dir, "meeting")
	file := filepath.Join(dir, "started.opus")
	runRetagJSON(t, packed, "--out", file, "--recorded-at-utc", "2026-11-09T11:31:48.250Z")
	meeting, err := inspectpkg.ExtractMeeting(file)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runInspect([]string{"--meeting-times", file}, &out, &stderr); code != 0 {
		t.Fatalf("inspect: %d %s", code, stderr.String())
	}
	dates := decodeMeetingTimes(t, out.Bytes())
	if len(dates) != 4 || dates["createdAtUtc"] == "" || dates["createdAtUtc"] != meeting.Manifest.Meeting.CreatedAtUTC ||
		dates["recordedAtUtc"] != "2026-11-09T11:31:48.250Z" || dates["recordedAtLocal"] != meeting.Manifest.Meeting.RecordedAtLocal ||
		dates["meetingStart"] != "recordedAtUtc 2026-11-09T11:31:48.250Z" {
		t.Fatal(dates)
	}
}

// A file whose manifest has no recordedAtUtc but whose DATE carries the start
// with an offset: meetingStart takes it from DATE, as the format says.
func TestInspectMeetingTimesTakesTheStartFromDATE(t *testing.T) {
	requireFFMediaTools(t)
	dir := t.TempDir()
	packed := packFixtureOpus(t, dir, "meeting")
	tags, err := portableMeetingTags(packed)
	if err != nil {
		t.Fatal(err)
	}
	setTagPreservingCase(tags, "DATE", "2026-11-09T12:31:48+01:00")
	file := filepath.Join(dir, "dated.opus")
	if err := writePortableMeetingFile(context.Background(), packed, file, tags); err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runInspect([]string{"--meeting-times", file}, &out, &stderr); code != 0 {
		t.Fatalf("inspect: %d %s", code, stderr.String())
	}
	dates := decodeMeetingTimes(t, out.Bytes())
	if dates["recordedAtUtc"] != "" || dates["meetingStart"] != "DATE 2026-11-09T12:31:48+01:00" {
		t.Fatal(dates)
	}

	// The packed file's DATE is its build time, which says nothing.
	out.Reset()
	if code := runInspect([]string{"--meeting-times", packed}, &out, &stderr); code != 0 {
		t.Fatalf("inspect: %d %s", code, stderr.String())
	}
	if dates := decodeMeetingTimes(t, out.Bytes()); strings.HasPrefix(dates["meetingStart"], "DATE") {
		t.Fatal(dates)
	}
}

// decodeMeetingTimes flattens the --meeting-times document, meetingStart as
// "source value".
func decodeMeetingTimes(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	flat := map[string]string{}
	for key, value := range doc {
		if key == "meetingStart" {
			var start struct{ Source, Value string }
			if err := json.Unmarshal(value, &start); err != nil {
				t.Fatal(err)
			}
			flat[key] = start.Source + " " + start.Value
			continue
		}
		var s string
		if err := json.Unmarshal(value, &s); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		flat[key] = s
	}
	return flat
}

func TestInspectMeetingIdentity(t *testing.T) {
	requireFFMediaTools(t)
	file := packFixtureOpus(t, t.TempDir(), "meeting")
	meeting, err := inspectpkg.ExtractMeeting(file)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runInspect([]string{"--meeting-identity", file}, &out, &stderr); code != 0 {
		t.Fatalf("inspect: %d %s", code, stderr.String())
	}
	var identity map[string]any
	if err := json.Unmarshal(out.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	if len(identity) != 3 || identity["roomId"] != meeting.Manifest.Meeting.RoomID || identity["jobId"] != meeting.Manifest.Meeting.JobID || identity["attemptNumber"] != float64(meeting.Manifest.Meeting.AttemptNumber) {
		t.Fatal(identity)
	}
	if code := runInspect([]string{"--meeting-identity", "--transcript", file}, &out, &stderr); code != 2 {
		t.Fatalf("conflicting flags: %d", code)
	}
}
