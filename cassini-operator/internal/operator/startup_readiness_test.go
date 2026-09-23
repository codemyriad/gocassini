package operator

import (
	"context"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func storageMockRequestCount(mock *storageMock, method, suffix string) int {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	count := 0
	for _, request := range mock.reqs {
		if strings.HasPrefix(request, method+" ") && strings.HasSuffix(request, suffix) {
			count++
		}
	}
	return count
}

// The enabled edge already checked Nextcloud storage. Its health hook must use
// that verdict, while still refreshing media/Talk findings if a browser check
// finished inside the ordinary two-second coalescing window.
func TestEnabledHealthHookReusesPreflightAndBypassesRecentBrowserCheck(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))
	t.Setenv(envStorageMode, storageModeDefault)

	mock := &storageMock{apps: []string{}, serviceAccount: true}
	server := mock.server(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	t.Setenv(envAppSecret, "sekret")
	t.Setenv(envNextcloudURL, server.URL)
	rt.cfg.CassiniBin = writeFakeDoctorBin(t, `[{"id":"ffmpeg","status":"ok","summary":"ffmpeg ready"}]`)
	rt.recordingSetup.mu.Lock()
	rt.recordingSetup.checkedAt = time.Now()
	rt.recordingSetup.hostChecks = []readinessCheck{{ID: "host.old", State: "warn", Code: "stale_host"}}
	rt.recordingSetup.mu.Unlock()

	cfg := testExAppConfig(server.URL)
	cfg.afterPreflight = func() { rt.checkRecordingReadinessAfterPreflight(context.Background()) }
	cfg.enabledCallback(context.Background(), log.New(io.Discard, "", 0))(true)

	if got := storageMockRequestCount(mock, http.MethodGet, "/ocs/v2.php/cloud/apps"); got != 1 {
		t.Fatalf("storage preflight app-list requests = %d, want one; the health hook must not repeat preflight", got)
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !snap.OK {
		t.Fatalf("enabled preflight did not establish storage readiness: %+v", snap)
	}
	var fresh, old bool
	for _, check := range rt.readiness(context.Background()).Checks {
		switch check.ID {
		case "host.ffmpeg":
			fresh = check.State == "passed" && check.CheckedAt != ""
		case "host.old":
			old = true
		}
	}
	if !fresh || old {
		t.Fatalf("after-preflight host verdict was coalesced away: fresh=%t old=%t", fresh, old)
	}
}

// Restart preflight is asynchronous. Its hook must observe the completed
// storage verdict and exactly one storage probe, without an enabled edge.
func TestRestartHealthHookRunsAfterOnePreflight(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	if err := SaveStorageSettings(path, false, storageModeSourceEnv, true); err != nil {
		t.Fatal(err)
	}
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	cfg := testExAppConfig(mock.server(t).URL)
	type observation struct {
		storage statusRecordingsAccess
		probes  int
	}
	observed := make(chan observation, 1)
	cfg.afterPreflight = func() {
		observed <- observation{
			storage: ncAccessSubstrate.snapshot(publishSinkNextcloudFiles),
			probes:  storageMockRequestCount(mock, http.MethodGet, "/ocs/v2.php/cloud/apps"),
		}
	}
	cfg.preflightOnRestart(context.Background(), log.New(io.Discard, "", 0))
	select {
	case got := <-observed:
		if !got.storage.OK || got.storage.CheckedAt == "" {
			t.Fatalf("restart hook ran before storage verdict: %+v", got.storage)
		}
		if got.probes != 1 {
			t.Fatalf("restart preflight app-list requests = %d, want one", got.probes)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("restart preflight never reached the health hook")
	}
}
