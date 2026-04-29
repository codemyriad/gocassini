package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SiteBundleManifest struct {
	Kind         string `json:"kind"`
	Version      string `json:"version"`
	CreatedAtUTC string `json:"created_at_utc"`
	State        string `json:"state,omitempty"`
	Stage        string `json:"stage,omitempty"`
	Error        string `json:"error,omitempty"`
	SourcePath   string `json:"source_path"`
	CatalogPath  string `json:"catalog_path,omitempty"`
	MeetingCount int    `json:"meeting_count,omitempty"`
}

func LoadSiteBundleManifest(path string) (SiteBundleManifest, bool, error) {
	manifestPath := filepath.Join(path, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return SiteBundleManifest{}, false, nil
		}
		return SiteBundleManifest{}, false, fmt.Errorf("read site bundle manifest: %w", err)
	}
	var manifest SiteBundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return SiteBundleManifest{}, false, fmt.Errorf("parse site bundle manifest: %w", err)
	}
	if !strings.EqualFold(manifest.Kind, "site") {
		return SiteBundleManifest{}, false, nil
	}
	return manifest, true, nil
}
