package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeStorage(t *testing.T, rec *httptest.ResponseRecorder) storageStatusResponse {
	t.Helper()
	var body storageStatusResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode /storage body: %v (%s)", err, rec.Body.String())
	}
	return body
}

func optionFor(t *testing.T, body storageStatusResponse, mode string) storageModeOption {
	t.Helper()
	for _, option := range body.Modes {
		if option.Mode == mode {
			return option
		}
	}
	t.Fatalf("/storage did not report the %q mode: %+v", mode, body.Modes)
	return storageModeOption{}
}

func getStorage(t *testing.T, cfg ExAppConfig, rt *Runtime) storageStatusResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	cfg.storageHandler(rt).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/storage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /storage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	return decodeStorage(t, rec)
}

// The Setup tab's whole job: which mode is on, and what the other one would
// need. A blocker with no command in it is not usable, because the first pass
// scaffolds nothing.
func TestStorageReportsTheActiveModeAndWhatTheOtherOneNeeds(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)
	ncAccessSubstrate.setMode(storageModeDefault, storageModeSourceConfigured)
	ncAccessSubstrate.setProbe(ncStorageProbe{
		AdminUser:         "admin",
		ServiceAccount:    true,
		FolderProbed:      true,
		DefaultRootProbed: true,
		Prereqs: []ncPrerequisiteStatus{
			{Name: ncAppGroupFolders, State: ncPrerequisiteMissing},
			{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
		},
	})
	ncAccessSubstrate.succeed()

	body := getStorage(t, testExAppConfig("http://nextcloud.invalid"), rt)
	if body.Mode != storageModeDefault {
		t.Fatalf("mode = %q, want %q", body.Mode, storageModeDefault)
	}
	if len(body.Modes) != 2 {
		t.Fatalf("modes = %+v, want exactly the two models", body.Modes)
	}

	active := optionFor(t, body, storageModeDefault)
	if !active.Active || !active.Available {
		t.Fatalf("the default mode reported active=%t available=%t, want both true", active.Active, active.Available)
	}
	if active.Summary == "" || active.Consequence == "" {
		t.Fatal("a mode must carry both what it means and what switching to it would do")
	}

	other := optionFor(t, body, storageModeAccessControlled)
	if other.Active || other.Available {
		t.Fatalf("access control reported active=%t available=%t on an instance with neither app", other.Active, other.Available)
	}
	if !strings.HasPrefix(other.Step, "app_missing:") {
		t.Fatalf("step = %q, want an app_missing step", other.Step)
	}
	instructions := strings.Join(other.Instructions, "\n")
	if !strings.Contains(instructions, "occ app:install "+ncAppGroupFolders) {
		t.Fatalf("instructions do not say how to install the missing app:\n%s", instructions)
	}
	// The consequence is the confirmation prompt's body, so it has to name the
	// thing an administrator would be surprised by.
	if !strings.Contains(other.Consequence, "readable by every account") {
		t.Fatalf("the opt-in consequence does not say that migrated recordings stay public: %q", other.Consequence)
	}
	if !strings.Contains(optionFor(t, body, storageModeDefault).Consequence, "dropped") {
		t.Fatal("the opt-out consequence does not say that access rules are dropped")
	}
}

// Before any preflight the mode is not "default", it is unknown — and neither
// mode may be offered as available on an instance nothing has looked at.
func TestStorageReportsAnUncheckedInstanceAsUnknownRatherThanDefault(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	resetStorageMode(t)

	body := getStorage(t, testExAppConfig("http://nextcloud.invalid"), rt)
	if body.Mode != "" {
		t.Fatalf("mode = %q, want \"\" — an unresolved mode is not the default one", body.Mode)
	}
	for _, option := range body.Modes {
		if option.Available {
			t.Errorf("%q was offered as available before anything checked the instance", option.Mode)
		}
		if option.Step != "unknown" {
			t.Errorf("%q step = %q, want \"unknown\"", option.Mode, option.Step)
		}
	}
}

// A PUT that asks for the mode already in force must not move an archive that
// is already where it belongs — a double-click, or a retry of a request whose
// response was lost, has to be a no-op.
func TestPutStorageIsANoOpForTheModeAlreadyInForce(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)
	ncAccessSubstrate.succeed()

	// A Nextcloud URL that cannot be reached: if the handler tried to transition
	// it would fail loudly rather than answering 200.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/storage", strings.NewReader(`{"access_control_enabled":false}`))
	testExAppConfig("http://127.0.0.1:1").storageHandler(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /storage for the active mode = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if body := decodeStorage(t, rec); body.Transition != nil {
		t.Fatalf("a no-op PUT reported a transition: %+v", body.Transition)
	}
}

func TestPutStorageRequiresTheFlag(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/storage", strings.NewReader(`{}`))
	testExAppConfig("http://nextcloud.invalid").storageHandler(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT /storage with no flag = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "access_control_enabled") {
		t.Fatalf("the error does not name the missing field: %s", rec.Body.String())
	}
}

// A transition the instance is not set up for is a 409, not a 500: nothing is
// broken and nothing was touched, so the UI can show the blocker and put the
// button back where it was.
func TestPutStorageAnswers409WhenTheTargetModeIsNotReady(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock := &storageMock{apps: []string{}, serviceAccount: true}
	cfg := testExAppConfig(mock.server(t).URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/storage", strings.NewReader(`{"access_control_enabled":true}`))
	cfg.storageHandler(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("PUT /storage into an unready mode = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), ncAppGroupFolders) {
		t.Fatalf("the error does not name what is missing: %s", rec.Body.String())
	}
	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("a refused PUT changed the recorded mode")
	}
}

func TestStorageRejectsOtherMethods(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	resetStorageMode(t)

	rec := httptest.NewRecorder()
	testExAppConfig("http://nextcloud.invalid").storageHandler(rt).
		ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/storage", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /storage = %d, want 405", rec.Code)
	}
	for _, verb := range []string{http.MethodGet, http.MethodPost, http.MethodPut} {
		if got := rec.Header().Get("Allow"); !strings.Contains(got, verb) {
			t.Fatalf("Allow = %q, want it to list %s", got, verb)
		}
	}
}

// The route has to be reachable through the same base-path mount as the rest of
// the operator API, or the Setup tab 404s in every deployment that sets one.
func TestStorageIsRoutedUnderTheOperatorBasePath(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	resetStorageMode(t)
	rt.cfg.BasePath = "/operator"

	handler := newHTTPHandler(discardLogger(), rt, ExAppConfig{})
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/operator/storage", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /operator/storage = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if body := decodeStorage(t, rec); len(body.Modes) != 2 {
		t.Fatalf("modes = %+v, want both models", body.Modes)
	}
}

// --- Recovering from a migration that did not finish -----------------------------

// THE QA STATE, and the reason it was unreachable.
//
// A switch that stopped after the flip leaves the recorded mode already equal to
// what the button asks for. The first pass short-circuited there — "already
// there, nothing to do" — which made the one action that would repair the
// instance impossible to reach from the UI. The short-circuit now applies only
// to a SETTLED instance; an unsettled one runs the cleanup.
func TestPutStorageRepairsAnUnfinishedMigrationInsteadOfNoOpping(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, true)
	ncStorage.set(true, storageModeSourceConfigured, false)
	if err := SaveStorageSettings(settings, true, storageModeSourceUser, false); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncACLRecordingsRoot+"/meetings/m1.opus", "audio-1")
	// The leftover the tidy-up never removed.
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")
	cfg := testExAppConfig(mock.server(t).URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/storage", strings.NewReader(`{"access_control_enabled":true}`))
	cfg.storageHandler(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /storage for the mode already in force = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if mock.has(ncDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Fatal("the leftover copy was left behind; the repair never ran")
	}
	if !mock.has(ncACLRecordingsRoot + "/meetings/m1.opus") {
		t.Fatal("the ACTIVE archive was cleared")
	}
	if !ncStorage.migrationClean() {
		t.Fatal("the instance is still marked unsettled after a successful repair")
	}
}

// The explicit button. `POST /storage {"action":"finish_migration"}` is the same
// repair, reachable without asking for a mode at all.
func TestPostStorageFinishMigrationClearsTheStaleRoot(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)
	ncStorage.set(false, storageModeSourceConfigured, false)
	if err := SaveStorageSettings(settings, false, storageModeSourceUser, false); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")
	mock.addFile(ncACLRecordingsRoot+"/meetings/m1.opus", "audio-1")
	cfg := testExAppConfig(mock.server(t).URL)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/storage", strings.NewReader(`{"action":"finish_migration"}`))
	cfg.storageHandler(rt).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("POST finish_migration = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if mock.has(ncACLRecordingsRoot + "/meetings/m1.opus") {
		t.Fatal("the stale Team-folder copy was left behind")
	}
	body := decodeStorage(t, rec)
	if !body.MigrationClean {
		t.Fatalf("/storage still reports an unfinished migration: %+v", body)
	}
}

// An unfinished migration is reported, with the root that holds the leftovers
// named — that is what the Setup tab renders a button beside.
func TestStorageReportsAnUnfinishedMigration(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)
	ncStorage.set(false, storageModeSourceConfigured, false)
	ncAccessSubstrate.setProbe(ncStorageProbe{ServiceAccount: true, FolderProbed: true, DefaultRootProbed: true})
	ncAccessSubstrate.succeed()

	body := getStorage(t, testExAppConfig("http://nextcloud.invalid"), rt)
	if body.MigrationClean {
		t.Fatal("an unfinished migration was reported as settled")
	}
	if body.PendingCleanup != ncACLRecordingsRoot {
		t.Fatalf("pending_cleanup = %q, want the root the mode does not name (%q)", body.PendingCleanup, ncACLRecordingsRoot)
	}
}

// A settled instance whose OTHER root still holds recordings is not an error —
// publishing and reading both work — but it is the thing an administrator most
// needs told, because the symptom is "my recordings are gone".
func TestStorageReportsAStrandedArchiveWithoutCallingItAFailure(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)
	ncAccessSubstrate.setProbe(ncStorageProbe{
		ServiceAccount:     true,
		FolderProbed:       true,
		DefaultRootProbed:  true,
		FolderPresent:      true,
		FolderMounted:      true,
		ACLArchiveMeetings: 4,
	})
	ncAccessSubstrate.succeed()

	body := getStorage(t, testExAppConfig("http://nextcloud.invalid"), rt)
	if !body.OK {
		t.Fatalf("a stranded archive was reported as a health failure: %+v", body)
	}
	if body.StrandedRecordings != 4 || body.StrandedRoot != ncACLRecordingsRoot {
		t.Fatalf("stranded = %d at %q, want 4 at %q", body.StrandedRecordings, body.StrandedRoot, ncACLRecordingsRoot)
	}
}
