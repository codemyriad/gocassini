package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type MeetingBundleManifest struct {
	Kind         string `json:"kind"`
	Version      string `json:"version"`
	CreatedAtUTC string `json:"created_at_utc"`
	// Title mirrors the recorder's manifest field: an optional human-readable
	// meeting name. The operator stamps it (SetMeetingBundleTitle) from the
	// Talk room name after promotion.
	Title            string            `json:"title,omitempty"`
	State            string            `json:"state,omitempty"`
	Stage            string            `json:"stage,omitempty"`
	Error            string            `json:"error,omitempty"`
	SourceKind       string            `json:"source_kind"`
	SourcePath       string            `json:"source_path"`
	ArtifactManifest string            `json:"artifact_manifest,omitempty"`
	Files            map[string]string `json:"files,omitempty"`
}

// SetMeetingBundleTitle stamps a human-readable meeting title into a bundle's
// cassini.json. The rewrite goes through a generic map so fields the
// operator's MeetingBundleManifest copy does not know about survive, and
// lands via temp-file rename so a concurrent reader (a publish scan) never
// sees a torn manifest.
func SetMeetingBundleTitle(bundleDir, title string) error {
	manifestPath := filepath.Join(bundleDir, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read meeting bundle manifest: %w", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse meeting bundle manifest: %w", err)
	}
	manifest["title"] = title
	updated, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("encode meeting bundle manifest: %w", err)
	}
	if err := writeFileAtomic(manifestPath, append(updated, '\n'), 0o644); err != nil {
		return fmt.Errorf("write meeting bundle manifest: %w", err)
	}
	return nil
}

func LoadMeetingBundleManifest(path string) (MeetingBundleManifest, bool, error) {
	manifestPath := filepath.Join(path, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		if os.IsNotExist(err) {
			return MeetingBundleManifest{}, false, nil
		}
		return MeetingBundleManifest{}, false, fmt.Errorf("read meeting bundle manifest: %w", err)
	}
	var manifest MeetingBundleManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return MeetingBundleManifest{}, false, fmt.Errorf("parse meeting bundle manifest: %w", err)
	}
	if !strings.EqualFold(manifest.Kind, "meeting") {
		return MeetingBundleManifest{}, false, nil
	}
	return manifest, true, nil
}
