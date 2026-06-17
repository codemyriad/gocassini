package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SiteBundleManifest struct {
	Kind                     string `json:"kind"`
	Version                  string `json:"version"`
	CreatedAtUTC             string `json:"created_at_utc"`
	State                    string `json:"state,omitempty"`
	Stage                    string `json:"stage,omitempty"`
	Error                    string `json:"error,omitempty"`
	SourcePath               string `json:"source_path"`
	CatalogPath              string `json:"catalog_path,omitempty"`
	MeetingCount             int    `json:"meeting_count,omitempty"`
	PublishedByJobID         string `json:"published_by_job_id,omitempty"`
	PublishedByAttemptNumber int    `json:"published_by_attempt_number,omitempty"`
	PublishedAtUTC           string `json:"published_at_utc,omitempty"`
}

type SiteBundleLineage struct {
	JobID          string
	AttemptNumber  int
	PublishedAtUTC string
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

func WriteSiteBundleLineage(path string, lineage SiteBundleLineage) error {
	manifest, ok, err := LoadSiteBundleManifest(path)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("site bundle manifest missing at %s", filepath.Join(path, "cassini.json"))
	}
	manifest.PublishedByJobID = strings.TrimSpace(lineage.JobID)
	manifest.PublishedByAttemptNumber = lineage.AttemptNumber
	manifest.PublishedAtUTC = strings.TrimSpace(lineage.PublishedAtUTC)

	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal site bundle manifest: %w", err)
	}
	body = append(body, '\n')
	if err := writeFileAtomic(filepath.Join(path, "cassini.json"), body, 0o644); err != nil {
		return fmt.Errorf("write site bundle manifest: %w", err)
	}
	return nil
}
