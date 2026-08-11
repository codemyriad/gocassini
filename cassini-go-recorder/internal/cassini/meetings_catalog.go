package cassini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

// meetingsCatalogVersion is the only catalog schema this CLI reads. The viewer
// rejects anything else outright and so does this, rather than guessing at
// fields that may have moved.
const meetingsCatalogVersion = "cassini.viewer.catalog.v1"

// meetingsCatalog is the per-caller meeting index.
//
// Entries are kept as raw JSON as well as decoded: the exporter owns this shape
// and the app's filter passes entries through verbatim, so `--json` re-emits
// exactly what the server sent. That keeps one payload contract for every
// consumer — this CLI, the viewer, and anything wrapping either — instead of a
// re-normalised second one.
type meetingsCatalog struct {
	Version  string            `json:"version"`
	Meetings []json.RawMessage `json:"meetings"`
}

// meetingsCatalogEntry is the decoded view of one catalog entry, mirroring the
// producer's type in cassini-viewer/src/viewer/catalog.ts. Unknown fields are
// tolerated: the raw entry, not this struct, is what gets re-emitted.
type meetingsCatalogEntry struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	DateLabel        string `json:"dateLabel"`
	ArtifactPath     string `json:"artifactPath,omitempty"`
	AudioPath        string `json:"audioPath,omitempty"`
	SpeakerCount     int    `json:"speakerCount,omitempty"`
	SegmentCount     int    `json:"segmentCount,omitempty"`
	DigestDurationMS int64  `json:"digestDurationMs,omitempty"`
}

// meetingsListing is a fetched catalog: the decoded entries in display order,
// each paired with the raw JSON it came from, plus where the bytes came from.
type meetingsListing struct {
	Version   string
	Source    string
	Entries   []meetingsCatalogEntry
	RawByID   map[string]json.RawMessage
	rawSorted []json.RawMessage
}

// fetchCatalog GETs the caller's meeting index and decodes it.
//
// An empty list is a legitimate 200: the app answers 200 with no meetings both
// when the caller genuinely has none and when the recordings substrate is
// mis-provisioned. That ambiguity is the server's deliberate fail-closed
// behaviour, so this returns success and lets the caller say so.
func (c *meetingsClient) fetchCatalog(ctx context.Context) (meetingsListing, error) {
	target, err := c.catalogURL()
	if err != nil {
		return meetingsListing{}, err
	}
	resp, err := c.get(ctx, target, c.json)
	if err != nil {
		return meetingsListing{}, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return meetingsListing{}, fmt.Errorf("read catalog from %s: %w", target.Redacted(), err)
	}

	var catalog meetingsCatalog
	if err := json.Unmarshal(body, &catalog); err != nil {
		return meetingsListing{}, fmt.Errorf("parse catalog from %s: %w", target.Redacted(), err)
	}
	if catalog.Version != meetingsCatalogVersion {
		return meetingsListing{}, fmt.Errorf("unsupported catalog version %q (this build reads %q)", catalog.Version, meetingsCatalogVersion)
	}

	listing := meetingsListing{
		Version: catalog.Version,
		Source:  meetingsSource(resp.Header),
		RawByID: make(map[string]json.RawMessage, len(catalog.Meetings)),
	}
	for i, raw := range catalog.Meetings {
		var entry meetingsCatalogEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return meetingsListing{}, fmt.Errorf("parse catalog entry %d from %s: %w", i, target.Redacted(), err)
		}
		listing.Entries = append(listing.Entries, entry)
		listing.RawByID[entry.ID] = raw
	}
	sortMeetingsNewestFirst(listing.Entries)
	listing.rawSorted = make([]json.RawMessage, 0, len(listing.Entries))
	for _, entry := range listing.Entries {
		listing.rawSorted = append(listing.rawSorted, listing.RawByID[entry.ID])
	}
	return listing, nil
}

// find returns the entry with the given id.
//
// The catalog is the only index — there is no listing route — so an id absent
// from it is either not a meeting or not one this caller may read, which are
// the same answer by design.
func (l meetingsListing) find(id string) (meetingsCatalogEntry, error) {
	wanted := strings.TrimSpace(id)
	for _, entry := range l.Entries {
		if entry.ID == wanted {
			return entry, nil
		}
	}
	return meetingsCatalogEntry{}, fmt.Errorf("no recording %q you can read; run `cassini meetings list` to see what this account can read", wanted)
}

// resolveAudioURL turns a catalog entry into the absolute URL of its portable
// .opus.
//
// audioPath is the contract, not the id. The two coincide today — the id is the
// job id and the file is named after it — but the exporter owns the path, and
// resolving it against the catalog's own URL is what the viewer does. It also
// handles all three shapes the exporter can emit ("./meetings/x.opus",
// "meetings/x.opus", and an absolute URL) without branching on any of them.
func resolveMeetingAudioURL(catalogURL *url.URL, entry meetingsCatalogEntry) (*url.URL, error) {
	audioPath := strings.TrimSpace(entry.AudioPath)
	if audioPath == "" {
		if strings.TrimSpace(entry.ArtifactPath) != "" {
			return nil, fmt.Errorf("meeting %q predates the single-file format: it has only an artifactPath directory, with no portable .opus to fetch", entry.ID)
		}
		return nil, fmt.Errorf("meeting %q has no audioPath in the catalog, so there is nothing to fetch", entry.ID)
	}
	resolved, err := catalogURL.Parse(audioPath)
	if err != nil {
		return nil, fmt.Errorf("meeting %q has an unusable audioPath %q: %w", entry.ID, audioPath, err)
	}
	return resolved, nil
}

// meetingDateLabelLayouts are the dateLabel forms the exporter emits, most
// precise first.
var meetingDateLabelLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}

// sortMeetingsNewestFirst orders entries the way the viewer lists them: newest
// first by dateLabel, entries with an unreadable label last, ties broken by id
// descending so the order is stable.
func sortMeetingsNewestFirst(entries []meetingsCatalogEntry) {
	parsed := make(map[string]time.Time, len(entries))
	for _, entry := range entries {
		if at, ok := parseMeetingDateLabel(entry.DateLabel); ok {
			parsed[entry.ID] = at
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		left, leftOK := parsed[entries[i].ID]
		right, rightOK := parsed[entries[j].ID]
		switch {
		case leftOK && rightOK:
			if !left.Equal(right) {
				return left.After(right)
			}
		case leftOK != rightOK:
			return leftOK
		}
		return entries[i].ID > entries[j].ID
	})
}

func parseMeetingDateLabel(label string) (time.Time, bool) {
	value := strings.TrimSpace(label)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range meetingDateLabelLayouts {
		if at, err := time.Parse(layout, value); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

// writeMeetingsCatalogJSON re-emits the catalog in display order, preserving
// each entry's bytes verbatim and adding where they came from.
func writeMeetingsCatalogJSON(out io.Writer, listing meetingsListing) error {
	document := struct {
		Version  string            `json:"version"`
		Source   string            `json:"source"`
		Meetings []json.RawMessage `json:"meetings"`
	}{
		Version:  listing.Version,
		Source:   listing.Source,
		Meetings: listing.rawSorted,
	}
	if document.Meetings == nil {
		document.Meetings = []json.RawMessage{}
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
