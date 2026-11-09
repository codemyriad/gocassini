package cassini

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // the zone tests must not depend on the host's zoneinfo
)

// meetingsTestZone is a zone with a non-zero offset, injected rather than set
// as time.Local, so a time rendered or compared in UTC by mistake fails.
func meetingsTestZone(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Rome")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func runMeetingsCLIIn(t *testing.T, loc *time.Location, baseURL string, args ...string) (int, string, string) {
	t.Helper()
	full := append([]string{"meetings"}, args...)
	full = append(full, "--nextcloud-url", baseURL, "--user", "alice", "--app-password", "app-pw-1234")
	var stdout, stderr bytes.Buffer
	code := Run(withMeetingsLocation(context.Background(), loc), full, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// mixedTimesCatalog holds both kinds of entry on one November morning in
// Rome (UTC+1):
//
//   - TALKNEW started 11:31Z = 12:31 Rome. Its legacy dateLabel is the UTC
//     digits every ExApp label carries.
//   - CRON has only a label, Rome wall clock 12:00 (= 11:00Z).
//   - TALKOLD started 10:45Z = 11:45 Rome, label again in UTC digits.
//
// By instant the order is TALKNEW, CRON, TALKOLD. By label digits it would be
// CRON (12:00), TALKNEW (11:31), TALKOLD (10:45).
const mixedTimesCatalog = `{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "CRON", "title": "Cron era", "dateLabel": "2026-11-09 12:00",
     "audioPath": "./meetings/CRON.opus", "roomId": "rm_11bb22cc33dd44ee", "roomName": "Old Standup"},
    {"id": "TALKOLD", "title": "Talk earlier", "dateLabel": "2026-11-09 10:45", "recordedAtUtc": "2026-11-09T10:45:12.500Z",
     "audioPath": "./meetings/TALKOLD.opus", "roomId": "rm_9f2a1c3d4e5b6a70", "roomName": "Weekly Sync"},
    {"id": "TALKNEW", "title": "Talk later", "dateLabel": "2026-11-09 11:31", "recordedAtUtc": "2026-11-09T11:31:48Z",
     "audioPath": "./meetings/TALKNEW.opus", "roomId": "rm_9f2a1c3d4e5b6a70", "roomName": "Weekly Sync"}
  ]
}`

func listedMeetingIDs(stdout string) []string {
	var ids []string
	for _, line := range strings.Split(stdout, "\n") {
		if rest, ok := strings.CutPrefix(line, "meeting="); ok {
			ids = append(ids, strings.Fields(rest)[0])
		}
	}
	return ids
}

func TestMeetingsListOrdersAndRendersRecordingStarts(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(mixedTimesCatalog))
	code, stdout, stderr := runMeetingsCLIIn(t, meetingsTestZone(t), fake.server.URL, "list")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if got := strings.Join(listedMeetingIDs(stdout), ","); got != "TALKNEW,CRON,TALKOLD" {
		t.Fatalf("order = %s, want TALKNEW,CRON,TALKOLD (by instant, labels read in local time):\n%s", got, stdout)
	}
	for _, want := range []string{
		// The instant in the reader's zone, offset written out, one token.
		"meeting=TALKNEW date=2026-11-09T12:31+01:00 ",
		"meeting=TALKOLD date=2026-11-09T11:45+01:00 ",
		// A label is printed exactly as published.
		"meeting=CRON date=2026-11-09 12:00 ",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in:\n%s", want, stdout)
		}
	}
}

func TestMeetingsListDateRangeIsReadInTheLocalZone(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(mixedTimesCatalog))
	rome := meetingsTestZone(t)
	for _, tc := range []struct {
		from, to string
		want     string
	}{
		// 11:40 Rome is 10:40Z: all three are after it.
		{"2026-11-09 11:40", "", "TALKNEW,CRON,TALKOLD"},
		// 12:10 Rome is 11:10Z: TALKOLD (10:45Z) and CRON (12:00 label) are
		// before it. In UTC, TALKOLD would have been kept.
		{"2026-11-09 12:10", "", "TALKNEW"},
		// Up to 12:00 Rome: the label at 12:00 is inclusive, TALKNEW (12:31
		// Rome) is out although its UTC digits say 11:31.
		{"", "2026-11-09 12:00", "CRON,TALKOLD"},
		// A bare date is the local calendar day.
		{"2026-11-09", "2026-11-09", "TALKNEW,CRON,TALKOLD"},
	} {
		args := []string{"list"}
		if tc.from != "" {
			args = append(args, "--from", tc.from)
		}
		if tc.to != "" {
			args = append(args, "--to", tc.to)
		}
		code, stdout, stderr := runMeetingsCLIIn(t, rome, fake.server.URL, args...)
		if code != 0 {
			t.Fatalf("%v: exit=%d stderr=%q", args, code, stderr)
		}
		if got := strings.Join(listedMeetingIDs(stdout), ","); got != tc.want {
			t.Errorf("%v = %s, want %s:\n%s", args, got, tc.want, stdout)
		}
	}
}

func TestMeetingsRoomsUseRecordingStarts(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(mixedTimesCatalog))
	code, stdout, stderr := runMeetingsCLIIn(t, meetingsTestZone(t), fake.server.URL, "rooms")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	// Weekly Sync's latest meeting (11:31Z) is after Old Standup's (11:00Z),
	// although Old Standup's label digits (12:00) are larger.
	if len(lines) < 3 || !strings.HasPrefix(lines[1], "room=rm_9f2a1c3d4e5b6a70 ") || !strings.HasPrefix(lines[2], "room=rm_11bb22cc33dd44ee ") {
		t.Fatalf("room order wrong:\n%s", stdout)
	}
	if !strings.Contains(lines[1], "latest=2026-11-09T12:31+01:00 earliest=2026-11-09T11:45+01:00") {
		t.Errorf("Weekly Sync latest/earliest not rendered as local instants: %s", lines[1])
	}
	if !strings.Contains(lines[2], "latest=2026-11-09 12:00 earliest=2026-11-09 12:00") {
		t.Errorf("Old Standup labels not left as-is: %s", lines[2])
	}

	code, stdout, stderr = runMeetingsCLIIn(t, meetingsTestZone(t), fake.server.URL, "rooms", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var document struct {
		Rooms []meetingsRoom `json:"rooms"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("parse rooms --json: %v\n%s", err, stdout)
	}
	if len(document.Rooms) != 2 {
		t.Fatalf("rooms = %+v", document.Rooms)
	}
	sync := document.Rooms[0]
	// The labels stay labels for old consumers; the instants are additive.
	if sync.Latest != "2026-11-09 11:31" || sync.LatestRecordedAtUTC != "2026-11-09T11:31:48Z" ||
		sync.Earliest != "2026-11-09 10:45" || sync.EarliestRecordedAtUTC != "2026-11-09T10:45:12.500Z" {
		t.Errorf("Weekly Sync JSON = %+v", sync)
	}
	if document.Rooms[1].LatestRecordedAtUTC != "" {
		t.Errorf("Old Standup gained an instant it does not have: %+v", document.Rooms[1])
	}
}

func TestMeetingsSearchRendersTheRecordingStart(t *testing.T) {
	const body = `{
  "hits":[
    {"meetingId":"TALK","title":"Standup","dateLabel":"2026-11-09 11:31","recordedAtUtc":"2026-11-09T11:31:48Z",
     "segmentId":"seg_1","startMs":1000,"endMs":2000,"speakerId":"S1","matched":"exact"},
    {"meetingId":"CRON","title":"Old","dateLabel":"2026-11-02 09:00",
     "segmentId":"seg_2","startMs":1000,"endMs":2000,"speakerId":"S1","matched":"exact"}],
  "coverage":{"visible":2,"searched":2}}`
	fake := newMeetingsFakeNextcloud(t, serveSearchRoute(body))
	code, stdout, stderr := runMeetingsCLIIn(t, meetingsTestZone(t), fake.server.URL, "search", "standup")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "moment=TALK ") || !strings.Contains(stdout, " date=2026-11-09T12:31+01:00 ") {
		t.Errorf("search hit not rendered as a local instant:\n%s", stdout)
	}
	if !strings.Contains(stdout, " date=2026-11-02 09:00 ") {
		t.Errorf("legacy label not left as-is:\n%s", stdout)
	}

	code, stdout, stderr = runMeetingsCLIIn(t, meetingsTestZone(t), fake.server.URL, "search", "standup", "--json")
	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, `"recordedAtUtc": "2026-11-09T11:31:48Z"`) && !strings.Contains(stdout, `"recordedAtUtc":"2026-11-09T11:31:48Z"`) {
		t.Errorf("search --json dropped recordedAtUtc:\n%s", stdout)
	}
}

func TestMeetingTimeAndDisplayTable(t *testing.T) {
	rome := meetingsTestZone(t)
	for _, tc := range []struct {
		name        string
		entry       meetingsCatalogEntry
		wantDisplay string
		wantAt      string
		wantDated   bool
		wantInstant bool
	}{
		{"instant wins over its label", meetingsCatalogEntry{RecordedAtUTC: "2026-07-01T08:00:00Z", DateLabel: "2026-07-01 08:00"},
			"2026-07-01T10:00+02:00", "2026-07-01T08:00:00Z", true, true},
		{"label read in the local zone", meetingsCatalogEntry{DateLabel: "2026-07-01 08:00"},
			"2026-07-01 08:00", "2026-07-01T06:00:00Z", true, false},
		{"malformed instant falls back to the label", meetingsCatalogEntry{RecordedAtUTC: "2026-07-01T10:00:00+02:00", DateLabel: "2026-07-01"},
			"2026-07-01", "2026-06-30T22:00:00Z", true, false},
		{"nothing", meetingsCatalogEntry{}, "", "0001-01-01T00:00:00Z", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, dated, instant := meetingTime(tc.entry, rome)
			if got := at.UTC().Format(time.RFC3339); got != tc.wantAt || dated != tc.wantDated || instant != tc.wantInstant {
				t.Errorf("meetingTime = %s dated=%v instant=%v, want %s %v %v", got, dated, instant, tc.wantAt, tc.wantDated, tc.wantInstant)
			}
			if got := meetingDisplayDate(tc.entry, rome); got != tc.wantDisplay {
				t.Errorf("meetingDisplayDate = %q, want %q", got, tc.wantDisplay)
			}
		})
	}
}
