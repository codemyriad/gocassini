package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// storageMock is a Nextcloud whose SHAPE is the test case: which apps are on,
// whether the service account exists, whether there is a mapped Team folder.
//
// It is deliberately separate from provisionMock. That one models the
// provisioner's write sequence and its refusal modes; this one models an
// instance an administrator has (or has not) set up, which is the only thing
// the preflight looks at. Sharing them would mean every new storage shape had
// to be expressible in a mock twenty other tests depend on.
type storageMock struct {
	mu   sync.Mutex
	reqs []string

	apps           []string // nil means both prerequisites are enabled
	serviceAccount bool
	everyoneGroup  bool
	folder         *gfFolder
	recordingsRoot bool
	// failAppList makes Nextcloud refuse to say which apps are enabled, which
	// is a different answer from "that app is off" and must not be read as one.
	failAppList bool
}

func (m *storageMock) saw(method, suffix string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reqs {
		if strings.HasPrefix(r, method+" ") && strings.HasSuffix(r, suffix) {
			return true
		}
	}
	return false
}

func (m *storageMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		m.mu.Lock()
		m.reqs = append(m.reqs, r.Method+" "+p)
		m.mu.Unlock()

		switch {
		case r.Method == http.MethodGet && p == "/ocs/v2.php/apps/app_api/api/v1/users":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":["admin"]}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups/admin":
			if actorOf(r) != defaultNextcloudAdminUser {
				w.WriteHeader(http.StatusUnauthorized)
				io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":997},"data":[]}}`)
				return
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":["admin"]}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/apps":
			if m.failAppList {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			apps := m.apps
			if apps == nil {
				apps = ncRequiredNativeApps
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"apps":`+jsonArray(apps)+`}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/users/"+ncRecordingsOwner:
			if !m.serviceAccount {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":998},"data":[]}}`)
				return
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"id":"`+ncRecordingsOwner+`"}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups":
			groups := []string{}
			if m.everyoneGroup {
				groups = append(groups, ncRecordingsEveryoneGroup)
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"groups":`+jsonArray(groups)+`}}}`)
		case r.Method == http.MethodGet && p == "/index.php/apps/groupfolders/folders":
			if m.folder == nil {
				io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
				return
			}
			encoded, err := json.Marshal(map[string]gfFolder{string(m.folder.ID): *m.folder})
			if err != nil {
				t.Fatalf("encode folder fixture: %v", err)
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":`+string(encoded)+`}}`)
		case r.Method == "PROPFIND" && strings.HasSuffix(p, "/"+ncRecordingsRoot):
			if !m.recordingsRoot {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>`+p+`/</d:href></d:response></d:multistatus>`)
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/catalog.json"):
			w.WriteHeader(http.StatusNotFound)
		default:
			// MKCOL, PROPPATCH and the group-folder writes all succeed.
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// mappedCassiniFolder is a Team folder set up the way the documented recipe
// leaves it: both mappings, advanced ACL on, the owner delegated as manager.
func mappedCassiniFolder() *gfFolder {
	return &gfFolder{
		ID:         "7",
		MountPoint: ncRecordingsMount,
		Groups:     json.RawMessage(fmt.Sprintf(`{"%s":%d,"%s":%d}`, ncRecordingsEveryoneGroup, aclPermRead, ncRecordingsOwnerGroup, aclMaskAll)),
		Manage:     []gfManage{{Type: "user", ID: ncRecordingsOwner}},
		ACL:        true,
	}
}

// runStoragePreflight wires the singletons a preflight touches to throwaway
// state and returns where the mode was persisted.
func runStoragePreflight(t *testing.T, mock *storageMock, logs io.Writer) (cfg ExAppConfig, settingsPath string) {
	t.Helper()
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	settingsPath = filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(settingsPath)
	cfg = testExAppConfig(mock.server(t).URL)
	cfg.preflightNCStorage(context.Background(), log.New(logs, "", 0))
	return cfg, settingsPath
}

func readPersistedMode(t *testing.T, path string) StorageSettings {
	t.Helper()
	settings, err := LoadStorageSettings(path)
	if err != nil {
		t.Fatalf("LoadStorageSettings(%s) error = %v", path, err)
	}
	return settings
}

// The upgrade latch, end to end: an instance the provisioner already built has
// no flag on disk, and must come out of the preflight still access-controlled —
// and with that decision written down, so it is never derived again.
func TestPreflightKeepsAccessControlForAnExistingInstall(t *testing.T) {
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	_, path := runStoragePreflight(t, mock, io.Discard)

	if accessControlled, resolved := ncStorage.mode(); !resolved || !accessControlled {
		t.Fatalf("mode() = (%t, %t), want access control ON and resolved", accessControlled, resolved)
	}
	settings := readPersistedMode(t, path)
	if !settings.Configured() || !settings.AccessControlled() {
		t.Fatalf("%s = %+v, want a recorded access_control_enabled=true", storageSettingsFileName, settings)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate = %+v, want provisioned", snap)
	}
	if snap.Mode != storageModeAccessControlled || snap.ModeSource != storageModeSourceDerived {
		t.Fatalf("status mode = (%q, %q), want (%q, %q)", snap.Mode, snap.ModeSource, storageModeAccessControlled, storageModeSourceDerived)
	}
}

// A Nextcloud with neither prerequisite app and no service account: the derived
// mode is `default`, and the setup is reported as unusable naming the one thing
// that is missing — not as a provisioning failure.
func TestPreflightDerivesDefaultOnADepsFreeInstance(t *testing.T) {
	mock := &storageMock{apps: []string{}}
	var logs strings.Builder
	_, path := runStoragePreflight(t, mock, &logs)

	if accessControlled, resolved := ncStorage.mode(); !resolved || accessControlled {
		t.Fatalf("mode() = (%t, %t), want the default model, resolved", accessControlled, resolved)
	}
	settings := readPersistedMode(t, path)
	if !settings.Configured() || settings.AccessControlled() {
		t.Fatalf("%s = %+v, want a recorded access_control_enabled=false", storageSettingsFileName, settings)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatal("substrate reported healthy with no service account to write recordings as")
	}
	if snap.Step != storageStepServiceAccount {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepServiceAccount)
	}
	if !strings.Contains(snap.Detail, "occ user:add") {
		t.Fatalf("detail %q does not tell the administrator how to create the account", snap.Detail)
	}
}

// The whole point of the deps-free model: neither third-party app, one service
// account, and the app works.
func TestPreflightAcceptsADepsFreeInstanceWithAServiceAccount(t *testing.T) {
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	runStoragePreflight(t, mock, io.Discard)

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate = %+v, want usable with only a service account", snap)
	}
	if snap.Mode != storageModeDefault {
		t.Fatalf("mode = %q, want %q", snap.Mode, storageModeDefault)
	}
	if !mock.saw("MKCOL", "/"+ncRecordingsRoot+"/meetings") {
		t.Fatalf("the canonical collections were never created; requests: %v", mock.reqs)
	}
	// Nothing was scaffolded on the administrator's behalf.
	for _, forbidden := range []string{"/ocs/v2.php/cloud/users", "/ocs/v2.php/cloud/groups", "/index.php/apps/groupfolders/folders"} {
		if mock.saw(http.MethodPost, forbidden) {
			t.Errorf("the preflight POSTed to %s — the first pass must create no prerequisites", forbidden)
		}
	}
}

// A recorded flag is obeyed and never re-derived, and when it disagrees with
// the storage the disagreement is what /status reports.
func TestPreflightHonoursARecordedFlagAndReportsAMismatch(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	// The instance is still fully access-controlled — a Team folder is mapped
	// over the canonical path — so publishing under the recorded default model
	// would write into the shared folder, not the private home.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("the recorded flag was overridden by a derivation")
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatal("a mode that disagrees with the storage must not report healthy")
	}
	if !strings.HasPrefix(snap.Step, storageStepModeMismatch) {
		t.Fatalf("step = %q, want a %q-prefixed step", snap.Step, storageStepModeMismatch)
	}
	if snap.ModeSource != storageModeSourceConfigured {
		t.Fatalf("mode_source = %q, want %q", snap.ModeSource, storageModeSourceConfigured)
	}
}

// An unreadable settings file must not fall through to a derivation that could
// answer `default` and publish the next recording org-wide.
func TestPreflightKeepsAccessControlWhenTheSettingsFileIsUnreadable(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ncStorage.setPath(path)

	// Deliberately a deps-free instance, whose DERIVED answer would be `default`.
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	var logs strings.Builder
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(&logs, "", 0))

	if accessControlled, _ := ncStorage.mode(); !accessControlled {
		t.Fatal("an unreadable storage_settings.json resolved to the open model")
	}
	if !strings.Contains(logs.String(), "keeping access control ON") {
		t.Fatalf("the refusal was not explained in the log:\n%s", logs.String())
	}
}

// The preflight must not create anything when the access-controlled model is
// selected but incomplete — it reports the missing prerequisite instead.
func TestPreflightScaffoldsNothingWhenAccessControlIsIncomplete(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, true, storageModeSourceUser); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	// Both apps enabled and an account, but no Team folder.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.Step != storageStepGroupFolder {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepGroupFolder)
	}
	if mock.saw(http.MethodPost, "/index.php/apps/groupfolders/folders") {
		t.Fatal("the preflight created the Team folder — the first pass must only say how to create it")
	}
	if !strings.Contains(snap.Detail, "occ groupfolders:create") {
		t.Fatalf("detail %q does not carry the command that would fix it", snap.Detail)
	}
}

// A preflight run must report ITS OWN verdict. succeed() deliberately refuses
// to overwrite a recorded degradation, which is right within one run and wrong
// across them: without a reset an administrator who installs the missing app
// and re-enables Cassini gets a run where everything works and a status that
// still says what was wrong before it — so publishing and recording stay
// refused and the documented remedy appears to do nothing.
func TestPreflightClearsAnEarlierFailureWhenTheInstanceIsFixed(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, true, storageModeSourceUser); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	// First run: the Team folder is missing, so the substrate is unavailable.
	broken := &storageMock{serviceAccount: true, everyoneGroup: true}
	testExAppConfig(broken.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Step != storageStepGroupFolder {
		t.Fatalf("first run step = %q, want %q", snap.Step, storageStepGroupFolder)
	}

	// The administrator creates the folder and re-enables the app.
	fixed := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	testExAppConfig(fixed.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate = %+v after the fix; the earlier failure was never cleared", snap)
	}
	if !ncAccessSubstrate.usable() {
		t.Fatal("publishing is still refused on an instance that is now correct")
	}
	if ncAccessSubstrate.recordingRefusal() != "" {
		t.Fatal("recording is still refused on an instance that is now correct")
	}
}

// The dangerous shape the read-path guard exists for: `group_everyone` off
// while `groupfolders` is on and a mapped Cassini folder still shadows the
// canonical path. The Team folder must still be looked at.
func TestPreflightStillSeesAMountedFolderWhenTheEveryoneAppIsOff(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	mock := &storageMock{
		apps:           []string{ncAppGroupFolders},
		serviceAccount: true,
		folder:         mappedCassiniFolder(),
		recordingsRoot: true,
	}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	probe, probed := ncAccessSubstrate.lastProbe()
	if !probed || !probe.FolderMounted {
		t.Fatalf("probe did not see the mounted Team folder: %+v", probe)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatal("default mode reported healthy over a mounted Team folder")
	}
	if ncStorageServesAsOwner() {
		t.Fatal("the read proxy would serve the Team folder's recordings as their ACL manager, to everybody")
	}
}

// An unanswerable apps question must not read as "no Team folder can exist".
func TestPreflightRefusesDefaultModeWhenNextcloudWillNotSayWhichAppsAreOn(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	mock := &storageMock{serviceAccount: true, failAppList: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatalf("substrate = %+v; an unanswered apps question is not evidence that nothing is mounted", snap)
	}
	if ncStorageServesAsOwner() {
		t.Fatal("the read proxy switched to reading as the owner on an instance nothing could be established about")
	}
}

// The derivation runs once, on whichever enabled edge comes first — which is
// not always a moment the instance is finished. A substrate built with `occ`
// moments earlier may not have reached the web workers the probe asks, so a
// fully access-controlled Nextcloud can derive `default` and be stuck with it:
// publishing refused, `mode_mismatch` forever, no way back. The installed-ExApp
// e2e caught exactly that.
//
// Reconsidering can only ever NARROW who may read the archive, so it cannot
// cause the disclosure the latch exists to prevent.
func TestPreflightReconsidersADerivedDefaultOnAnAccessControlledInstance(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	// What an early derivation leaves behind.
	if err := SaveStorageSettings(path, false, storageModeSourceDerived); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	// The instance the probe can now see: complete, access-controlled.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	var logs strings.Builder
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(&logs, "", 0))

	if accessControlled, _ := ncStorage.mode(); !accessControlled {
		t.Fatalf("stayed on the derived default over a complete access-controlled substrate:\n%s", logs.String())
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !snap.OK {
		t.Fatalf("substrate = %+v, want usable after adopting access control", snap)
	}
	// And the correction is durable, so the next enable does not undo it.
	persisted := readPersistedMode(t, path)
	if !persisted.AccessControlled() {
		t.Fatalf("%s = %+v, want the corrected mode written down", storageSettingsFileName, persisted)
	}
}

// The other direction is the whole point of the latch, and must never happen:
// a derived ACCESS-CONTROLLED mode is not reconsidered towards default, because
// that would widen an archive nobody asked to widen.
func TestPreflightNeverReconsidersTowardsTheOpenMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, true, storageModeSourceDerived); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	// The substrate has gone away — the derivation would now say `default`.
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); !accessControlled {
		t.Fatal("a derived access-controlled mode was widened to default; the latch exists to stop exactly this")
	}
	if readPersistedMode(t, path).AccessControlled() != true {
		t.Fatal("the recorded mode was widened on disk")
	}
}

// An administrator who chose default meant it. Reconsidering that would
// override a decision, not repair a guess.
func TestPreflightNeverReconsidersAModeAnAdministratorChose(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("an administrator's explicit choice of default was overridden")
	}
	// It is still reported as a mismatch, which is the honest outcome: they
	// have a mounted Team folder and a default mode, and only they can say
	// which one they meant.
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !strings.HasPrefix(snap.Step, storageStepModeMismatch) {
		t.Fatalf("step = %q, want the mismatch reported rather than silently fixed", snap.Step)
	}
}

// The declared mode wins over the derivation on a fresh install, and is written
// down as stated — so it survives, and is never reconsidered.
func TestPreflightHonoursTheDeclaredInitialMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	t.Setenv(envStorageMode, "default")

	// An instance whose substrate is complete: the derivation would say access
	// controlled, and the declaration must beat it.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("the derivation overrode a declared mode")
	}
	persisted := readPersistedMode(t, path)
	if persisted.AccessControlled() || !persisted.Chosen() {
		t.Fatalf("%s = %+v, want a stated default", storageSettingsFileName, persisted)
	}
	// Stated, so the narrowing self-heal leaves it alone on the next run.
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))
	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("a declared default was reconsidered; a stated mode is never touched")
	}
}

// Declaring access control on a stack that has not been built yet is the
// debugging shape the harness's --debug-skip-storage-scaffold produces: the
// mode is selected, nothing exists, and the app reports what is missing rather
// than quietly falling back to the open model.
func TestPreflightHonoursADeclaredAccessControlOnAnEmptyInstance(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))
	t.Setenv(envStorageMode, "acl-enabled")

	mock := &storageMock{apps: []string{}}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); !accessControlled {
		t.Fatal("a declared access-controlled mode fell back to default")
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatal("reported healthy with nothing built")
	}
	if !strings.HasPrefix(snap.Step, "app_missing:") {
		t.Fatalf("step = %q, want the first missing prerequisite named", snap.Step)
	}
}

// A misspelt deploy option falls back to deriving rather than to a guess, and
// says so — the value is the operator's typo, not a mode.
func TestPreflightIgnoresAnUnrecognisedDeclaredMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))
	t.Setenv(envStorageMode, "acl_enabld")

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	var logs strings.Builder
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(&logs, "", 0))

	// Derived, not defaulted: this instance is access-controlled.
	if accessControlled, _ := ncStorage.mode(); !accessControlled {
		t.Fatalf("fell back to the open model instead of deriving:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "acl_enabld") {
		t.Fatalf("the rejected value was not named in the log:\n%s", logs.String())
	}
}
