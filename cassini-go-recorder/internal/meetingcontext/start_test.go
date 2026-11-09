package meetingcontext

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zone tests must not depend on the host's zoneinfo

	"gocassini/internal/portable"
)

func TestMeetingStartLine(t *testing.T) {
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Fatal(err)
	}
	rome, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		meeting Meeting
		loc     *time.Location
		want    string
	}{
		{"instant in a half-hour zone", Meeting{RecordedAtUTC: "2026-11-09T11:31:48.250Z", CreatedAtUTC: "2026-11-09T12:40:00Z"}, kolkata, "- Started: 2026-11-09T17:01+05:30"},
		{"instant in winter Rome", Meeting{RecordedAtUTC: "2026-11-09T11:31:48Z"}, rome, "- Started: 2026-11-09T12:31+01:00"},
		// A server in a zone-less container still writes the offset.
		{"instant in UTC", Meeting{RecordedAtUTC: "2026-11-09T11:31:48Z"}, time.UTC, "- Started: 2026-11-09T11:31+00:00"},
		{"label only", Meeting{RecordedAtLocal: "2026-11-09T12:31:48", CreatedAtUTC: "2026-11-09T12:40:00Z"}, rome, "- Started: 2026-11-09T12:31:48 (recording site's wall clock, time zone unknown)"},
		// The format's step 2: a DATE with an offset that is not the build
		// time outranks the label.
		{"DATE instant", Meeting{RecordedAtLocal: "2026-11-09T09:31:48", CreatedAtUTC: "2026-11-09T12:40:00Z", MeetingStart: &portable.MeetingStart{Source: "DATE", Value: "2026-11-09T10:31:48+01:00"}}, kolkata, "- Started: 2026-11-09T15:01+05:30"},
		{"build time only", Meeting{CreatedAtUTC: "2026-11-09T12:40:00Z"}, rome, "- Started: unknown (the file was built 2026-11-09T12:40:00Z)"},
		{"nothing", Meeting{}, rome, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := meetingStartLine(tc.meeting, tc.loc); got != tc.want {
				t.Fatalf("meetingStartLine = %q, want %q", got, tc.want)
			}
		})
	}
}

// Build picks the start from the file's DATE when the manifest has no
// recordedAtUtc, carries it in the JSON, and the markdown shows it — also
// after the bundle round-trips through JSON.
func TestBuildTakesTheStartFromDATE(t *testing.T) {
	bundle := Build(BuildInput{
		ID: "M1", Title: "Standup",
		CreatedAtUTC:    "2026-11-09T12:40:00Z",
		RecordedAtLocal: "2026-11-09T09:31:48",
		DateTag:         "2026-11-09T10:31:48+01:00",
	})
	if got := bundle.Meeting.MeetingStart; got == nil || got.Source != "DATE" || got.Value != "2026-11-09T10:31:48+01:00" {
		t.Fatalf("meetingStart = %+v, want DATE 2026-11-09T10:31:48+01:00", got)
	}
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"meetingStart":{"source":"DATE","value":"2026-11-09T10:31:48+01:00"}`) {
		t.Fatalf("JSON does not carry the start: %s", raw)
	}
	var decoded MeetingContext
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, b := range []MeetingContext{bundle, decoded} {
		got, err := RenderMarkdown(Bundle{Meetings: []MeetingContext{b}}, RenderOpts{Location: time.UTC})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, "- Started: 2026-11-09T09:31+00:00\n") {
			t.Fatalf("markdown does not start the meeting at DATE:\n%s", got)
		}
	}

	// A DATE that is only the build time says nothing; the label stands.
	packStamp := Build(BuildInput{ID: "M2", Title: "Old", CreatedAtUTC: "2026-11-09T12:40:00Z", RecordedAtLocal: "2026-11-09T09:31:48", DateTag: "2026-11-09T12:40:00Z"})
	if got := packStamp.Meeting.MeetingStart; got == nil || got.Source != "recordedAtLocal" {
		t.Fatalf("meetingStart = %+v, want the recordedAtLocal label", got)
	}
}

// The build time is never presented as when the meeting was recorded.
func TestRenderMarkdownDoesNotCallTheBuildTimeTheRecording(t *testing.T) {
	bundle := Bundle{Meetings: []MeetingContext{Build(BuildInput{ID: "M1", Title: "Standup", CreatedAtUTC: "2026-11-09T12:40:00Z"})}}
	got, err := RenderMarkdown(bundle, RenderOpts{Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Recorded (UTC)") {
		t.Fatalf("createdAtUtc labelled as the recording time:\n%s", got)
	}
}
