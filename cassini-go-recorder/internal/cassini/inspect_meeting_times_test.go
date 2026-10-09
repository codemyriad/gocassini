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
