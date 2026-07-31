package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// siteManifestVersion mirrors the `cassini publish` CLI's site manifest
// version (cassini-go-recorder/internal/cassini/site_bundle.go), used only when
// seeding a manifest for a live site that never had one.
const siteManifestVersion = "cassini.site.v1"

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

// UpdateLiveSiteManifest records the publish lineage and meeting count on the
// live site's cassini.json, seeding the manifest when the site does not have
// one yet.
//
// Its predecessor (WriteSiteBundleLineage) required the manifest to already
// exist, which was true when publishing replaced the whole site with an
// attempt bundle that carried one. Upserting into a live site has no such
// guarantee: an ExApp site root is created by the first delivery, and a site
// seeded by an entrypoint has no manifest at all. A missing manifest is
// therefore seeded rather than treated as an error; a present one keeps every
// field this function does not own.
func UpdateLiveSiteManifest(path string, lineage SiteBundleLineage, meetingCount int) error {
	manifest, ok, err := LoadSiteBundleManifest(path)
	if err != nil {
		return err
	}
	if !ok {
		manifest = SiteBundleManifest{
			Kind:         "site",
			Version:      siteManifestVersion,
			CreatedAtUTC: strings.TrimSpace(lineage.PublishedAtUTC),
			CatalogPath:  "catalog.json",
		}
	}
	manifest.State = bundleStateReady
	manifest.Stage = "ready"
	manifest.Error = ""
	manifest.MeetingCount = meetingCount
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
