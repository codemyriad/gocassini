package cassini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// v1 static exports remain usable. v2 is a verified, complete content contract;
// a failed pull must be resumed before it can seed a harness.
func validateSeedManifest(root string, catalog meetingsCatalog) error {
	raw, err := os.ReadFile(filepath.Join(root, seedPackManifestName))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var manifest seedPackManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("invalid seed manifest: %w", err)
	}
	if manifest.Version != seedPackManifestVersion && manifest.Version != "cassini.seed.pack.v1" {
		return fmt.Errorf("unsupported seed manifest version %q", manifest.Version)
	}
	if manifest.Version == seedPackManifestVersion {
		if !manifest.Complete || len(manifest.Failed) > 0 || manifest.Selected != len(catalog.Meetings) {
			return fmt.Errorf("seed pack is incomplete; resume its pull")
		}
		if manifest.Annotations != "embedded" && manifest.Annotations != "current" {
			return fmt.Errorf("invalid seed annotation policy")
		}
	}
	if len(manifest.Meetings) != len(catalog.Meetings) || manifest.Totals.Meetings != len(catalog.Meetings) {
		return fmt.Errorf("seed manifest and catalog counts differ")
	}
	entries := map[string]string{}
	for _, raw := range catalog.Meetings {
		var entry meetingsCatalogEntry
		if err := json.Unmarshal(raw, &entry); err != nil {
			return err
		}
		rel, err := packRelativeAsset(entry.AudioPath)
		if err != nil {
			return err
		}
		entries[rel] = entry.ID
	}
	var total int64
	for _, entry := range manifest.Meetings {
		id, ok := entries[entry.Path]
		if !ok || id != entry.ID {
			return fmt.Errorf("seed manifest has duplicate or unknown entry %q", entry.Path)
		}
		delete(entries, entry.Path)
		file := filepath.Join(root, filepath.FromSlash(entry.Path))
		info, err := os.Stat(file)
		if err != nil {
			return err
		}
		if info.Size() != entry.Bytes {
			return fmt.Errorf("seed size mismatch: %s", entry.Path)
		}
		total += info.Size()
		if manifest.Version == seedPackManifestVersion || entry.SHA256 != "" {
			digest, err := annotateFileSHA256(file)
			if err != nil {
				return err
			}
			if digest != entry.SHA256 {
				return fmt.Errorf("seed SHA-256 mismatch: %s", entry.Path)
			}
		}
	}
	if total != manifest.Totals.Bytes {
		return fmt.Errorf("seed manifest byte total differs")
	}
	return nil
}
