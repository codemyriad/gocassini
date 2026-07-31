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

// upsertSiteCatalog merges incoming entries into existing: an entry whose id is
// already present is replaced in place (so a republished meeting updates rather
// than duplicates, and the archive's order does not churn), and a new one is
// appended.
func upsertSiteCatalog(existing siteCatalog, incoming siteCatalog) (siteCatalog, error) {
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
		if position, ok := index[id]; ok {
			merged.Meetings[position] = entry
			continue
		}
		index[id] = len(merged.Meetings)
		merged.Meetings = append(merged.Meetings, entry)
	}
	return merged, nil
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
