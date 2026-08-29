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
	// RoomToken and RoomName mirror the recorder's manifest fields: which
	// conversation the recording came from, stamped by SetMeetingBundleRoom
	// from the job's Talk binding after promotion. Empty means "no known room",
	// which is normal for a non-Talk job.
	//
	// The bundle holds the raw token — it is the operator's own working
	// directory, the same exposure as the jobs table it was read from. Packing
	// derives the published id from it; the token itself is never published.
	RoomToken string `json:"room_token,omitempty"`
	RoomName  string `json:"room_name,omitempty"`
	// JobID and AttemptNumber mirror the recorder's manifest fields: which job
	// and attempt built this bundle (D-640), stamped by SetMeetingBundleRoom
	// alongside the room. `cassini pack` reads them when the seal does not pass
	// them explicitly, which is what carries the provenance through the
	// `cassini publish <bundle>` path.
	JobID            string            `json:"job_id,omitempty"`
	AttemptNumber    int               `json:"attempt_number,omitempty"`
	State            string            `json:"state,omitempty"`
	Stage            string            `json:"stage,omitempty"`
	Error            string            `json:"error,omitempty"`
	SourceKind       string            `json:"source_kind"`
	SourcePath       string            `json:"source_path"`
	ArtifactManifest string            `json:"artifact_manifest,omitempty"`
	Files            map[string]string `json:"files,omitempty"`
}

// SetMeetingBundleRoom stamps the meeting's title, the room it came from and
// the job that produced it, in one rewrite.
//
// One call rather than five, because each rewrite is a read-modify-rename of
// the same file: doing them separately would multiply the window in which a
// crash leaves the bundle stamped with a title and no room, which is exactly
// the state that looks correct and is not.
//
// Every field is optional and a blank one is left alone rather than written as
// an empty string. A rerun whose room lookup failed must not erase a room an
// earlier attempt did resolve. attemptNumber is 1-based, so a non-positive
// value is "unknown" and is skipped on the same principle.
func SetMeetingBundleRoom(bundleDir, title, roomToken, roomName, jobID string, attemptNumber int) error {
	fields := map[string]any{
		"title":      title,
		"room_token": roomToken,
		"room_name":  roomName,
		"job_id":     jobID,
	}
	if attemptNumber > 0 {
		fields["attempt_number"] = attemptNumber
	}
	return setMeetingBundleFields(bundleDir, fields)
}

// setMeetingBundleFields merges non-blank fields into a bundle's cassini.json.
// The rewrite goes through a generic map so fields the operator's
// MeetingBundleManifest copy does not know about survive, and lands via
// temp-file rename so a concurrent reader (a publish scan) never sees a torn
// manifest.
func setMeetingBundleFields(bundleDir string, fields map[string]any) error {
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
		// A blank string is "nothing to say", never "set this to empty" — see
		// the note on SetMeetingBundleRoom. Non-string values (the attempt
		// number) are already filtered by their caller, so they are written as
		// they arrive.
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
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
