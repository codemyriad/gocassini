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

	if err := SetMeetingBundleRoom(bundleDir, "Daily Meeting", "a7bc3k9x", "Daily Meeting"); err != nil {
		t.Fatalf("SetMeetingBundleRoom() error = %v", err)
	}

	updated := readBundleManifestFixture(t, bundleDir)
	for key, want := range map[string]string{
		"title":     "Daily Meeting",
		"room_id":   "a7bc3k9x",
		"room_name": "Daily Meeting",
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
	if manifest.RoomID != "a7bc3k9x" || manifest.RoomName != "Daily Meeting" {
		t.Errorf("manifest room = %q/%q, want %q/%q", manifest.RoomID, manifest.RoomName, "a7bc3k9x", "Daily Meeting")
	}
}

func TestSetMeetingBundleRoomLeavesBlankFieldsAlone(t *testing.T) {
	bundleDir := writeBundleManifestFixture(t)

	// A Talk job whose room-name lookup failed still knows its token, and a
	// non-Talk job knows neither. Neither may write an empty string: a rerun
	// that resolved less than an earlier attempt would otherwise erase what the
	// earlier one found.
	if err := SetMeetingBundleRoom(bundleDir, "", "a7bc3k9x", ""); err != nil {
		t.Fatalf("SetMeetingBundleRoom() error = %v", err)
	}

	updated := readBundleManifestFixture(t, bundleDir)
	if updated["room_id"] != "a7bc3k9x" {
		t.Errorf("room_id = %v, want %q", updated["room_id"], "a7bc3k9x")
	}
	for _, key := range []string{"title", "room_name"} {
		if _, ok := updated[key]; ok {
			t.Errorf("%s = %v, want left unwritten", key, updated[key])
		}
	}
}

func TestSetMeetingBundleRoomErrorsWithoutManifest(t *testing.T) {
	if err := SetMeetingBundleRoom(t.TempDir(), "Daily Meeting", "", ""); err == nil {
		t.Error("SetMeetingBundleRoom() on empty dir error = nil, want error")
	}
}
