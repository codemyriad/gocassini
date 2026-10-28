package operator

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// holdFirstInventoryRead makes the first `cassini models list` wait until
// release is called, so a test can change Settings while the retired-model
// migration is between reading the settings and saving them. Later listings
// return at once.
func holdFirstInventoryRead(t *testing.T, rt *Runtime) (waitListing func(), release func()) {
	t.Helper()
	root := rt.cfg.ModelCacheRoot
	script := `#!/bin/sh
set -eu
r="$CASSINI_CACHE_ROOT"
if [ ! -e "$r/listing" ]; then
  : > "$r/listing"
  while [ ! -e "$r/go" ]; do sleep 0.02; done
fi
cat "$r/inventory.json"
`
	if err := os.WriteFile(rt.cfg.CassiniBin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	waitListing = func() {
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(filepath.Join(root, "listing")); err == nil {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("the migration never read the inventory")
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	release = func() {
		if err := os.WriteFile(filepath.Join(root, "go"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(release)
	return waitListing, release
}

type migrationResult struct {
	migrated bool
	err      error
}

func startRetiredPinMigration(t *testing.T, rt *Runtime) <-chan migrationResult {
	t.Helper()
	cpuOnly(t)
	setUltraInt8(t, rt, false, false)
	pinned := STTSettings{TranscriptionEnabled: true, ActiveModel: v3Int8, ActiveRevision: retiredV3Rev, Quality: sttQualityBalanced, DeviceOverride: deviceCPU, Source: sttSourceUser}
	if err := Save(rt.settingsPath, pinned); err != nil {
		t.Fatal(err)
	}
	rt.setSettings(pinned)
	done := make(chan migrationResult, 1)
	go func() {
		migrated, err := rt.migrateRetiredSpeechModel(rt.ctx)
		done <- migrationResult{migrated, err}
	}()
	return done
}

// A Settings change saved while the migration reads the inventory and queues
// the install is kept: the migration moves only the pin, on the settings as
// they are when it saves, instead of writing back the copy it read first.
func TestRetiredPinMigrationKeepsAConcurrentSettingsChange(t *testing.T) {
	rt := modelTestRuntime(t)
	waitListing, release := holdFirstInventoryRead(t, rt)
	done := startRetiredPinMigration(t, rt)
	waitListing()

	rec := httptest.NewRecorder()
	rt.handlePutSettings(rec, httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"transcription_enabled":false,"transcription_terms":["Cassini"]}`)))
	if rec.Code != 200 {
		t.Fatalf("PUT /settings: %d %s", rec.Code, rec.Body.String())
	}
	release()
	if r := <-done; !r.migrated || r.err != nil {
		t.Fatalf("migrated=%v err=%v", r.migrated, r.err)
	}

	onDisk, err := LoadOrInitSettings(rt.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]STTSettings{"memory": rt.currentSettings(), "disk": onDisk} {
		if s.TranscriptionEnabled || !slices.Equal(s.TranscriptionTerms, []string{"Cassini"}) {
			t.Fatalf("%s: the migration overwrote the concurrent change: %+v", name, s)
		}
		if s.ActiveModel != modelParakeetUltraInt8 || s.ActiveRevision != ultraInt8Rev {
			t.Fatalf("%s: pin not moved: %+v", name, s)
		}
	}
}

// When Settings selected another model meanwhile, the migration leaves that
// choice alone and reports it, so the retried check looks again.
func TestRetiredPinMigrationLeavesANewSelectionAlone(t *testing.T) {
	rt := modelTestRuntime(t)
	waitListing, release := holdFirstInventoryRead(t, rt)
	done := startRetiredPinMigration(t, rt)
	waitListing()

	rec := httptest.NewRecorder()
	rt.handlePutSettings(rec, httptest.NewRequest("PUT", "/settings", strings.NewReader(`{"quality":"fast","transcription_enabled":true,"active_model":"`+modelParakeet110M+`","active_revision":"`+strings.Repeat("a", 64)+`","device_override":"cpu"}`)))
	if rec.Code != 200 {
		t.Fatalf("PUT /settings: %d %s", rec.Code, rec.Body.String())
	}
	release()
	if r := <-done; r.migrated || !errors.Is(r.err, errSettingsChangedDuringMigration) {
		t.Fatalf("migrated=%v err=%v, want the change reported", r.migrated, r.err)
	}
	onDisk, err := LoadOrInitSettings(rt.settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]STTSettings{"memory": rt.currentSettings(), "disk": onDisk} {
		if s.ActiveModel != modelParakeet110M || s.Quality != sttQualityFast {
			t.Fatalf("%s: the migration replaced the new selection: %+v", name, s)
		}
	}
	// The retry sees a pin that is not retired and does nothing.
	if migrated, err := rt.migrateRetiredSpeechModel(rt.ctx); migrated || err != nil {
		t.Fatalf("retry migrated=%v err=%v", migrated, err)
	}
}
