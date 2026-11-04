package operator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The note on a share, in the words the contract fixes: what the file is,
// when, how long, and where to open it. No URL.
func TestShareNoteWording(t *testing.T) {
	start := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		facts    meetingFacts
		imported bool
		want     string
	}{
		"a recording":      {meetingFacts{Title: "Weekly sync", StartedAt: start, Duration: 42 * time.Minute}, false, "Recording of Weekly sync, Friday 30 October 2026, 10:00 (42 min). Open it in Cassini."},
		"an import":        {meetingFacts{Title: "Weekly sync", StartedAt: start, Duration: 42 * time.Minute}, true, "Imported meeting Weekly sync, Friday 30 October 2026, 10:00 (42 min). Open it in Cassini."},
		"over an hour":     {meetingFacts{Title: "Planning", StartedAt: start, Duration: 65 * time.Minute}, false, "Recording of Planning, Friday 30 October 2026, 10:00 (1 h 05). Open it in Cassini."},
		"very short":       {meetingFacts{Title: "Test", StartedAt: start, Duration: 20 * time.Second}, false, "Recording of Test, Friday 30 October 2026, 10:00 (under a minute). Open it in Cassini."},
		"no title":         {meetingFacts{StartedAt: start, Duration: 42 * time.Minute}, false, "Recording from Friday 30 October 2026, 10:00 (42 min). Open it in Cassini."},
		"no date":          {meetingFacts{Title: "Weekly sync", Duration: 42 * time.Minute}, false, "Recording of Weekly sync (42 min). Open it in Cassini."},
		"nothing known":    {meetingFacts{}, false, "Recording. Open it in Cassini."},
		"multi-line title": {meetingFacts{Title: "Weekly\nsync", StartedAt: start}, false, "Recording of Weekly sync, Friday 30 October 2026, 10:00. Open it in Cassini."},
	} {
		t.Run(name, func(t *testing.T) {
			if got := tc.facts.shareNote(tc.imported); got != tc.want {
				t.Fatalf("note\n got: %s\nwant: %s", got, tc.want)
			}
		})
	}
	// A rounded minute, never a second count.
	if got := formatMeetingDuration(42*time.Minute + 40*time.Second); got != "43 min" {
		t.Fatalf("duration = %q", got)
	}
}

// Every share a publication makes carries the note, built from what the
// operator knows about the meeting at that moment: the indexed title and
// the job's recorded times.
func TestRecordingSharesCarryANoteNamingTheMeeting(t *testing.T) {
	f := newDirectPublishFixture(t, []sharePrincipal{{Type: "user", ID: "bob"}, {Type: "group", ID: "marketing"}})
	metadata, err := openMeetingMetadataStore(filepath.Join(t.TempDir(), meetingMetadataFilename), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = metadata.Close() })
	f.rt.meetingMetadata = metadata
	setJobTimes(t, f.rt, directPublishJob, "2026-11-01T10:00:00.000000000Z", "2026-11-01T10:30:00.000000000Z")
	if err := f.deliver(); err != nil {
		t.Fatal(err)
	}
	const want = "Recording of Standup, Sunday 1 November 2026, 10:00 (30 min). Open it in Cassini."
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, key := range []string{"0/alice", "0/bob", "1/marketing"} {
		if !f.shares[key] {
			t.Fatalf("not shared with %s: %v", key, f.shares)
		}
		if f.notes[key] != want {
			t.Fatalf("note on %s = %q, want %q", key, f.notes[key], want)
		}
	}
}

// A title is cut between characters, never inside one: a Cyrillic title
// well over 80 bytes but under the cap must come through whole, and a longer
// CJK one is still valid UTF-8 after the cut. A note with a broken character
// in it is refused by a utf8mb4 Nextcloud database, which fails the publish.
func TestMeetingTitlesAreCutBetweenCharacters(t *testing.T) {
	cyrillic := strings.TrimSpace(strings.Repeat("Совещание ", 6)) // 59 characters, 113 bytes
	if got := oneLineTitle(cyrillic); got != cyrillic {
		t.Fatalf("a %d-byte title was cut: %q", len(cyrillic), got)
	}
	cjk := strings.Repeat("会議", 70) // 140 characters
	got := oneLineTitle(cjk)
	if !utf8.ValidString(got) {
		t.Fatalf("the cut title is not valid UTF-8: %q", got)
	}
	if runes := []rune(got); len(runes) != maxShareNoteTitle+1 || !strings.HasSuffix(got, "…") || !strings.HasPrefix(cjk, strings.TrimSuffix(got, "…")) {
		t.Fatalf("cut title = %q (%d characters)", got, len(runes))
	}
	if got := oneLineTitle("  Weekly\r\n\tsync  "); got != "Weekly   sync" {
		t.Fatalf("one-line title = %q", got)
	}
	// Through the operator's own record of the meeting, and into the note.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ctx := context.Background()
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J1", Stage: "done", State: "succeeded", CreatedAt: "2026-10-30T09:00:00Z"})
	indexMeeting(t, rt, "J1.opus", cyrillic, 2520000)
	facts := rt.meetingFactsFor(ctx, "J1")
	if facts.Title != cyrillic {
		t.Fatalf("indexed title came back as %q", facts.Title)
	}
	if note := facts.shareNote(false); !utf8.ValidString(note) || !strings.Contains(note, cyrillic) {
		t.Fatalf("note = %q", note)
	}
}
