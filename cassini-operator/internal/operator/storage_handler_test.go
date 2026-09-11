package operator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
		ServiceAccount:    true,
		FolderProbed:      true,
		DefaultRootProbed: true,
		FolderPresent:     true,
		FolderMounted:     true,
		ACLArchive:        ncArchiveFacts{Root: ncACLRecordingsRoot, Probed: true, Present: true, Entries: []davEntry{{Name: "a.opus"}, {Name: "b.opus"}, {Name: "c.opus"}, {Name: "d.opus"}}},
		DefaultArchive:    ncArchiveFacts{Root: ncDefaultRecordingsRoot, Probed: true},
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

// --- The first run (D-755) --------------------------------------------------

// postStorageAction is the POST every action test makes.
func postStorageAction(t *testing.T, cfg ExAppConfig, rt *Runtime, body string) storageStatusResponse {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/storage", strings.NewReader(body))
	cfg.storageHandler(rt).ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /storage %s = %d, want 200 (%s)", body, rec.Code, rec.Body.String())
	}
	return decodeStorage(t, rec)
}

// A fresh install owes the dialog until somebody answers it, and then never
// again — on any browser, to any administrator. That is the whole reason the
// flag is in the operator's settings file rather than in local storage.
func TestStorageFirstRunIsAcknowledgedOncePerInstall(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)
	if err := SaveStorageSettings(settings, false, storageModeSourceUser, true); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	// A probed instance with an empty archive: the fresh install this dialog
	// exists for. Nothing here makes it "past" its first run.
	ncAccessSubstrate.setProbe(ncStorageProbe{ServiceAccount: true, FolderProbed: true, DefaultRootProbed: true})
	ncAccessSubstrate.succeed()
	cfg := testExAppConfig("http://nextcloud.invalid")

	if body := getStorage(t, cfg, rt); !body.FirstRun {
		t.Fatal("a fresh install with an empty archive did not report first_run")
	}

	// The action answers with the full status, so the caller never has to follow
	// up with a GET to find out what it changed.
	body := postStorageAction(t, cfg, rt, `{"action":"acknowledge_first_run"}`)
	if body.FirstRun {
		t.Fatalf("first_run is still true after acknowledging it: %+v", body)
	}
	if len(body.Modes) != 2 || body.Mode != storageModeDefault {
		t.Fatalf("the acknowledgement did not answer with the full storage status: %+v", body)
	}

	// Persisted per install: the record, not the response, is what makes the
	// next container answer the same way.
	persisted := readPersistedMode(t, settings)
	if !persisted.FirstRunAcknowledged {
		t.Fatalf("%s = %+v, want first_run_acknowledged", storageSettingsFileName, persisted)
	}
	// And the mode record it shares a file with is untouched.
	if !persisted.Configured() || persisted.AccessControlled() || !persisted.Clean() {
		t.Fatalf("acknowledging the dialog rewrote the mode record: %+v", persisted)
	}

	// Idempotent: a double-click, or a retry of a request whose response was
	// lost, is the same request.
	if again := postStorageAction(t, cfg, rt, `{"action":"acknowledge_first_run"}`); again.FirstRun {
		t.Fatalf("a second acknowledgement re-opened the first run: %+v", again)
	}
	if persisted := readPersistedMode(t, settings); !persisted.FirstRunAcknowledged || persisted.AccessControlled() {
		t.Fatalf("%s after a second acknowledgement = %+v", storageSettingsFileName, persisted)
	}
}

// The dialog can be answered before a mode has ever been written down — an
// install whose settings file does not exist yet. Recording the answer must not
// invent a decision nobody took.
func TestStorageFirstRunAcknowledgementDoesNotDecideAMode(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	resetStorageMode(t)
	settings := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(settings)

	body := postStorageAction(t, testExAppConfig("http://nextcloud.invalid"), rt, `{"action":"acknowledge_first_run"}`)
	if body.FirstRun {
		t.Fatalf("first_run is still true after acknowledging it: %+v", body)
	}
	if body.Mode != "" || !body.AwaitingChoice {
		t.Fatalf("the acknowledgement decided a mode: mode=%q awaiting_choice=%t", body.Mode, body.AwaitingChoice)
	}
	persisted := readPersistedMode(t, settings)
	if !persisted.FirstRunAcknowledged || persisted.Configured() {
		t.Fatalf("%s = %+v, want the acknowledgement alone", storageSettingsFileName, persisted)
	}
}

// The acknowledgement outlives the mode state machine. Every step of a switch
// rewrites storage_settings.json, and a switch that put the first-run dialog
// back in front of the administrator who had just used it would be absurd.
func TestStorageFirstRunAcknowledgementSurvivesAModeWrite(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)
	ncAccessSubstrate.setProbe(ncStorageProbe{ServiceAccount: true})
	ncAccessSubstrate.succeed()
	cfg := testExAppConfig("http://nextcloud.invalid")

	postStorageAction(t, cfg, rt, `{"action":"acknowledge_first_run"}`)
	if err := SaveStorageSettings(settings, true, storageModeSourceUser, false); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	if persisted := readPersistedMode(t, settings); !persisted.FirstRunAcknowledged {
		t.Fatalf("a mode write dropped the first-run acknowledgement: %+v", persisted)
	}
}

// An install that has been keeping recordings under a decided mode is past its
// first run, whichever release it happened under. Both halves of that rule
// matter, so both are pinned here.
func TestStorageFirstRunIsFalseForAnInstallThatIsAlreadyPastIt(t *testing.T) {
	withRecordings := ncStorageProbe{ACLArchive: ncArchiveFacts{Probed: true, Present: true, Entries: []davEntry{{Name: "m1.opus"}}}}
	for _, tc := range []struct {
		name         string
		modeRecorded bool
		probed       bool
		probe        ncStorageProbe
		want         bool
	}{
		{
			// The upgrade: a mode is recorded and the archive is not empty.
			name:         "recorded mode and recordings",
			modeRecorded: true,
			probed:       true,
			probe:        withRecordings,
			want:         false,
		},
		{
			// A mode with nothing under it is the fresh install this dialog is
			// for — the operator records one on enable, which is exactly not
			// evidence that anybody saw anything.
			name:         "recorded mode, empty archive",
			modeRecorded: true,
			probed:       true,
			want:         true,
		},
		{
			// Recordings the operator has not resolved a mode for yet. Who can
			// read them is still the open question.
			name:   "recordings, no recorded mode",
			probed: true,
			probe:  withRecordings,
			want:   true,
		},
		{
			// Failing to look is not evidence of an empty archive, but it is not
			// evidence of a full one either, and the cost of being wrong here is
			// one dialog.
			name:         "recorded mode, never probed",
			modeRecorded: true,
			probe:        withRecordings,
			want:         true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storageFirstRun(false, tc.modeRecorded, tc.probed, tc.probe); got != tc.want {
				t.Fatalf("storageFirstRun(false, %t, %t, …) = %t, want %t", tc.modeRecorded, tc.probed, got, tc.want)
			}
			// An answered dialog is answered whatever the instance looks like.
			if got := storageFirstRun(true, tc.modeRecorded, tc.probed, tc.probe); got {
				t.Fatal("an acknowledged install reported first_run again")
			}
		})
	}
}

// The upgrade, end to end through the endpoint: an install with a recorded mode
// and recordings in the Team folder never sees the dialog at all.
func TestStorageDoesNotAskAnUpgradingInstallToSeeTheFirstRunDialog(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, true)
	ncAccessSubstrate.setProbe(ncStorageProbe{
		ServiceAccount: true,
		ACLArchive:     ncArchiveFacts{Probed: true, Present: true, Entries: []davEntry{{Name: "m1.opus"}, {Name: "m2.opus"}}},
	})
	ncAccessSubstrate.succeed()

	body := getStorage(t, testExAppConfig("http://nextcloud.invalid"), rt)
	if body.FirstRun {
		t.Fatalf("an install with a recorded mode and an archive was asked to do its first run again: %+v", body)
	}
}

// Nothing is running, so there is nothing to report. `null` rather than an
// inactive object: the UI branches on the field's presence.
func TestStorageReportsNoMigrationWhenNoneIsRunning(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	rec := httptest.NewRecorder()
	testExAppConfig("http://nextcloud.invalid").storageHandler(rt).
		ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/storage", nil))
	if !strings.Contains(rec.Body.String(), `"migration":null`) {
		t.Fatalf("an idle instance did not report migration:null: %s", rec.Body.String())
	}
}

func TestStorageRejectsAnUnknownAction(t *testing.T) {
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/storage", strings.NewReader(`{"action":"acknowledge"}`))
	testExAppConfig("http://nextcloud.invalid").storageHandler(rt).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST /storage with an unknown action = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), storageActionAcknowledgeFirstRun) {
		t.Fatalf("the error does not offer the acknowledge action: %s", rec.Body.String())
	}
}
