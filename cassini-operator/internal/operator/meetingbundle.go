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
	// meeting name. The operator stamps it (SetMeetingBundleRoom) from the
	// Talk room name after promotion.
	Title string `json:"title,omitempty"`
	// RoomID and RoomName mirror the recorder's manifest fields: which
	// conversation the recording came from, stamped by SetMeetingBundleRoom
	// from the job's Talk binding after promotion. Empty means "no known room",
	// which is normal for a non-Talk job.
	RoomID           string            `json:"room_id,omitempty"`
	RoomName         string            `json:"room_name,omitempty"`
	State            string            `json:"state,omitempty"`
	Stage            string            `json:"stage,omitempty"`
	Error            string            `json:"error,omitempty"`
	SourceKind       string            `json:"source_kind"`
	SourcePath       string            `json:"source_path"`
	ArtifactManifest string            `json:"artifact_manifest,omitempty"`
	Files            map[string]string `json:"files,omitempty"`
}

// SetMeetingBundleRoom stamps the meeting's title and the room it came from in
// one rewrite.
//
// One call rather than three, because each rewrite is a read-modify-rename of
// the same file: doing them separately would triple the window in which a
// crash leaves the bundle stamped with a title and no room, which is exactly
// the state that looks correct and is not.
//
// Every field is optional and a blank one is left alone rather than written as
// an empty string. A rerun whose room lookup failed must not erase a room an
// earlier attempt did resolve.
func SetMeetingBundleRoom(bundleDir, title, roomID, roomName string) error {
	return setMeetingBundleFields(bundleDir, map[string]string{
		"title":     title,
		"room_id":   roomID,
		"room_name": roomName,
	})
}

// setMeetingBundleFields merges non-blank fields into a bundle's cassini.json.
// The rewrite goes through a generic map so fields the operator's
// MeetingBundleManifest copy does not know about survive, and lands via
// temp-file rename so a concurrent reader (a publish scan) never sees a torn
// manifest.
func setMeetingBundleFields(bundleDir string, fields map[string]string) error {
	manifestPath := filepath.Join(bundleDir, "cassini.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("read meeting bundle manifest: %w", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse meeting bundle manifest: %w", err)
	}
	wrote := false
	for key, value := range fields {
		if strings.TrimSpace(value) == "" {
			continue
		}
		manifest[key] = value
		wrote = true
	}
	if !wrote {
		return nil
	}
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
