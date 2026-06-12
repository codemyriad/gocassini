package cassini

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const siteManifestVersion = "cassini.site.v1"

type SiteBundle struct {
	RootDir      string
	ManifestPath string
}

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

type LoadedSiteBundle struct {
	RootDir   string
	Manifest  SiteBundleManifest
	FoundPath string
}

func PrepareSiteBundle(outDir string) (SiteBundle, error) {
	rootDir, err := filepath.Abs(outDir)
	if err != nil {
		return SiteBundle{}, fmt.Errorf("resolve output path: %w", err)
	}
	if err := ensureEmptyDir(rootDir); err != nil {
		return SiteBundle{}, err
	}
	bundle := SiteBundle{
		RootDir:      rootDir,
		ManifestPath: filepath.Join(rootDir, "cassini.json"),
	}
	if err := writeSiteManifest(bundle, SiteBundleManifest{
		State: bundleStatePreparing,
		Stage: "prepare",
	}); err != nil {
		return SiteBundle{}, err
	}
	return bundle, nil
}

func FinalizeSiteBundle(bundle SiteBundle, meta SiteBundleManifest) error {
	meta.State = bundleStateReady
	meta.Stage = "ready"
	meta.Error = ""

	catalogPath := filepath.Join(bundle.RootDir, "catalog.json")
	if _, err := os.Stat(catalogPath); err == nil {
		meta.CatalogPath = "catalog.json"
		meta.MeetingCount = readCatalogMeetingCount(catalogPath)
	}

	return writeSiteManifest(bundle, meta)
}

func LoadSiteBundle(path string) (LoadedSiteBundle, bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadedSiteBundle{}, false, nil
		}
		return LoadedSiteBundle{}, false, fmt.Errorf("stat site bundle path: %w", err)
	}

	root := path
	if !info.IsDir() {
		if filepath.Base(path) != "cassini.json" {
			return LoadedSiteBundle{}, false, nil
		}
		root = filepath.Dir(path)
	}

	manifestPath := filepath.Join(root, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return LoadedSiteBundle{}, false, nil
		}
		return LoadedSiteBundle{}, false, fmt.Errorf("read site bundle manifest: %w", err)
	}

	var manifest SiteBundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return LoadedSiteBundle{}, false, fmt.Errorf("parse site bundle manifest: %w", err)
	}
	if !strings.EqualFold(manifest.Kind, "site") {
		return LoadedSiteBundle{}, false, nil
	}

	return LoadedSiteBundle{
		RootDir:   root,
		Manifest:  manifest,
		FoundPath: manifestPath,
	}, true, nil
}

func readCatalogMeetingCount(path string) int {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	var catalog struct {
		Meetings []json.RawMessage `json:"meetings"`
	}
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return 0
	}
	return len(catalog.Meetings)
}

func UpdateSiteBundleStatus(bundle SiteBundle, state string, stage string, errText string) error {
	meta, err := readSiteManifest(bundle.ManifestPath)
	if err != nil {
		return err
	}
	meta.State = state
	meta.Stage = stage
	meta.Error = strings.TrimSpace(errText)
	return writeSiteManifest(bundle, meta)
}

func UpdateSiteBundleSource(bundle SiteBundle, sourcePath string, stage string) error {
	meta, err := readSiteManifest(bundle.ManifestPath)
	if err != nil {
		return err
	}
	meta.SourcePath = sourcePath
	if stage != "" {
		meta.Stage = stage
	}
	return writeSiteManifest(bundle, meta)
}

func readSiteManifest(path string) (SiteBundleManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SiteBundleManifest{}, fmt.Errorf("read site manifest: %w", err)
	}
	var manifest SiteBundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return SiteBundleManifest{}, fmt.Errorf("parse site manifest: %w", err)
	}
	return manifest, nil
}

func writeSiteManifest(bundle SiteBundle, meta SiteBundleManifest) error {
	meta.Kind = "site"
	meta.Version = siteManifestVersion
	if strings.TrimSpace(meta.CreatedAtUTC) == "" {
		meta.CreatedAtUTC = time.Now().UTC().Format(time.RFC3339Nano)
	}
	body, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal site manifest: %w", err)
	}
	body = append(body, '\n')
	if err := writeFileAtomic(bundle.ManifestPath, body, 0o644); err != nil {
		return fmt.Errorf("write site manifest: %w", err)
	}
	return nil
}
