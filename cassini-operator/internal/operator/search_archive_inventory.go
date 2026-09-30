package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// searchArchiveInventory is an independently observed list of what the archive
// holds, as of CheckedAt.
//
// The index cannot supply this universe itself. A meeting published before the
// index existed has no meeting_index row, so a count of rows says how much was
// indexed and never how much was missed — which is the only question archive
// coverage is asked.
type searchArchiveInventory struct {
	OpusNames []string
	CheckedAt string
}

// searchInventory lists the archive, or reports that it could not.
//
// Nextcloud: ownerRecordingNames, a Depth:1 PROPFIND of the recordings root as
// the `cassini` account. Direct shares (#334) made reader visibility per-caller
// and assembles each caller's catalog from their own shares, so no caller's
// view is the archive. The owner's is, because it owns every file in it.
//
// Local site: the on-disk catalog, which for a standalone operator is the whole
// archive.
//
// A false `known` means no list could be taken. That is not an empty archive,
// and the caller must not read it as one.
func (rt *Runtime) searchInventory(ctx context.Context) (searchArchiveInventory, bool, error) {
	if rt.resolvedPublishSinkName() == publishSinkNextcloudFiles {
		cfg, err := LoadExAppConfig()
		if err != nil || !cfg.Active {
			return searchArchiveInventory{}, false, nil
		}
		client := &http.Client{Timeout: ncProvisionTimeout}
		names, err := cfg.ownerRecordingNames(ctx, client)
		if err != nil {
			return searchArchiveInventory{}, false, err
		}
		inventory := searchArchiveInventory{
			OpusNames: make([]string, 0, len(names)),
			// RFC3339, as every other checked_at in this response is.
			// nowUTCString carries nanoseconds, which is right for a sortable
			// internal stamp and wrong for a field the panel renders as an age.
			CheckedAt: time.Now().UTC().Format(time.RFC3339),
		}
		// Keyed by Nextcloud file id, which is what makes it a recovery lookup
		// elsewhere. Coverage joins on the name, as the index does.
		for _, name := range names {
			inventory.OpusNames = append(inventory.OpusNames, name)
		}
		return inventory, true, nil
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
	inventory := searchArchiveInventory{CheckedAt: info.ModTime().UTC().Format(time.RFC3339)}
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
			continue
		}
		inventory.OpusNames = append(inventory.OpusNames, name)
		seen[name] = true
	}
	return inventory, true, nil
}
