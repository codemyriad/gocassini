package cassini

import (
	"context"
	"testing"
)

// contentCatalog exercises the three states the summary/word-count pair has to
// keep apart: stated-positive, stated-negative, and never stated at all.
const contentCatalog = `{
  "version": "cassini.viewer.catalog.v1",
  "meetings": [
    {"id": "RICH", "title": "Weekly Sync", "dateLabel": "2026-08-11 10:32",
     "audioPath": "./meetings/RICH.opus", "hasSummary": true, "wordCount": 3468},
    {"id": "SILENT", "title": "Nobody spoke", "dateLabel": "2026-08-04 10:30",
     "audioPath": "./meetings/SILENT.opus", "hasSummary": false, "wordCount": 0},
    {"id": "LEGACY", "title": "Published before D-716", "dateLabel": "2026-01-04 09:00",
     "audioPath": "./meetings/LEGACY.opus"}
  ]
}`

func TestMeetingsCatalogEntryCarriesSummaryPresenceAndWordCount(t *testing.T) {
	fake := newMeetingsFakeNextcloud(t, serveCatalog(contentCatalog))
	client := newMeetingsClient(meetingsConfig{
		nextcloudURL: fake.server.URL, user: "alice", appPassword: "app-pw-1234", appID: meetingsDefaultAppID,
	})
	listing, err := client.fetchCatalog(context.Background())
	if err != nil {
		t.Fatalf("fetchCatalog: %v", err)
	}

	byID := map[string]meetingsCatalogEntry{}
	for _, entry := range listing.Entries() {
		byID[entry.ID] = entry
	}

	rich := byID["RICH"]
	if rich.HasSummary == nil || !*rich.HasSummary {
		t.Errorf("RICH hasSummary = %v, want a stated true", rich.HasSummary)
	}
	if rich.WordCount == nil || *rich.WordCount != 3468 {
		t.Errorf("RICH wordCount = %v, want a stated 3468", rich.WordCount)
	}

	// The pair's whole reason for existing: an entry that says "no summary" and
	// "no words" must not read the same as one that says nothing. A plain bool
	// and a plain int would collapse these two meetings into each other.
	silent := byID["SILENT"]
	if silent.HasSummary == nil || *silent.HasSummary {
		t.Errorf("SILENT hasSummary = %v, want a stated false", silent.HasSummary)
	}
	if silent.WordCount == nil || *silent.WordCount != 0 {
		t.Errorf("SILENT wordCount = %v, want a stated 0", silent.WordCount)
	}

	legacy := byID["LEGACY"]
	if legacy.HasSummary != nil {
		t.Errorf("LEGACY hasSummary = %v, want nil: an archive published before the field said nothing", *legacy.HasSummary)
	}
	if legacy.WordCount != nil {
		t.Errorf("LEGACY wordCount = %v, want nil: an archive published before the field said nothing", *legacy.WordCount)
	}
}
