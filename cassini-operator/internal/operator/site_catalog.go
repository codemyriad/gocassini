package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The published site's catalog.json is the viewer's index:
//
//	{"version": "cassini.viewer.catalog.v1", "meetings": [ {...}, {...} ]}
//
// Publishing one meeting has to *upsert* into that list rather than replace it,
// so the rest of the archive survives. Entries are carried as json.RawMessage
// and re-emitted verbatim: the exporter owns their shape, and this code has no
// business normalising fields it does not understand.
type siteCatalog struct {
	Version  string            `json:"version"`
	Meetings []json.RawMessage `json:"meetings"`
}

// loadSiteCatalog reads a site's catalog. A missing catalog is not an error —
// that is simply the first publish into an empty site. A malformed one is,
// because silently replacing it would lose the archive it indexes.
func loadSiteCatalog(sitePath string) (siteCatalog, bool, error) {
	raw, err := os.ReadFile(filepath.Join(sitePath, "catalog.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return siteCatalog{}, false, nil
		}
		return siteCatalog{}, false, fmt.Errorf("read site catalog: %w", err)
	}
	var catalog siteCatalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return siteCatalog{}, false, fmt.Errorf("parse site catalog %s: %w", filepath.Join(sitePath, "catalog.json"), err)
	}
	return catalog, true, nil
}

// catalogEntryID reads the "id" field an entry is keyed by.
func catalogEntryID(entry json.RawMessage) (string, error) {
	var probe struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(entry, &probe); err != nil {
		return "", fmt.Errorf("parse catalog entry: %w", err)
	}
	id := strings.TrimSpace(probe.ID)
	if id == "" {
		return "", fmt.Errorf("catalog entry has no id")
	}
	return id, nil
}

// catalogEntryAssets lists the site-relative paths an entry points at, so the
// sink knows which files to carry across with it. Portable meetings carry an
// audioPath (a .opus); older exports carry an artifactPath (a directory).
func catalogEntryAssets(entry json.RawMessage) ([]string, error) {
	var probe struct {
		AudioPath    string `json:"audioPath"`
		ArtifactPath string `json:"artifactPath"`
	}
	if err := json.Unmarshal(entry, &probe); err != nil {
		return nil, fmt.Errorf("parse catalog entry: %w", err)
	}
	var assets []string
	for _, candidate := range []string{probe.AudioPath, probe.ArtifactPath} {
		rel, ok := siteRelativeAsset(candidate)
		if !ok {
			continue
		}
		assets = append(assets, rel)
	}
	return assets, nil
}

// siteRelativeAsset normalises a catalog path into a path inside the site, and
// rejects anything that is not: an absolute path, a URL, or one that escapes
// the site root. A published catalog can name a remote recordings base URL
// (the exporter's recordingsBaseUrl), which is not ours to copy.
func siteRelativeAsset(raw string) (string, bool) {
	candidate := strings.TrimSpace(raw)
	if candidate == "" {
		return "", false
	}
	if strings.Contains(candidate, "://") {
		return "", false
	}
	if strings.HasPrefix(candidate, "/") {
		return "", false
	}
	candidate = strings.TrimPrefix(candidate, "./")
	cleaned := filepath.Clean(filepath.FromSlash(candidate))
	if cleaned == "." || cleaned == string(filepath.Separator) {
		return "", false
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", false
	}
	return cleaned, true
}

// catalogEntryOverlay is what the operator knows about a meeting that its
// artifact does not.
//
// Only the room name so far, and the reason it is here rather than in the file
// is the whole of D-640's second decision: a room's display name is editable
// and a published recording is not, so freezing a copy of the name into every
// artifact meant a rename could only be honoured by rewriting every file that
// room ever produced. The operator resolves the CURRENT name from the job's
// Talk binding on every publish, and the catalog — which is rewritten anyway —
// is where it lands.
//
// Empty means "the operator has nothing to say", which is the normal state for
// a non-Talk job and for one whose room lookup failed. It must not erase a name
// an earlier publish did resolve.
type catalogEntryOverlay struct {
	RoomName string
}

func (o catalogEntryOverlay) empty() bool {
	return strings.TrimSpace(o.RoomName) == ""
}

// catalogCarriedFields are the entry fields that survive a republish by being
// copied from the entry being replaced.
//
// A field belongs here when the artifact cannot supply it. `roomName` cannot,
// by design (see catalogEntryOverlay). `roomId` can and normally does — the
// exporter derives it from the file — but a recording published before the
// field existed carries none until the backfill re-tags it, and dropping a
// recovered id on the next re-seal is exactly the regression D-640 exists to
// close.
//
// Deliberately a carry-forward and not a merge of everything: an entry field
// the exporter DID emit always wins, so a corrected title or a re-derived room
// id lands rather than being pinned by whatever was published first.
var catalogCarriedFields = []string{"roomName", "roomId"}

// upsertSiteCatalog merges incoming entries into existing: an entry whose id is
// already present is replaced in place (so a republished meeting updates rather
// than duplicates, and the archive's order does not churn), and a new one is
// appended.
//
// Entries are still carried as json.RawMessage and re-emitted verbatim wherever
// nothing needs to change, so a field this code has never heard of survives. The
// two cases that do need to change — the operator's overlay, and the fields a
// republish would otherwise drop — are applied by decoding the one entry
// involved, not by normalising the archive.
func upsertSiteCatalog(existing siteCatalog, incoming siteCatalog, overlay catalogEntryOverlay) (siteCatalog, error) {
	merged := siteCatalog{
		Version:  existing.Version,
		Meetings: append([]json.RawMessage{}, existing.Meetings...),
	}
	// The incoming catalog comes from the exporter that just ran, so it names
	// the current format; an empty one leaves whatever the archive already had.
	if strings.TrimSpace(incoming.Version) != "" {
		merged.Version = incoming.Version
	}

	index := make(map[string]int, len(merged.Meetings))
	for position, entry := range merged.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			return siteCatalog{}, fmt.Errorf("existing catalog: %w", err)
		}
		index[id] = position
	}

	for _, entry := range incoming.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			return siteCatalog{}, fmt.Errorf("incoming catalog: %w", err)
		}
		position, replacing := index[id]
		var previous json.RawMessage
		if replacing {
			previous = merged.Meetings[position]
		}
		entry, err = applyCatalogEntryOverlay(entry, previous, overlay)
		if err != nil {
			return siteCatalog{}, fmt.Errorf("incoming catalog entry %s: %w", id, err)
		}
		if replacing {
			merged.Meetings[position] = entry
			continue
		}
		index[id] = len(merged.Meetings)
		merged.Meetings = append(merged.Meetings, entry)
	}
	return merged, nil
}

// applyCatalogEntryOverlay stamps what the operator knows onto one entry and
// carries forward what a republish would otherwise drop.
//
// Precedence, highest first:
//
//  1. the incoming entry's own value — the exporter read it out of the file it
//     just published, so it is the freshest claim the artifact can make;
//  2. the operator's overlay — for a field the artifact cannot carry at all;
//  3. the entry being replaced — for a value that only ever existed in the
//     catalog, or that the artifact has not been re-tagged with yet.
//
// The entry is returned untouched, bytes and all, when none of that applies —
// which is the common case, and keeps a plain republish from rewriting the
// archive's formatting.
func applyCatalogEntryOverlay(entry json.RawMessage, previous json.RawMessage, overlay catalogEntryOverlay) (json.RawMessage, error) {
	if overlay.empty() && len(previous) == 0 {
		return entry, nil
	}

	fields := map[string]any{}
	if err := json.Unmarshal(entry, &fields); err != nil {
		return nil, fmt.Errorf("parse entry: %w", err)
	}

	changed := false
	if name := strings.TrimSpace(overlay.RoomName); name != "" && catalogStringField(fields, "roomName") != name {
		fields["roomName"] = name
		changed = true
	}

	if len(previous) > 0 {
		var previousFields map[string]any
		if err := json.Unmarshal(previous, &previousFields); err != nil {
			// A malformed entry already in the archive is not a reason to fail
			// the publish: the incoming entry is well-formed and replacing it is
			// an improvement. Carry nothing forward and move on.
			previousFields = nil
		}
		for _, field := range catalogCarriedFields {
			if catalogStringField(fields, field) != "" {
				continue
			}
			carried := catalogStringField(previousFields, field)
			if carried == "" {
				continue
			}
			fields[field] = carried
			changed = true
		}
	}

	if !changed {
		return entry, nil
	}
	updated, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode entry: %w", err)
	}
	return updated, nil
}

// catalogStringField reads a field that is only meaningful as a non-blank
// string. A non-string value reads as absent rather than as an error: the
// exporter owns the entry shape, and a publish is the wrong moment to start
// adjudicating it.
func catalogStringField(fields map[string]any, name string) string {
	if fields == nil {
		return ""
	}
	value, ok := fields[name].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

// writeSiteCatalog writes the catalog atomically, matching the exporter's
// two-space indent and trailing newline so a hand diff against an exported site
// stays readable.
func writeSiteCatalog(sitePath string, catalog siteCatalog) error {
	if catalog.Meetings == nil {
		catalog.Meetings = []json.RawMessage{}
	}
	body, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal site catalog: %w", err)
	}
	body = append(body, '\n')
	if err := writeFileAtomic(filepath.Join(sitePath, "catalog.json"), body, 0o644); err != nil {
		return fmt.Errorf("write site catalog: %w", err)
	}
	return nil
}
