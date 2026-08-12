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

// meetingsCatalogItem pairs one decoded entry with the raw JSON it came from and
// its parsed date.
//
// They travel together rather than in maps keyed by id: ids are server data and
// nothing guarantees they are unique, so an id-keyed lookup would silently make
// duplicate entries share one date and drop one of their payloads.
type meetingsCatalogItem struct {
	entry meetingsCatalogEntry
	raw   json.RawMessage
	at    time.Time
	dated bool
}

// meetingsListing is a fetched catalog: the entries in display order plus where
// the bytes came from.
type meetingsListing struct {
	Version string
	Source  string
	Items   []meetingsCatalogItem
	// Skipped counts entries dropped as unusable (no id at all — including a
	// literal JSON null in the array), so the caller can say the list is
	// incomplete instead of rendering phantom meetings.
	Skipped int
}

// Entries returns the decoded entries in display order.
func (l meetingsListing) Entries() []meetingsCatalogEntry {
	entries := make([]meetingsCatalogEntry, 0, len(l.Items))
	for _, item := range l.Items {
		entries = append(entries, item.entry)
	}
	return entries
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
	}
	for i, raw := range catalog.Meetings {
		var entry meetingsCatalogEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return meetingsListing{}, fmt.Errorf("parse catalog entry %d from %s: %w", i, target.Redacted(), err)
		}
		// A JSON null decodes into a zero entry without error, and an entry with
		// no id cannot be fetched or referred to. Drop both rather than listing
		// a meeting that does not exist.
		if strings.TrimSpace(entry.ID) == "" {
			listing.Skipped++
			continue
		}
		at, dated := parseMeetingDateLabel(entry.DateLabel)
		listing.Items = append(listing.Items, meetingsCatalogItem{entry: entry, raw: raw, at: at, dated: dated})
	}
	sortMeetingsNewestFirst(listing.Items)
	return listing, nil
}

// find returns the entry with the given id.
//
// The catalog is the only index — there is no listing route — so an id absent
// from it is either not a meeting or not one this caller may read, which are
// the same answer by design.
func (l meetingsListing) find(id string) (meetingsCatalogEntry, error) {
	wanted := strings.TrimSpace(id)
	for _, item := range l.Items {
		if item.entry.ID == wanted {
			return item.entry, nil
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
// handles the relative shapes the exporter can emit ("./meetings/x.opus" and
// "meetings/x.opus") without branching on them.
//
// The result is then required to stay within the app's own published tree. The
// catalog is data fetched over the network, and every request carries the
// caller's Nextcloud app password: an entry naming another host would otherwise
// make this CLI hand that credential to whatever host the catalog asked for.
// Nothing legitimate needs that, so it is refused rather than trusted.
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
	if err := requireWithinPublishedTree(catalogURL, resolved, entry.ID, audioPath); err != nil {
		return nil, err
	}
	return resolved, nil
}

// requireWithinPublishedTree rejects a resolved asset URL that leaves the origin
// or the directory the catalog itself was served from.
//
// Two escapes matter, and both are refused for the same reason — the request
// would carry the caller's app password:
//
//   - a different scheme, host or port (an absolute audioPath, or one pointing at
//     a CDN), which would disclose the credential to a third party;
//   - a path climbing out of the published tree with "..", which would aim
//     credentialed requests at unrelated routes on the same Nextcloud.
func requireWithinPublishedTree(catalogURL *url.URL, resolved *url.URL, meetingID string, audioPath string) error {
	if resolved.Scheme != catalogURL.Scheme || resolved.Host != catalogURL.Host {
		return fmt.Errorf(
			"refusing to fetch meeting %q from %s: the catalog points outside the Nextcloud you configured (%s://%s), and the request would carry your app password",
			meetingID, resolved.Redacted(), catalogURL.Scheme, catalogURL.Host)
	}
	// The catalog's own directory is the boundary; url.Parse has already
	// normalised away any "." and ".." segments by this point.
	base := catalogURL.EscapedPath()
	if idx := strings.LastIndex(base, "/"); idx >= 0 {
		base = base[:idx+1]
	}
	if !strings.HasPrefix(resolved.EscapedPath(), base) {
		return fmt.Errorf(
			"refusing to fetch meeting %q from %s: audioPath %q escapes the published tree at %s",
			meetingID, resolved.EscapedPath(), audioPath, base)
	}
	return nil
}

// meetingDateLabelLayouts are the dateLabel forms the exporter emits, most
// precise first.
var meetingDateLabelLayouts = []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"}

// sortMeetingsNewestFirst orders items the way the viewer lists them: newest
// first by dateLabel, items with an unreadable label last, ties broken by id
// descending. Stable, so two items that compare equal keep the catalog's order.
func sortMeetingsNewestFirst(items []meetingsCatalogItem) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := items[i], items[j]
		switch {
		case left.dated && right.dated:
			if !left.at.Equal(right.at) {
				return left.at.After(right.at)
			}
		case left.dated != right.dated:
			return left.dated
		}
		return left.entry.ID > right.entry.ID
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
	meetings := make([]json.RawMessage, 0, len(listing.Items))
	for _, item := range listing.Items {
		meetings = append(meetings, item.raw)
	}
	document := struct {
		Version  string            `json:"version"`
		Source   string            `json:"source"`
		Meetings []json.RawMessage `json:"meetings"`
	}{
		Version:  listing.Version,
		Source:   listing.Source,
		Meetings: meetings,
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(document)
}
