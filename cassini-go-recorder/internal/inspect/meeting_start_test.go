package inspect

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"gocassini/internal/portable"
)

// `cassini inspect` says when the meeting happened by the format's rule, the
// shapes of conformance vectors 027-029, and warns when the
// CASSINI_RECORDED_AT_UTC mirror disagrees with the manifest.
func TestInspectPathReportsTheMeetingStart(t *testing.T) {
	requireFFMediaTools(t)
	const created = "2026-03-11T08:30:00Z" // the fixture's createdAtUtc
	const label = "2026-03-11T07:31:48"
	for _, tc := range []struct {
		name        string
		meeting     func(*portable.Meeting)
		tags        func(map[string]string)
		want        []string
		wantMissing []string
		wantStart   string
	}{
		{
			name: "recordedAtUtc",
			meeting: func(m *portable.Meeting) {
				m.RecordedAtUTC = "2026-03-11T07:31:48.250Z"
				m.RecordedAtLocal = label
			},
			want:        []string{"recorded_at utc=2026-03-11T07:31:48.250Z local=" + label},
			wantStart:   "meeting_start source=recordedAtUtc value=2026-03-11T07:31:48.250Z",
			wantMissing: []string{"between manifest and tag"},
		},
		{
			name:        "DATE is the pack stamp",
			meeting:     func(m *portable.Meeting) { m.RecordedAtLocal = label },
			tags:        func(tags map[string]string) { tags["DATE"] = created },
			want:        []string{"recorded_at utc=- local=" + label},
			wantStart:   "meeting_start source=recordedAtLocal value=" + label,
			wantMissing: []string{"between manifest and tag"},
		},
		{
			name:      "DATE with an offset",
			meeting:   func(m *portable.Meeting) { m.RecordedAtLocal = label },
			tags:      func(tags map[string]string) { tags["DATE"] = "2026-03-11T08:31:48+01:00" },
			wantStart: "meeting_start source=DATE value=2026-03-11T08:31:48+01:00",
		},
		{
			name:    "the mirror disagrees",
			meeting: func(m *portable.Meeting) { m.RecordedAtUTC = "2026-03-11T07:31:48Z" },
			tags:    func(tags map[string]string) { tags["CASSINI_RECORDED_AT_UTC"] = "2026-03-11T06:00:00Z" },
			want: []string{
				"warning=recorded-at disagrees between manifest and tag: manifest=2026-03-11T07:31:48Z CASSINI_RECORDED_AT_UTC=2026-03-11T06:00:00Z",
			},
			// The manifest is the record.
			wantStart: "meeting_start source=recordedAtUtc value=2026-03-11T07:31:48Z",
		},
		{
			name: "a mirror the manifest does not have",
			tags: func(tags map[string]string) { tags["CASSINI_RECORDED_AT_UTC"] = "2026-03-11T06:00:00Z" },
			want: []string{
				"warning=recorded-at disagrees between manifest and tag: manifest=- CASSINI_RECORDED_AT_UTC=2026-03-11T06:00:00Z",
			},
			wantMissing: []string{"meeting_start", "recorded_at "},
		},
		{
			name:        "the same instant spelled differently",
			meeting:     func(m *portable.Meeting) { m.RecordedAtUTC = "2026-03-11T07:31:48Z" },
			tags:        func(tags map[string]string) { tags["CASSINI_RECORDED_AT_UTC"] = "2026-03-11T07:31:48.000Z" },
			wantStart:   "meeting_start source=recordedAtUtc value=2026-03-11T07:31:48Z",
			wantMissing: []string{"between manifest and tag"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "meeting.opus"), portableFixtureOptions{
				meeting: tc.meeting, tags: tc.tags,
			})
			var out bytes.Buffer
			if err := InspectPath(&out, path); err != nil {
				t.Fatalf("inspect: %v\n%s", err, out.String())
			}
			got := out.String()
			if !strings.Contains(got, "cassini=ok") {
				t.Fatalf("a disagreement is not an error; want cassini=ok:\n%s", got)
			}
			want := tc.want
			if tc.wantStart != "" {
				want = append(want, tc.wantStart+"\n")
			}
			for _, w := range want {
				if !strings.Contains(got, w) {
					t.Errorf("inspect output lacks %q:\n%s", w, got)
				}
			}
			for _, w := range tc.wantMissing {
				if strings.Contains(got, w) {
					t.Errorf("inspect output has %q:\n%s", w, got)
				}
			}
		})
	}
}

// ExtractMeeting carries the DATE tag, so its readers apply the same rule.
func TestExtractMeetingTakesTheStartFromDATE(t *testing.T) {
	requireFFMediaTools(t)
	path := createPortableOpusFixture(t, filepath.Join(t.TempDir(), "meeting.opus"), portableFixtureOptions{
		meeting: func(m *portable.Meeting) { m.RecordedAtLocal = "2026-03-11T07:31:48" },
		tags:    func(tags map[string]string) { tags["DATE"] = "2026-03-11T08:31:48+01:00" },
	})
	meeting, err := ExtractMeeting(path)
	if err != nil {
		t.Fatal(err)
	}
	start := meeting.MeetingStart()
	if start.Source != portable.MeetingStartDATE || start.Value != "2026-03-11T08:31:48+01:00" || !start.Instant {
		t.Fatalf("MeetingStart = %+v, want the DATE instant", start)
	}
}
