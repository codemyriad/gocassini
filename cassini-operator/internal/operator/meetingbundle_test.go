package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeBundleManifestFixture writes a ready bundle manifest carrying a field
// the operator's struct does not know about, so every stamp test also checks
// that the recorder-owned schema survives the rewrite.
func writeBundleManifestFixture(t *testing.T) string {
	t.Helper()
	bundleDir := t.TempDir()
	original := `{"kind":"meeting","version":"cassini.meeting.v1","state":"ready","future_field":"keep-me"}`
	if err := os.WriteFile(filepath.Join(bundleDir, "cassini.json"), []byte(original), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	return bundleDir
}

func readBundleManifestFixture(t *testing.T, bundleDir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(bundleDir, "cassini.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("parse updated manifest: %v", err)
	}
	return updated
}

func TestSetMeetingBundleRoomStampsAndPreservesUnknownFields(t *testing.T) {
	bundleDir := writeBundleManifestFixture(t)

	if err := SetMeetingBundleRoom(bundleDir, "Daily Meeting", "a7bc3k9x", "Daily Meeting", "01K3Q7W8ZC9F0MJXQ2NB8V4RTD", 2); err != nil {
		t.Fatalf("SetMeetingBundleRoom() error = %v", err)
	}

	updated := readBundleManifestFixture(t, bundleDir)
	for key, want := range map[string]string{
		"title":      "Daily Meeting",
		"room_token": "a7bc3k9x",
		"room_name":  "Daily Meeting",
		"job_id":     "01K3Q7W8ZC9F0MJXQ2NB8V4RTD",
		// The stamp must not drop fields the operator's manifest struct does
		// not know about — the recorder owns the schema.
		"future_field": "keep-me",
		"state":        "ready",
	} {
		if updated[key] != want {
			t.Errorf("%s = %v, want %q", key, updated[key], want)
		}
	}

	manifest, ok, err := LoadMeetingBundleManifest(bundleDir)
	if err != nil || !ok {
		t.Fatalf("LoadMeetingBundleManifest() = ok=%t err=%v", ok, err)
	}
	if manifest.Title != "Daily Meeting" {
		t.Errorf("manifest.Title = %q, want %q", manifest.Title, "Daily Meeting")
	}
	if manifest.RoomToken != "a7bc3k9x" || manifest.RoomName != "Daily Meeting" {
		t.Errorf("manifest room = %q/%q, want %q/%q", manifest.RoomToken, manifest.RoomName, "a7bc3k9x", "Daily Meeting")
	}
	// The job and attempt ride on the same stamp (D-640): they are the only
	// lineage a `.opus` published straight from this bundle would carry.
	if manifest.JobID != "01K3Q7W8ZC9F0MJXQ2NB8V4RTD" || manifest.AttemptNumber != 2 {
		t.Errorf("manifest provenance = %q/%d, want the job id and attempt 2", manifest.JobID, manifest.AttemptNumber)
	}
}

func TestSetMeetingBundleRoomLeavesBlankFieldsAlone(t *testing.T) {
	bundleDir := writeBundleManifestFixture(t)

	// A Talk job whose room-name lookup failed still knows its token, and a
	// non-Talk job knows neither. Neither may write an empty string: a rerun
	// that resolved less than an earlier attempt would otherwise erase what the
	// earlier one found.
	if err := SetMeetingBundleRoom(bundleDir, "", "a7bc3k9x", "", "", 0); err != nil {
		t.Fatalf("SetMeetingBundleRoom() error = %v", err)
	}

	updated := readBundleManifestFixture(t, bundleDir)
	if updated["room_token"] != "a7bc3k9x" {
		t.Errorf("room_token = %v, want %q", updated["room_token"], "a7bc3k9x")
	}
	// Same rule for the provenance: attempts are 1-based, so a zero is
	// "unknown" and must not be written as an attempt that cannot exist.
	for _, key := range []string{"title", "room_name", "job_id", "attempt_number"} {
		if _, ok := updated[key]; ok {
			t.Errorf("%s = %v, want left unwritten", key, updated[key])
		}
	}
}

func TestSetMeetingBundleRoomErrorsWithoutManifest(t *testing.T) {
	if err := SetMeetingBundleRoom(t.TempDir(), "Daily Meeting", "", "", "", 0); err == nil {
		t.Error("SetMeetingBundleRoom() on empty dir error = nil, want error")
	}
}
