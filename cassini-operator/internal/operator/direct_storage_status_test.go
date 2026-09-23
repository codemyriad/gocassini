package operator

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDirectStorageStatusHasOneAudienceModel(t *testing.T) {
	rt := &Runtime{cfg: Config{PublishSink: publishSinkNextcloudFiles}}
	cfg := ExAppConfig{PublishSink: publishSinkNextcloudFiles, sharePaths: &recordingSharePathCache{}}
	status := cfg.storageStatus(rt, nil)
	if status.Mode != "default" || len(status.Modes) != 1 || status.Modes[0].Label != "Room participants" {
		t.Fatalf("storage status still offers multiple models: %+v", status)
	}
	if status.Modes[0].Root != ncDefaultRecordingsRoot {
		t.Fatalf("wrong private archive: %+v", status.Modes[0])
	}
}

func TestDirectStorageOffersAccountCreationAndAcceptsFirstRun(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)
	ncAccessSubstrate.setProbe(ncStorageProbe{DefaultRootProbed: true})
	cfg := ExAppConfig{PublishSink: publishSinkNextcloudFiles, sharePaths: &recordingSharePathCache{}}
	status := cfg.storageStatus(rt, nil)
	if status.ServiceAccount.Exists || len(status.Modes) != 1 {
		t.Fatalf("unexpected direct-share setup status: %+v", status)
	}
	foundAccount := false
	for _, step := range status.Modes[0].Setup {
		if step.Action == setupActionCreateUser {
			foundAccount = true
		}
	}
	if !foundAccount {
		t.Fatalf("missing account creation step: %+v", status.Modes[0].Setup)
	}
	postStorageAction(t, cfg, rt, `{"action":"acknowledge_first_run"}`)
	rec := httptest.NewRecorder()
	cfg.storageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/storage", strings.NewReader(`{"action":"preview"}`)))
	if rec.Code != http.StatusGone {
		t.Fatalf("retired mode action returned %d, want 410", rec.Code)
	}
}
