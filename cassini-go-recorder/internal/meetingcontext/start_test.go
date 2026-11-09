package meetingcontext

import (
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zone tests must not depend on the host's zoneinfo
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
