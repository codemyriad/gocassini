package operator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSetMeetingBundleTitleStampsAndPreservesUnknownFields(t *testing.T) {
	bundleDir := t.TempDir()
	// Include a field the operator's manifest struct does not know about; the
	// stamp must not drop it (the recorder owns the schema).
	original := `{"kind":"meeting","version":"cassini.meeting.v1","state":"ready","future_field":"keep-me"}`
	if err := os.WriteFile(filepath.Join(bundleDir, "cassini.json"), []byte(original), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	if err := SetMeetingBundleTitle(bundleDir, "Daily Meeting"); err != nil {
		t.Fatalf("SetMeetingBundleTitle() error = %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(bundleDir, "cassini.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("parse updated manifest: %v", err)
	}
	if updated["title"] != "Daily Meeting" {
		t.Errorf("title = %v, want %q", updated["title"], "Daily Meeting")
	}
	if updated["future_field"] != "keep-me" {
		t.Errorf("future_field = %v, want preserved", updated["future_field"])
	}
	if updated["state"] != "ready" {
		t.Errorf("state = %v, want preserved", updated["state"])
	}

	manifest, ok, err := LoadMeetingBundleManifest(bundleDir)
	if err != nil || !ok {
		t.Fatalf("LoadMeetingBundleManifest() = ok=%t err=%v", ok, err)
	}
	if manifest.Title != "Daily Meeting" {
		t.Errorf("manifest.Title = %q, want %q", manifest.Title, "Daily Meeting")
	}
}

func TestSetMeetingBundleTitleErrorsWithoutManifest(t *testing.T) {
	if err := SetMeetingBundleTitle(t.TempDir(), "Daily Meeting"); err == nil {
		t.Error("SetMeetingBundleTitle() on empty dir error = nil, want error")
	}
}
