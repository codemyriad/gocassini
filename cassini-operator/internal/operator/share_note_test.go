package operator

import (
	"path/filepath"
	"testing"
	"time"
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
