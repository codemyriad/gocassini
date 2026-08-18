package cassini

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestMeetingsListFiltersByRoom(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--room", "a7bc3k9x")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "meetings=2 ") {
		t.Errorf("expected 2 meetings, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "filter=room:a7bc3k9x excluded=2") {
		t.Errorf("expected the filter to be echoed with its exclusion count, got:\n%s", stdout)
	}
	for _, id := range []string{"SYNC1", "SYNC2"} {
		if !strings.Contains(stdout, "meeting="+id+" ") {
			t.Errorf("missing %s in:\n%s", id, stdout)
		}
	}
	for _, id := range []string{"LEGACY", "NOROOM"} {
		if strings.Contains(stdout, "meeting="+id+" ") {
			t.Errorf("%s should not match a different room:\n%s", id, stdout)
		}
	}
}

func TestMeetingsListFiltersByNameOnlyRoom(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	// The selector `meetings rooms` printed for a room recorded before Cassini
	// kept room ids. Filtering by it is the only way to reach those meetings.
	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--room", "name:Old Standup")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "meetings=1 ") || !strings.Contains(stdout, "meeting=LEGACY ") {
		t.Errorf("expected only LEGACY, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "room=name:Old Standup") {
		t.Errorf("expected the room column to show the selector, got:\n%s", stdout)
	}
}

func TestMeetingsListRoomFilterNeverMatchesARoomlessMeeting(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	// NOROOM has neither id nor name. No --room value selects it — including
	// the empty-looking ones someone might reach for.
	for _, selector := range []string{"name:", "NOROOM", "name:Untitled meeting"} {
		code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--room", selector)
		if code != 0 {
			t.Fatalf("--room %q exit=%d stderr=%q", selector, code, stderr)
		}
		if strings.Contains(stdout, "meeting=NOROOM ") {
			t.Errorf("--room %q matched a meeting that records no room:\n%s", selector, stdout)
		}
	}
}

func TestMeetingsListFiltersByDateRangeInclusively(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	// A bare --to must cover the whole day. SYNC2 is at 2026-08-04 10:30, so a
	// --to of 2026-08-04 that stopped at midnight would silently drop it — and
	// nothing in the output would say why.
	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--from", "2026-08-04", "--to", "2026-08-04")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "meetings=1 ") || !strings.Contains(stdout, "meeting=SYNC2 ") {
		t.Errorf("expected exactly SYNC2, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "filter=from:2026-08-04 00:00:00 to:2026-08-04 23:59:59") {
		t.Errorf("expected the resolved bounds to be shown, got:\n%s", stdout)
	}
}

func TestMeetingsListCombinesFiltersWithAnd(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL,
		"list", "--room", "a7bc3k9x", "--from", "2026-08-05")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	// SYNC1 is in the room AND after the date; SYNC2 is in the room but before it.
	if !strings.Contains(stdout, "meetings=1 ") || !strings.Contains(stdout, "meeting=SYNC1 ") {
		t.Errorf("expected exactly SYNC1, got:\n%s", stdout)
	}
}

func TestMeetingsListRejectsAnUnusableDateBeforeTheNetworkCall(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--from", "last tuesday")

	if code != 2 {
		t.Fatalf("exit=%d, want 2 (a usage error)", code)
	}
	if !strings.Contains(stderr, "list configuration error") || !strings.Contains(stderr, "2026-08-11") {
		t.Errorf("expected a usage error naming the accepted forms, got %q", stderr)
	}
	// Making the caller wait for a round trip to hear their flag is malformed
	// is pure cost, so the validation must come first.
	if len(fake.requests) != 0 {
		t.Errorf("a malformed date reached the network: %v", fake.requests)
	}
}

func TestMeetingsListRejectsAnInvertedRange(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, _, stderr := runMeetingsCLI(t, fake.server.URL,
		"list", "--from", "2026-08-31", "--to", "2026-08-01")

	if code != 2 {
		t.Fatalf("exit=%d, want 2", code)
	}
	// An inverted range can only match nothing, and an empty list looks exactly
	// like "you may read no recordings" — a far more alarming answer.
	if !strings.Contains(stderr, "did you swap them?") {
		t.Errorf("expected an inverted-range error, got %q", stderr)
	}
	if len(fake.requests) != 0 {
		t.Errorf("an inverted range reached the network: %v", fake.requests)
	}
}

func TestMeetingsListDropsUndatedEntriesFromADatedRangeAndSaysSo(t *testing.T) {
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"DATED","title":"t","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/DATED.opus"},
	  {"id":"UNDATED","title":"t","dateLabel":"some-slug","audioPath":"./meetings/UNDATED.opus"}]}`
	fake := newMeetingsFakeNextcloud(t, serveCatalog(catalog))

	// An undated entry has a zero timestamp, which fails every --from and
	// matches every --to. Left implicit, the same meeting would appear or
	// vanish depending on which end of the range the caller typed.
	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--to", "2030-01-01")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "meeting=UNDATED ") {
		t.Errorf("an undated meeting survived a dated range:\n%s", stdout)
	}
	if !strings.Contains(stdout, "have a date this build cannot read") {
		t.Errorf("expected the dropped undated entry to be reported, got:\n%s", stdout)
	}
}

func TestMeetingsListFilteredToNothingDoesNotBlameProvisioning(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--room", "no-such-room")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, "your filter excluded all 4 readable meeting(s)") {
		t.Errorf("expected a filter explanation, got:\n%s", stdout)
	}
	// The pre-existing empty-list note would send someone to debug a
	// provisioning failure that does not exist.
	if strings.Contains(stdout, "mis-provisioned") {
		t.Errorf("a filtered-empty list must not blame provisioning:\n%s", stdout)
	}
}

func TestMeetingsListJSONEchoesTheFilterAndCounts(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL,
		"list", "--json", "--room", "a7bc3k9x", "--from", "2026-08-01")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var document struct {
		Skipped int `json:"skipped"`
		Filter  *struct {
			From string `json:"from"`
			To   string `json:"to"`
			Room string `json:"room"`
		} `json:"filter"`
		Excluded        int               `json:"excluded"`
		ExcludedUndated int               `json:"excludedUndated"`
		Meetings        []json.RawMessage `json:"meetings"`
	}
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("parse list JSON: %v (%q)", err, stdout)
	}
	if document.Filter == nil {
		t.Fatalf("expected the filter echoed in JSON: %q", stdout)
	}
	if document.Filter.Room != "a7bc3k9x" || document.Filter.From != "2026-08-01 00:00:00" || document.Filter.To != "" {
		t.Errorf("filter echo = %+v", *document.Filter)
	}
	if document.Excluded != 2 || len(document.Meetings) != 2 {
		t.Errorf("excluded=%d meetings=%d, want 2 and 2", document.Excluded, len(document.Meetings))
	}
	// skipped keeps meaning "the catalog is malformed" and must never absorb
	// filtered-out entries: an agent reads a non-zero skipped as a server-side
	// problem, which a filter doing its job is not.
	if document.Skipped != 0 {
		t.Errorf("skipped = %d, want 0 — filtering is not skipping", document.Skipped)
	}
}

func TestMeetingsListJSONOmitsTheFilterWhenThereIsNone(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(roomCatalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--json")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("parse list JSON: %v", err)
	}
	if _, ok := document["filter"]; ok {
		t.Errorf("filter must be absent on an unfiltered list: %q", stdout)
	}
	if document["excluded"] != float64(0) {
		t.Errorf("excluded = %v, want 0", document["excluded"])
	}
	if got := document["meetings"].([]any); len(got) != 4 {
		t.Errorf("meetings = %d, want all 4", len(got))
	}
}

func TestParseMeetingsFilterBoundAcceptsWhatTheCatalogPrints(t *testing.T) {
	cases := []struct {
		value      string
		endOfRange bool
		want       string
	}{
		{"2026-08-11", false, "2026-08-11 00:00:00"},
		{"2026-08-11", true, "2026-08-11 23:59:59"},
		// An explicit time is used exactly, at both ends: the caller said what
		// they meant, so widening it to the whole day would override them.
		{"2026-08-11 14:30", false, "2026-08-11 14:30:00"},
		{"2026-08-11 14:30", true, "2026-08-11 14:30:00"},
		{"2026-08-11 14:30:05", true, "2026-08-11 14:30:05"},
	}
	for _, tc := range cases {
		at, err := parseMeetingsFilterBound(tc.value, tc.endOfRange)
		if err != nil {
			t.Fatalf("parseMeetingsFilterBound(%q, %t): %v", tc.value, tc.endOfRange, err)
		}
		if got := at.Format(meetingsFilterStampLayout); got != tc.want {
			t.Errorf("parseMeetingsFilterBound(%q, %t) = %s, want %s", tc.value, tc.endOfRange, got, tc.want)
		}
		// The catalog's labels carry no timezone (D-484), so neither may these:
		// asserting one would invent an instant the data cannot justify. A
		// zone-less time.Parse yields UTC, which is the same space
		// parseMeetingDateLabel puts the catalog's own labels in — so the two
		// sides of every comparison agree.
		if at.Location() != time.UTC {
			t.Errorf("parseMeetingsFilterBound(%q) location = %v, want UTC", tc.value, at.Location())
		}
	}
}

func TestMeetingsRoomSelectorRoundTripsThroughThePrintedForm(t *testing.T) {
	// `meetings rooms` prints the selector flattened, and the printed value is
	// what a caller pastes back. Comparing against the raw name would make a
	// room whose name contains a tab advertise a selector that matches nothing
	// — hiding the whole room behind its own listing.
	entry := meetingsCatalogEntry{ID: "A", RoomName: "Weekly\tSync"}
	printed := oneLineField(entry.roomSelector())
	if printed != "name:Weekly Sync" {
		t.Fatalf("printed selector = %q, want %q", printed, "name:Weekly Sync")
	}
	if !meetingMatchesRoom(entry, printed) {
		t.Errorf("--room %q does not match the entry that printed it", printed)
	}
}

func TestMeetingsRoomIDIsMatchedBeforeTheNamePrefixIsInterpreted(t *testing.T) {
	// A room id is opaque server data and nothing stops one from starting with
	// the name-selector prefix. If one does, the entry whose selector was
	// printed must still be the entry that selector selects.
	entry := meetingsCatalogEntry{ID: "A", RoomID: "name:Weekly"}
	if got := entry.roomSelector(); got != "name:Weekly" {
		t.Fatalf("selector = %q", got)
	}
	if !meetingMatchesRoom(entry, "name:Weekly") {
		t.Error("an id that looks like a name selector must still match by id")
	}
	// And a genuinely name-only entry is still matched by the same string —
	// they are not distinguishable at the selector level, which is why the
	// grouping keeps them apart by the presence of an id rather than by string.
	nameOnly := meetingsCatalogEntry{ID: "B", RoomName: "Weekly"}
	if !meetingMatchesRoom(nameOnly, "name:Weekly") {
		t.Error("a name-only entry must match its own name selector")
	}
}

func TestMeetingsListUndatedNoteCountsOnlyTheFilteredRoom(t *testing.T) {
	// An undated meeting in a DIFFERENT room is not something the date range
	// cost this caller, and reporting it would send them to re-list without
	// --from/--to, which surfaces nothing extra.
	catalog := `{"version":"cassini.viewer.catalog.v1","meetings":[
	  {"id":"INROOM","title":"t","dateLabel":"2026-08-11 10:32","audioPath":"./meetings/INROOM.opus","roomId":"tok"},
	  {"id":"OTHER1","title":"t","dateLabel":"some-slug","audioPath":"./meetings/OTHER1.opus","roomId":"other"},
	  {"id":"OTHER2","title":"t","dateLabel":"other-slug","audioPath":"./meetings/OTHER2.opus","roomId":"other"}]}`
	fake := newMeetingsFakeNextcloud(t, serveCatalog(catalog))

	code, stdout, stderr := runMeetingsCLI(t, fake.server.URL, "list", "--room", "tok", "--from", "2026-01-01")

	if code != 0 {
		t.Fatalf("exit=%d stderr=%q", code, stderr)
	}
	if strings.Contains(stdout, "have a date this build cannot read") {
		t.Errorf("undated meetings from another room were reported as lost to the date filter:\n%s", stdout)
	}
	if !strings.Contains(stdout, "meeting=INROOM ") {
		t.Errorf("expected INROOM, got:\n%s", stdout)
	}
}

func TestOneLineFieldNeutralisesLineAndBidiSeparators(t *testing.T) {
	// unicode.IsControl is false for every rune above U+00FF, so these two
	// classes used to pass through: U+2028/U+2029 are line terminators to
	// Python, JavaScript and Java (forging a whole record for exactly the
	// consumers most likely to be parsing this), and the bidi overrides reverse
	// how the rest of the line renders in any terminal that honours them.
	for _, bad := range []string{" ", " ", "‮", "‪", "⁦", "⁩", "‎", "‏"} {
		got := oneLineField("Sync" + bad + "room=forged")
		if strings.ContainsAny(got, bad) {
			t.Errorf("oneLineField kept %U, which can break out of the line: %q", []rune(bad)[0], got)
		}
	}
	// Zero-width joiners are load-bearing in Arabic, Indic scripts and emoji
	// sequences, and must survive — flattening them corrupts real names for no
	// security benefit.
	for _, good := range []string{"‌", "‍"} {
		if got := oneLineField("a" + good + "b"); !strings.Contains(got, good) {
			t.Errorf("oneLineField dropped %U, which is legitimate text: %q", []rune(good)[0], got)
		}
	}
}
