package cassini

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestMeetingsRoomsGroupsAndOrdersByMostRecent(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "rooms")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if lines[0] != "rooms=2 caller=alice source=nextcloud-files" {
		t.Errorf("header = %q", lines[0])
	}
	// Most recently active first, matching the order `list` is in — someone
	// scanning both should not have to re-sort in their head.
	want := []string{
		"room=" + roomCatalogTokenRoomID + " name=Weekly Sync meetings=2 latest=2026-08-11 10:32 earliest=2026-08-04 10:30",
		"room=" + roomCatalogNameRoomID + " name=Old Standup meetings=1 latest=2026-07-02 09:00 earliest=2026-07-02 09:00",
	}
	for i, expected := range want {
		if i+1 >= len(lines) || lines[i+1] != expected {
			t.Errorf("line %d = %q, want %q", i+1, lines[min(i+1, len(lines)-1)], expected)
		}
	}
	// The room-less meeting is counted, never hidden: a rooms listing whose
	// numbers do not add up to the meeting list is a discrepancy nobody ever
	// tracks down.
	if !strings.Contains(stdout, "note=1 readable meeting(s) carry no room at all") {
		t.Errorf("expected an unattributed-meetings note, got %q", stdout)
	}
}

func TestMeetingsRoomsJSONCarriesTheDiagnostics(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "rooms", "--json")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var document struct {
		Version      string `json:"version"`
		Source       string `json:"source"`
		Skipped      int    `json:"skipped"`
		Unattributed int    `json:"unattributed"`
		Rooms        []struct {
			Selector string `json:"room"`
			RoomID   string `json:"roomId"`
			RoomName string `json:"roomName"`
			Meetings int    `json:"meetings"`
			Latest   string `json:"latest"`
			Earliest string `json:"earliest"`
		} `json:"rooms"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("parse rooms JSON: %v (%q)", err, stdout)
	}
	if document.Version != "cassini.viewer.catalog.v1" || document.Source != "nextcloud-files" {
		t.Errorf("version/source = %q/%q", document.Version, document.Source)
	}
	if document.Unattributed != 1 {
		t.Errorf("unattributed = %d, want 1", document.Unattributed)
	}
	if len(document.Rooms) != 2 {
		t.Fatalf("rooms = %d, want 2 (%q)", len(document.Rooms), stdout)
	}
	first := document.Rooms[0]
	if first.Selector != roomCatalogTokenRoomID || first.RoomID != roomCatalogTokenRoomID || first.Meetings != 2 {
		t.Errorf("first room = %+v", first)
	}
	// A backfilled room reports an id like any other. Its id happens to be
	// derived from the room's name rather than its token, which nothing in the
	// output distinguishes — and nothing should, because a consumer cannot act
	// on the difference.
	second := document.Rooms[1]
	if second.Selector != roomCatalogNameRoomID || second.RoomID != roomCatalogNameRoomID || second.RoomName != "Old Standup" {
		t.Errorf("second room = %+v", second)
	}
}

func TestMeetingsRoomsEmptyCatalogExplainsTheAmbiguity(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(`{"version":"cassini.viewer.catalog.v1","meetings":[]}`))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "rooms")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "rooms=0") {
		t.Errorf("stdout=%q", stdout)
	}
	// The server answers 200 with an empty list both when the caller has no
	// readable recordings and when the substrate is mis-provisioned. It cannot
	// tell them apart, so neither may this.
	if !strings.Contains(stdout, "mis-provisioned recordings folder") {
		t.Errorf("expected the same ambiguity note `list` gives, got %q", stdout)
	}
}

func TestMeetingsRoomsFlattensControlCharactersInRoomNames(t *testing.T) {
	// Room names are Talk conversation names: user-controlled, and sanitised at
	// record time only for control characters and length. In a key=value line a
	// newline would let a name forge additional room= rows that anything
	// parsing this output would read as real rooms.
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"A","title":"t","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/A.opus",
	   "roomId":"rm_tok","roomName":"Sync\nroom=forged name=evil"}]}`
	fake := newMeetingsFakeNextcloud(t, serveCatalog(catalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "rooms")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// The property is line-level, and only line-level: within a line the text
	// form is for reading, not parsing (the same stance `list` documents for
	// titles). What must not happen is a second record LINE.
	records := 0
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.HasPrefix(line, "room=") {
			records++
		}
	}
	if records != 1 {
		t.Errorf("a room name forged an extra record line (%d records): %q", records, stdout)
	}
	if strings.Contains(stdout, "\n") && strings.Contains(stdout, "\nroom=forged") {
		t.Errorf("newline survived into the line-oriented output: %q", stdout)
	}
}

func TestMeetingsRoomsUsageAndDispatch(t *testing.T) {
	t.Run("leaf help goes to stderr and succeeds", func(t *testing.T) {
		code, stdout, stderr := runMeetingsCLIRaw(t, "meetings", "rooms", "--help")
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if !strings.Contains(stderr, "cassini meetings rooms") {
			t.Errorf("expected leaf usage on stderr, got %q", stderr)
		}
		if stdout != "" {
			t.Errorf("expected nothing on stdout, got %q", stdout)
		}
	})

	t.Run("positional arguments are rejected", func(t *testing.T) {
		code, _, stderr := runMeetingsCLIRaw(t, "meetings", "rooms", "extra")
		if code != 2 {
			t.Fatalf("exit=%d, want 2", code)
		}
		if !strings.Contains(stderr, "does not accept positional arguments") {
			t.Errorf("stderr=%q", stderr)
		}
	})

	t.Run("the family usage advertises rooms", func(t *testing.T) {
		code, stdout, _ := runMeetingsCLIRaw(t, "meetings", "--help")
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		if !strings.Contains(stdout, "cassini meetings rooms") {
			t.Errorf("family usage missing rooms: %q", stdout)
		}
	})
}

func TestGroupMeetingsByRoomKeepsSameNamedRoomsApart(t *testing.T) {
	// Two different ids, one display name — in practice a room identified from
	// its Talk token and the same room identified from its name by the catalog
	// backfill. They must NOT merge: two conversations can genuinely share a
	// name, and a room can be renamed between recordings, so merging on the name
	// would assert an identity nothing in the data supports. Merging them is a
	// human judgement, made with scripts/reattribute-catalog-room.sh.
	listing := meetingsListing{Items: []meetingsCatalogItem{
		{entry: meetingsCatalogEntry{ID: "A", RoomID: "rm_fromtoken", RoomName: "Weekly Sync"}},
		{entry: meetingsCatalogEntry{ID: "B", RoomID: "rm_fromname", RoomName: "Weekly Sync"}},
	}}

	rooms, unattributed := groupMeetingsByRoom(listing)

	if unattributed != 0 {
		t.Errorf("unattributed = %d, want 0", unattributed)
	}
	if len(rooms) != 2 {
		t.Fatalf("rooms = %d, want 2 (%+v)", len(rooms), rooms)
	}
	for _, room := range rooms {
		if room.Meetings != 1 {
			t.Errorf("room %q has %d meetings, want 1", room.Selector, room.Meetings)
		}
	}
}

func TestGroupMeetingsByRoomLabelsARoomFromALaterRecording(t *testing.T) {
	// The newest recording in a room may predate the room NAME being recorded
	// while still carrying the id (a failed name lookup at record time). The
	// row should still be labelled if any recording in the room carries a name.
	listing := meetingsListing{Items: []meetingsCatalogItem{
		{entry: meetingsCatalogEntry{ID: "NEW", RoomID: "rm_tok"}},
		{entry: meetingsCatalogEntry{ID: "OLD", RoomID: "rm_tok", RoomName: "Weekly Sync"}},
	}}

	rooms, _ := groupMeetingsByRoom(listing)

	if len(rooms) != 1 {
		t.Fatalf("rooms = %d, want 1", len(rooms))
	}
	if rooms[0].RoomName != "Weekly Sync" || rooms[0].Meetings != 2 {
		t.Errorf("room = %+v, want both meetings under the name %q", rooms[0], "Weekly Sync")
	}
}

// runMeetingsCLIRaw runs the CLI with no connection flags injected, for the
// usage and dispatch cases that never reach the network.
func runMeetingsCLIRaw(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr strings.Builder
	code := Run(context.Background(), args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestMeetingsRoomsDistinguishesAMalformedCatalogFromAnEmptyOne(t *testing.T) {
	// `list` refuses to blame provisioning when the server DID return meetings
	// and every one was unusable. `rooms` must draw the same line, or the two
	// commands disagree about the same catalog one keystroke apart.
	fake := newMeetingsFakeNextcloud(t, serveCatalog(
		`{"version":"cassini.viewer.catalog.v1","meetings":[{"title":"t"},{"title":"u"}]}`))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "rooms")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "the catalog is malformed rather than empty") {
		t.Errorf("expected the malformed-catalog note, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "mis-provisioned") {
		t.Errorf("a malformed catalog must not be reported as a provisioning problem:\n%s", stdout)
	}
}
