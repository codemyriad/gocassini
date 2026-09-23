package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// searchArchiveInventory is an as-of list from the storage preflight or local
// site catalog. The index cannot supply this universe itself: older meetings
// may have no meeting_index row. Unsupported counts non-Opus entries, including
// legacy directory-shaped meetings which the search index cannot join to.
type searchArchiveInventory struct {
	OpusNames   []string
	Unsupported int
	// CatalogPresent means the storage probe saw catalog.json beside the
	// recordings, not that it parsed or cross-checked its contents.
	CatalogPresent bool
	CheckedAt      string
}

// searchInventory returns a cheap authoritative archive snapshot where one is
// already available. The Nextcloud branch reads the cached storage preflight;
// the local branch reads its on-disk catalog. It never starts network traffic
// from a health read. A false known means no valid snapshot has been taken.
func (rt *Runtime) searchInventory() (inventory searchArchiveInventory, known bool, err error) {
	if rt.resolvedPublishSinkName() == publishSinkNextcloudFiles {
		accessControlled, resolved := ncStorage.mode()
		if !resolved {
			return searchArchiveInventory{}, false, nil
		}
		probe, checkedAt, hasProbe := ncAccessSubstrate.lastProbeWithTime()
		if !hasProbe || checkedAt == "" {
			return searchArchiveInventory{}, false, nil
		}
		inventory, known = inventoryFromStorageProbe(probe, accessControlled, checkedAt)
		return inventory, known, nil
	}

	if strings.TrimSpace(rt.cfg.SiteRoot) == "" {
		return searchArchiveInventory{}, false, nil
	}
	catalog, exists, err := loadSiteCatalog(rt.cfg.SiteRoot)
	if err != nil {
		return searchArchiveInventory{}, false, err
	}
	if !exists {
		return searchArchiveInventory{}, false, nil
	}
	info, err := os.Stat(filepath.Join(rt.cfg.SiteRoot, "catalog.json"))
	if err != nil {
		return searchArchiveInventory{}, false, fmt.Errorf("stat local archive catalog: %w", err)
	}
	inventory.CheckedAt = info.ModTime().UTC().Format(time.RFC3339)
	inventory.CatalogPresent = true
	seen := map[string]bool{}
	for _, raw := range catalog.Meetings {
		var entry struct {
			AudioPath string `json:"audioPath"`
		}
		if err := json.Unmarshal(raw, &entry); err != nil {
			return searchArchiveInventory{}, false, fmt.Errorf("parse local archive meeting: %w", err)
		}
		asset, valid := siteRelativeAsset(entry.AudioPath)
		name := filepath.Base(asset)
		if !valid || !strings.EqualFold(filepath.Ext(asset), ".opus") || seen[name] {
			inventory.Unsupported++
			continue
		}
		inventory.OpusNames = append(inventory.OpusNames, name)
		seen[name] = true
	}
	return inventory, true, nil
}

func inventoryFromStorageProbe(probe ncStorageProbe, accessControlled bool, checkedAt string) (searchArchiveInventory, bool) {
	facts := probe.archiveFor(accessControlled)
	if !facts.Probed {
		return searchArchiveInventory{}, false
	}
	return inventoryFromArchiveFacts(facts, checkedAt), true
}

func inventoryFromArchiveFacts(facts ncArchiveFacts, checkedAt string) searchArchiveInventory {
	inventory := searchArchiveInventory{CheckedAt: checkedAt, CatalogPresent: facts.CatalogProbed && facts.Catalog}
	seen := map[string]bool{}
	for _, entry := range facts.Entries {
		if entry.Name != "" && strings.EqualFold(filepath.Ext(entry.Name), ".opus") && !seen[entry.Name] {
			inventory.OpusNames = append(inventory.OpusNames, entry.Name)
			seen[entry.Name] = true
		} else {
			inventory.Unsupported++
		}
	}
	return inventory
}
