package cassini

import (
	"bytes"
	"encoding/json"
	"testing"

	inspectpkg "gocassini/internal/inspect"
)

func TestInspectMeetingTimes(t *testing.T) {
	requireFFMediaTools(t)
	file := packFixtureOpus(t, t.TempDir(), "meeting")
	meeting, err := inspectpkg.ExtractMeeting(file)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	if code := runInspect([]string{"--meeting-times", file}, &out, &stderr); code != 0 {
		t.Fatalf("inspect: %d %s", code, stderr.String())
	}
	var dates map[string]string
	if err := json.Unmarshal(out.Bytes(), &dates); err != nil {
		t.Fatal(err)
	}
	if len(dates) != 2 || dates["createdAtUtc"] == "" || dates["createdAtUtc"] != meeting.Manifest.Meeting.CreatedAtUTC || dates["recordedAtLocal"] != meeting.Manifest.Meeting.RecordedAtLocal {
		t.Fatal(dates)
	}
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
