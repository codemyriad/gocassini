package operator

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Production values (meeting 01M7017M2R7CP08PNBGQ7X5AKW): the recorder started
// at 11:31:48.109, but the stream the remux planned at timeline 0 got its
// first packet 39 s later. Position 0 of the audio is that packet.
var testAnchors = []recordingAnchor{
	{FirstPacketWallMS: 1794223947189, TimelineSeconds: 0.008630},
	{FirstPacketWallMS: 1794223990082, TimelineSeconds: 42.901264},
	{FirstPacketWallMS: 1794223947181, TimelineSeconds: 0},
}

const (
	testSessionStarted = "2026-11-09T11:31:48.109649551Z"
	testRecordStarted  = "2026-11-09T11:31:47.900000000Z"
	testJobULID        = "01M7017M2R7CP08PNBGQ7X5AKW"
)

func TestChooseRecordedAtPrecedence(t *testing.T) {
	full := recordedAtEvidence{Anchors: testAnchors, SessionStartedWallUTC: testSessionStarted, RecordStartedAt: testRecordStarted, JobID: testJobULID}
	for _, tc := range []struct {
		name   string
		mutate func(*recordedAtEvidence)
		want   string
		source recordedAtSource
	}{
		{"recording anchors win", func(*recordedAtEvidence) {}, "2026-11-09T11:32:27.181Z", recordedAtFromRecording},
		{"session without anchors", func(e *recordedAtEvidence) { e.Anchors = nil }, "2026-11-09T11:31:48.109Z", recordedAtFromSession},
		{"unusable anchors fall through", func(e *recordedAtEvidence) {
			e.Anchors = []recordingAnchor{{FirstPacketWallMS: 0}, {FirstPacketWallMS: 5, TimelineSeconds: 0}}
		}, "2026-11-09T11:31:48.109Z", recordedAtFromSession},
		{"job start without bundle", func(e *recordedAtEvidence) { e.Anchors, e.SessionStartedWallUTC = nil, "" }, "2026-11-09T11:31:47.900Z", recordedAtFromJob},
		{"garbage session falls through", func(e *recordedAtEvidence) { e.Anchors, e.SessionStartedWallUTC = nil, "yesterday" }, "2026-11-09T11:31:47.900Z", recordedAtFromJob},
		{"plain RFC 3339 job start", func(e *recordedAtEvidence) {
			e.Anchors, e.SessionStartedWallUTC, e.RecordStartedAt = nil, "", "2026-06-15T10:34:01Z"
		}, "2026-06-15T10:34:01.000Z", recordedAtFromJob},
		{"job id last", func(e *recordedAtEvidence) { e.Anchors, e.SessionStartedWallUTC, e.RecordStartedAt = nil, "", "" }, "", recordedAtFromJobID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := full
			tc.mutate(&ev)
			at, source, ok := chooseRecordedAt(ev)
			if !ok || source != tc.source {
				t.Fatalf("source = %q ok=%v, want %q", source, ok, tc.source)
			}
			if tc.want != "" && formatRecordedAtUTC(at) != tc.want {
				t.Fatalf("instant = %s, want %s", formatRecordedAtUTC(at), tc.want)
			}
			if !recordedAtUTCPattern.MatchString(formatRecordedAtUTC(at)) {
				t.Fatalf("%s does not match the contract pattern", formatRecordedAtUTC(at))
			}
		})
	}
	if _, _, ok := chooseRecordedAt(recordedAtEvidence{JobID: "daily-meeting-2026-06-10"}); ok {
		t.Fatal("no evidence must yield no instant")
	}
}

// The ULID's millisecond timestamp is when the recording was asked for
// (here 22 s before the recorder started: the production maximum).
func TestChooseRecordedAtDecodesJobID(t *testing.T) {
	at, source, ok := chooseRecordedAt(recordedAtEvidence{JobID: testJobULID})
	if !ok || source != recordedAtFromJobID {
		t.Fatalf("source = %q ok=%v", source, ok)
	}
	if at.Format(time.RFC3339Nano) != "2026-11-09T11:31:26.424Z" {
		t.Fatalf("ULID instant %s is not the job's creation", at)
	}
}

func TestParseRecordingAnchorsReadsRemuxTags(t *testing.T) {
	raw := `{"streams":[
	 {"tags":{"STREAM_ID":"s_000002","SOURCE_START_SECONDS":"0.000000","OFFSET_SECONDS":"0.000000","FIRST_PACKET_WALL_MS":"1794223947181"}},
	 {"tags":{"SOURCE_START_SECONDS":"0.5","OFFSET_SECONDS":"42.0","FIRST_PACKET_WALL_MS":"1794223990082"}},
	 {"tags":{"FIRST_PACKET_WALL_MS":"not a number","OFFSET_SECONDS":"1"}},
	 {}
	]}`
	anchors, err := parseRecordingAnchors([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(anchors) != 2 || anchors[0] != (recordingAnchor{1794223947181, 0}) || anchors[1] != (recordingAnchor{1794223990082, 42.5}) {
		t.Fatalf("anchors = %+v", anchors)
	}
}

func TestRecordedAtUTCShape(t *testing.T) {
	for value, want := range map[string]string{
		"2026-11-09T11:32:27Z":          "2026-11-09T11:32:27.000Z",
		"2026-11-09T11:32:27.181847Z":   "2026-11-09T11:32:27.181Z",
		"2026-11-09T12:32:27+01:00":     "",
		"2026-11-09T11:32:27":           "",
		"2026-11-09":                    "",
		" 2026-11-09T11:32:27.5Z ":      "2026-11-09T11:32:27.500Z",
		"2026-13-09T11:32:27Z":          "",
		"2026-11-09T11:32:27.181Zjunk":  "",
		"2026-11-09 11:32:27.181Z":      "",
		"20261109T113227Z":              "",
		"2026-11-09T11:32:27.181+00:00": "",
	} {
		if got := normalizedRecordedAtUTC(value); got != want {
			t.Errorf("normalizedRecordedAtUTC(%q) = %q, want %q", value, got, want)
		}
	}
}

// Entries with an instant sort by it; entries without one keep their
// dateLabel place; the order is the same in Go and in SQL.
func TestMeetingSortKeyMixesInstantsAndLabels(t *testing.T) {
	type item struct{ id, label, instant string }
	items := []item{
		{"Y", "2026-11-09 10:30", ""},
		{"X", "2026-11-09 11:00", "2026-11-09T11:00:30.000Z"},
		// Its label is the request time; the audio started 25 minutes later.
		{"Z", "2026-11-09 10:20", "2026-11-09T10:45:00.000Z"},
		{"OLD", "2026-06-10", ""},
		{"W", "2026-11-09 11:00", ""},
	}
	want := []string{"X", "W", "Z", "Y", "OLD"}
	keys := map[string]string{}
	for _, it := range items {
		keys[it.id] = meetingSortKey(it.label, it.instant)
	}
	for i := 1; i < len(want); i++ {
		if !(keys[want[i-1]] > keys[want[i]]) {
			t.Fatalf("%s (%q) must sort before %s (%q)", want[i-1], keys[want[i-1]], want[i], keys[want[i]])
		}
	}

	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer metadata.Close()
	for i, it := range items {
		entry := map[string]any{"id": it.id, "dateLabel": it.label, "audioPath": "./meetings/" + it.id + ".opus"}
		if it.instant != "" {
			entry["recordedAtUtc"] = it.instant
		}
		raw, _ := json.Marshal(entry)
		if err := metadata.Put(context.Background(), int64(i+1), it.id+".opus", raw); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := metadata.db.Query(`SELECT opus_name FROM meeting_metadata ORDER BY ` + meetingMetadataSortKeySQL + ` DESC, file_id DESC`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		_ = rows.Scan(&name)
		got = append(got, meetingStem(name))
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("SQL order = %v, want %v", got, want)
	}

	// The served catalog orders the same way.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rowsJSON []string
		for i, it := range items {
			rowsJSON = append(rowsJSON, `{"uid_file_owner":"cassini","file_source":`+string(rune('1'+i))+`,"permissions":1,"path":"/Shared/`+it.id+`.opus","item_type":"file"}`)
		}
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[`+strings.Join(rowsJSON, ",")+`]}}`)
	}))
	defer server.Close()
	cfg := ExAppConfig{NextcloudURL: server.URL, AppID: "cassini", AppVersion: "1", AppSecret: "secret", sharePaths: &recordingSharePathCache{}}
	snapshot, err := cfg.directShareSnapshot(context.Background(), server.Client(), "alice", metadata)
	if err != nil {
		t.Fatal(err)
	}
	got = got[:0]
	for _, raw := range snapshot.entries {
		var e struct {
			ID            string `json:"id"`
			RecordedAtUTC string `json:"recordedAtUtc"`
		}
		_ = json.Unmarshal(raw, &e)
		got = append(got, e.ID)
		if e.ID == "X" && e.RecordedAtUTC != "2026-11-09T11:00:30.000Z" {
			t.Fatalf("served entry lost its instant: %s", raw)
		}
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("catalog order = %v, want %v", got, want)
	}
}

// An instant bound compares instants; a zone-less bound keeps comparing as it
// always did. Entries are placed by recordedAtUtc when present.
func TestMeetingsListFilterAcceptsInstants(t *testing.T) {
	entries := []json.RawMessage{
		// Label says 23:30 UTC on the 9th; the instant is the same moment.
		json.RawMessage(`{"id":"LATE","dateLabel":"2026-11-09 23:30","recordedAtUtc":"2026-11-09T23:30:00.000Z"}`),
		json.RawMessage(`{"id":"LABEL","dateLabel":"2026-11-09 10:00"}`),
		json.RawMessage(`{"id":"NEXT","dateLabel":"2026-11-10 08:00","recordedAtUtc":"2026-11-10T08:00:00Z"}`),
		json.RawMessage(`{"id":"UNDATED","dateLabel":"daily"}`),
	}
	ids := func(kept []json.RawMessage) string {
		var out []string
		for _, raw := range kept {
			var e struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &e)
			out = append(out, e.ID)
		}
		return strings.Join(out, ",")
	}
	for _, tc := range []struct{ from, to, want string }{
		// Old zone-less day bounds: unchanged behaviour.
		{"2026-11-09", "2026-11-09", "LATE,LABEL"},
		// The 10th in Rome starts at 23:00 UTC on the 9th.
		{"2026-11-10T00:00:00+01:00", "2026-11-10T23:59:59+01:00", "LATE,NEXT"},
		{"2026-11-09T23:30:00Z", "", "LATE,NEXT"},
		{"", "2026-11-09T23:29:59.999Z", "LABEL"},
	} {
		filter, err := newMeetingsListFilter(tc.from, tc.to, "", "")
		if err != nil {
			t.Fatalf("%s..%s: %v", tc.from, tc.to, err)
		}
		kept, excluded := applyMeetingsListFilter(entries, filter)
		if got := ids(kept); got != tc.want {
			t.Errorf("%s..%s kept %s, want %s", tc.from, tc.to, got, tc.want)
		}
		if excluded.Undated != 1 {
			t.Errorf("%s..%s undated = %d, want 1", tc.from, tc.to, excluded.Undated)
		}
	}
	filter, err := newMeetingsListFilter("2026-11-10T00:00:00+01:00", "2026-11-10 12:00", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if filter.fromEcho() != "2026-11-09T23:00:00Z" || filter.toEcho() != "2026-11-10 12:00:00" {
		t.Fatalf("echo = %q %q", filter.fromEcho(), filter.toEcho())
	}
}

// Search hits and the rooms list read the instant from the caller's catalog.
func TestCatalogHydrationCarriesRecordedAt(t *testing.T) {
	raw := []byte(`{"meetings":[{"id":"A","dateLabel":"2026-11-09 10:00","recordedAtUtc":"2026-11-09T10:00:05.5Z","audioPath":"./meetings/A.opus"},{"id":"B","dateLabel":"2026-11-09 11:00","audioPath":"./meetings/B.opus"}]}`)
	entries, err := decodeCatalogEntries(raw)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].recordedAtUTC != "2026-11-09T10:00:05.500Z" || entries[1].recordedAtUTC != "" {
		t.Fatalf("hydration = %+v", entries)
	}

	ordered, _ := orderListedMeetings([]json.RawMessage{
		json.RawMessage(`{"id":"A","dateLabel":"2026-11-09 10:00","recordedAtUtc":"2026-11-09T12:00:00Z","roomId":"r1"}`),
		json.RawMessage(`{"id":"B","dateLabel":"2026-11-09 11:00","roomId":"r2"}`),
	})
	if ordered[0].id != "A" {
		t.Fatalf("instant must order the list: %+v", ordered)
	}
	rooms, _ := groupMeetingsByRoom(ordered)
	if rooms[0].RoomID != "r1" || rooms[0].Latest != "2026-11-09 10:00" {
		t.Fatalf("rooms = %+v", rooms)
	}
}
