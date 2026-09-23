package operator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectStorageHasOnlyAccountSetup(t *testing.T) {
	rt := &Runtime{cfg: Config{PublishSink: publishSinkNextcloudFiles}}
	cfg := ExAppConfig{PublishSink: publishSinkNextcloudFiles}
	status := cfg.storageStatus(rt)
	if len(status.Setup) != 1 || status.Setup[0].Action != "create_user" {
		t.Fatalf("setup = %+v", status.Setup)
	}
}

func TestDirectStorageOffersAccountCreationAndAcceptsFirstRun(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	ncAccessSubstrate.reset()
	t.Cleanup(func() { ncAccessSubstrate.reset() })
	ncAccessSubstrate.setProbe(ncStorageProbe{})
	cfg := ExAppConfig{PublishSink: publishSinkNextcloudFiles, sharePaths: &recordingSharePathCache{}}
	status := cfg.storageStatus(rt)
	if status.ServiceAccount.Exists || len(status.Setup) != 1 {
		t.Fatalf("unexpected direct-share setup status: %+v", status)
	}
	foundAccount := false
	for _, step := range status.Setup {
		if step.Action == "create_user" {
			foundAccount = true
		}
	}
	if !foundAccount {
		t.Fatalf("missing account creation step: %+v", status.Setup)
	}
	ack := httptest.NewRecorder()
	cfg.storageHandler(rt).ServeHTTP(ack, httptest.NewRequest(http.MethodPost, "/storage", strings.NewReader(`{"action":"acknowledge_first_run"}`)))
	if ack.Code != http.StatusOK {
		t.Fatalf("first-run acknowledgement returned %d: %s", ack.Code, ack.Body.String())
	}
	rec := httptest.NewRecorder()
	cfg.storageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/storage", strings.NewReader(`{"action":"preview"}`)))
	if rec.Code != http.StatusGone {
		t.Fatalf("retired mode action returned %d, want 410", rec.Code)
	}
}

func resetDirectSubstrate(t *testing.T) {
	t.Helper()
	ncAccessSubstrate.reset()
	ncAccessSubstrate.markApplicable()
	t.Cleanup(ncAccessSubstrate.reset)
}

func TestDirectPreflightKeepsLastUsableResultUntilProbeFinishes(t *testing.T) {
	resetDirectSubstrate(t)
	ncAccessSubstrate.beginRun()
	ncAccessSubstrate.succeed()
	ncAccessSubstrate.beginRun()
	if !ncAccessSubstrate.usable() {
		t.Fatal("a concurrent preflight hid the last successful result")
	}
	ncAccessSubstrate.unavailable("sharing_api", nil)
	if ncAccessSubstrate.usable() {
		t.Fatal("a completed failed probe was ignored")
	}
	ncAccessSubstrate.beginRun()
	ncAccessSubstrate.succeed()
	if !ncAccessSubstrate.usable() {
		t.Fatal("a successful recheck did not recover availability")
	}
}
