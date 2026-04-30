package operator

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type MeetingBundleManifest struct {
	Kind             string            `json:"kind"`
	Version          string            `json:"version"`
	CreatedAtUTC     string            `json:"created_at_utc"`
	State            string            `json:"state,omitempty"`
	Stage            string            `json:"stage,omitempty"`
	Error            string            `json:"error,omitempty"`
	SourceKind       string            `json:"source_kind"`
	SourcePath       string            `json:"source_path"`
	ArtifactManifest string            `json:"artifact_manifest,omitempty"`
	Files            map[string]string `json:"files,omitempty"`
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
