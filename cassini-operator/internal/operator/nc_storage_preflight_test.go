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
	"time"
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

// The mode is NEVER inferred from the instance. A Nextcloud carrying the entire
// access-controlled substrate, with nothing recorded and nothing declared, falls
// back to `default` like any other — and is then reported as a mismatch, because
// its Team folder is mounted over the path the default model writes to.
//
// This is the case the removed derivation existed to smooth over. Smoothing it
// over made who can read the archive a function of what Nextcloud looked like at
// one instant, and got it wrong on a stack still being assembled. The loud
// failure is the feature: CASSINI_STORAGE_MODE is how a deployment avoids it.
func TestPreflightNeverInfersAccessControlFromACompleteSubstrate(t *testing.T) {
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	_, path := runStoragePreflight(t, mock, io.Discard)

	if accessControlled, resolved := ncStorage.mode(); !resolved || accessControlled {
		t.Fatalf("mode() = (%t, %t), want the default model — a complete substrate must not decide the mode", accessControlled, resolved)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatalf("substrate = %+v, want unusable: a mounted Team folder wins the path the default model writes to", snap)
	}
	if snap.Step != storageStepModeMismatch+":"+storageStepFolderMount {
		t.Fatalf("step = %q, want the mounted-folder mismatch", snap.Step)
	}
	// It must also name the way out, because this is exactly what an
	// access-controlled install upgrading into this build looks like.
	if !strings.Contains(snap.Detail, envStorageMode) {
		t.Fatalf("detail %q never mentions %s, so an administrator is told what is wrong but not how to fix it", snap.Detail, envStorageMode)
	}
	// And nothing is written down, so setting the deploy option still works.
	if readPersistedMode(t, path).Configured() {
		t.Fatalf("%s recorded a mode the sanity gate rejected; a recorded mode is never reconsidered, so this would shadow %s forever", storageSettingsFileName, envStorageMode)
	}
}

// A Nextcloud with neither prerequisite app and no service account: the mode
// falls back to `default`, and the setup is reported as unusable naming the one
// thing that is missing — not as a provisioning failure.
func TestPreflightFallsBackToDefaultOnADepsFreeInstance(t *testing.T) {
	mock := &storageMock{apps: []string{}}
	var logs strings.Builder
	_, path := runStoragePreflight(t, mock, &logs)

	if accessControlled, resolved := ncStorage.mode(); !resolved || accessControlled {
		t.Fatalf("mode() = (%t, %t), want the default model, resolved", accessControlled, resolved)
	}
	// The fallback failed its own sanity gate (no service account), so it is not
	// recorded — the next enable decides again, once the account exists.
	if readPersistedMode(t, path).Configured() {
		t.Fatalf("%s recorded a mode that could not be used", storageSettingsFileName)
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
	_, path := runStoragePreflight(t, mock, io.Discard)

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate = %+v, want usable with only a service account", snap)
	}
	if snap.Mode != storageModeDefault || snap.ModeSource != storageModeSourceDefault {
		t.Fatalf("status mode = (%q, %q), want (%q, %q)", snap.Mode, snap.ModeSource, storageModeDefault, storageModeSourceDefault)
	}
	// This fallback DID survive its sanity gate, so it is written down — the
	// counterpart to the deps-free test above, where it is not.
	settings := readPersistedMode(t, path)
	if !settings.Configured() || settings.AccessControlled() {
		t.Fatalf("%s = %+v, want a recorded access_control_enabled=false", storageSettingsFileName, settings)
	}
	if settings.Source != storageModeSourceDefault {
		t.Fatalf("recorded source = %q, want %q", settings.Source, storageModeSourceDefault)
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
// The deletion, pinned as a negative test.
//
// An earlier build reconsidered a recorded `default` against the live instance
// and adopted access control when the substrate was complete. That made the file
// non-authoritative — it could say `default` while the app acted
// access-controlled, because Nextcloud had changed underneath it. Nothing
// re-opens a recorded decision now, and this test fails if that comes back.
func TestPreflightNeverReconsidersARecordedMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceDefault); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	ncStorage.setPath(path)

	// The instance the probe can see: complete, access-controlled.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	var logs strings.Builder
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(&logs, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatalf("the recorded default was re-opened against the instance:\n%s", logs.String())
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.ModeSource != storageModeSourceConfigured {
		t.Fatalf("mode source = %q, want %q — anything read from disk reports as configured", snap.ModeSource, storageModeSourceConfigured)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("%s was rewritten:\nbefore %s\nafter  %s", storageSettingsFileName, before, after)
	}
}

// A recorded ACCESS-CONTROLLED mode is never widened to default, whatever the
// instance looks like — that would widen an archive nobody asked to widen.
func TestPreflightNeverWidensARecordedMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, true, storageModeSourceEnv); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	// The substrate has gone away entirely.
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); !accessControlled {
		t.Fatal("a recorded access-controlled mode was widened to default; nothing may re-open a recorded decision")
	}
	if readPersistedMode(t, path).AccessControlled() != true {
		t.Fatal("the recorded mode was widened on disk")
	}
}

// An administrator who chose default meant it, and a mounted Team folder does
// not overrule them — it is reported, not resolved.
func TestPreflightReportsAMismatchRatherThanOverrulingAnAdministrator(t *testing.T) {
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

// A declared mode is written down IMMEDIATELY, before the sanity gate, because
// it is a decision rather than a fallback — an administrator who set the deploy
// option should be able to read the mode back even while the storage behind it
// is still missing. The fallback is the opposite; see the deferred-persist test.
func TestPreflightHonoursTheDeclaredInitialMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	t.Setenv(envStorageMode, "default")

	// An instance whose substrate is complete. The declaration decides anyway,
	// and the resulting mismatch is reported rather than resolved.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("the instance overrode a declared mode")
	}
	persisted := readPersistedMode(t, path)
	if !persisted.Configured() || persisted.AccessControlled() || persisted.Source != storageModeSourceEnv {
		t.Fatalf("%s = %+v, want a recorded default with source %q — written even though the gate then failed", storageSettingsFileName, persisted, storageModeSourceEnv)
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !strings.HasPrefix(snap.Step, storageStepModeMismatch) {
		t.Fatalf("step = %q, want the mismatch reported", snap.Step)
	}
	// Recorded, so the next run reads it back rather than consulting the
	// environment again.
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))
	if _, source := ncStorage.snapshot(); source != storageModeSourceConfigured {
		t.Fatalf("source = %q on the second run, want %q — the file is authoritative once written", source, storageModeSourceConfigured)
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

// A misspelt deploy option falls back to the default model and says so loudly —
// the value is the operator's typo, not a mode.
func TestPreflightIgnoresAnUnrecognisedDeclaredMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))
	t.Setenv(envStorageMode, "acl_enabld")

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	var logs strings.Builder
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(&logs, "", 0))

	// A typo is not a mode. It falls through to the fallback like an unset
	// variable would — it does NOT become access control because the instance
	// happens to look access-controlled, which is the inference that was removed.
	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatalf("a rejected value still produced access control:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "acl_enabld") {
		t.Fatalf("the rejected value was not named in the log:\n%s", logs.String())
	}
	// And the resulting mismatch is reported, so the typo surfaces as a problem
	// rather than as a quietly different instance.
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !strings.HasPrefix(snap.Step, storageStepModeMismatch) {
		t.Fatalf("step = %q, want the mismatch reported", snap.Step)
	}
}

// Deferred persist, over two runs. The fallback is written down only once the
// sanity gate has agreed the instance can actually run it.
//
// The order matters more than it looks. A recorded mode is never reconsidered,
// so recording a `default` this instance cannot run would shadow
// CASSINI_STORAGE_MODE — and the deploy option is exactly what an administrator
// reaches for to resolve the mismatch. Deciding late costs one probe per enable
// until the instance is coherent, and keeps every escape hatch open.
func TestPreflightRecordsTheFallbackOnlyOnceItSurvivesTheGate(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)

	// Run 1: the default mode cannot run here — no service account.
	broken := &storageMock{apps: []string{}}
	testExAppConfig(broken.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("os.Stat(%s) = %v, want the file absent — a mode the gate rejected must not be recorded", storageSettingsFileName, err)
	}
	// The env var still works, which is the whole reason for the deferral.
	if _, source := ncStorage.snapshot(); source != storageModeSourceDefault {
		t.Fatalf("source = %q, want %q", source, storageModeSourceDefault)
	}

	// Run 2: the administrator created the account. Now it records.
	resetSubstrateRecord(t)
	fixed := &storageMock{apps: []string{}, serviceAccount: true}
	testExAppConfig(fixed.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	settings := readPersistedMode(t, path)
	if !settings.Configured() || settings.AccessControlled() {
		t.Fatalf("%s = %+v, want a recorded default once the gate passed", storageSettingsFileName, settings)
	}
	if settings.Source != storageModeSourceDefault {
		t.Fatalf("recorded source = %q, want %q", settings.Source, storageModeSourceDefault)
	}
}

// The counterpart to the deferral, and the reason it is safe: a mismatch leaves
// CASSINI_STORAGE_MODE able to fix it. This is the recovery path an
// access-controlled install upgrading into this build actually walks.
func TestADeclaredModeRescuesAnInstanceStuckOnTheFallback(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}

	// Run 1: nothing declared. Falls back to default, which this instance cannot
	// run, and records nothing.
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.OK {
		t.Fatal("a mounted Team folder under the default mode reported healthy")
	}

	// Run 2: the administrator sets the deploy option and re-enables.
	t.Setenv(envStorageMode, storageModeAccessControlled)
	resetSubstrateRecord(t)
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); !accessControlled {
		t.Fatal("the declaration did not take; the fallback had recorded a mode that shadowed it")
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !snap.OK {
		t.Fatalf("substrate = %+v, want usable once the mode matches the storage", snap)
	}
	if settings := readPersistedMode(t, path); settings.Source != storageModeSourceEnv {
		t.Fatalf("recorded source = %q, want %q", settings.Source, storageModeSourceEnv)
	}
}

// The folder list must never be swallowed into "there is no Cassini folder".
//
// Group Folders answers with EVERY folder on the instance in one list, and Go
// fails the whole decode if any single record does not fit gfFolder — one
// unrelated folder whose `acl` comes back as 0 rather than false is enough. Read
// as "absent", that answer passes the default mode's sanity check on an instance
// that really does have a mapped, ACL-enabled Cassini Team folder: the substrate
// records `provisioned`, and the read proxy then serves the entire archive as
// the ACL manager to every authenticated account.
//
// Each case below is a real Cassini folder the probe must NOT miss. The
// assertion is that the probe fails closed (FolderProbed false → the default
// mode is refused), not that it finds the folder.
func TestProbeRefusesAFolderListItCouldNotUnderstand(t *testing.T) {
	cassini := mappedCassiniFolder()
	encoded, err := json.Marshal(map[string]gfFolder{string(cassini.ID): *cassini})
	if err != nil {
		t.Fatalf("encode folder fixture: %v", err)
	}
	withCassini := string(encoded[1 : len(encoded)-1]) // strip the braces to splice siblings in

	cases := []struct {
		name string
		data string
	}{
		{
			// The one that actually happens: another folder on the instance whose
			// `acl` is an int. The Cassini record right beside it is perfect.
			name: "a sibling folder with acl as an int",
			data: `{` + withCassini + `,"3":{"id":3,"mount_point":"Other","groups":[],"acl":0}}`,
		},
		{
			name: "a sibling whose manage is an object rather than a list",
			data: `{` + withCassini + `,"3":{"id":"3","mount_point":"Other","manage":{"type":"user"}}}`,
		},
		{"data is null", `null`},
		{"data is a scalar", `"nope"`},
		{"data is a number", `12`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetProvisioningUser(t)
			resetSubstrateRecord(t)
			resetStorageMode(t)
			ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch p := r.URL.Path; {
				case p == "/ocs/v2.php/apps/app_api/api/v1/users":
					io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":["admin"]}}`)
				case p == "/ocs/v2.php/cloud/groups/admin":
					io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":["admin"]}}}`)
				case p == "/ocs/v2.php/cloud/apps":
					io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"apps":`+jsonArray(ncRequiredNativeApps)+`}}}`)
				case p == "/ocs/v2.php/cloud/users/"+ncRecordingsOwner:
					io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"id":"`+ncRecordingsOwner+`"}}}`)
				case p == "/ocs/v2.php/cloud/groups":
					io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"groups":`+jsonArray([]string{ncRecordingsEveryoneGroup})+`}}}`)
				case p == "/index.php/apps/groupfolders/folders":
					io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":`+tc.data+`}}`)
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(srv.Close)

			cfg := testExAppConfig(srv.URL)
			probe, err := cfg.probeNCStorage(context.Background(), &http.Client{}, log.New(io.Discard, "", 0))
			if err != nil {
				t.Fatalf("probeNCStorage() error = %v", err)
			}
			if probe.FolderProbed {
				t.Fatalf("FolderProbed = true after a list the probe could not read; an unanswered question must not read as \"no folder\"")
			}
			// Which is what makes the default mode refuse rather than serve.
			if ok, step, _ := probe.sanity(false); ok {
				t.Fatal("the default mode was accepted on an unreadable folder list")
			} else if step != storageStepModeMismatch+":"+storageStepFolderUnknown {
				t.Fatalf("step = %q, want the folder-unknown mismatch", step)
			}
		})
	}
}

// The counterpart: a list the probe CAN read, with an unrelated sibling, still
// finds the Cassini folder. Failing closed must not mean failing always.
func TestProbeFindsCassiniBesideAWellFormedSibling(t *testing.T) {
	cassini := mappedCassiniFolder()
	encoded, err := json.Marshal(map[string]gfFolder{string(cassini.ID): *cassini})
	if err != nil {
		t.Fatalf("encode folder fixture: %v", err)
	}
	data := `{` + string(encoded[1:len(encoded)-1]) + `,"3":{"id":"3","mount_point":"Other","groups":{},"acl":false}}`

	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch p := r.URL.Path; {
		case p == "/ocs/v2.php/apps/app_api/api/v1/users":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":["admin"]}}`)
		case p == "/ocs/v2.php/cloud/groups/admin":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":["admin"]}}}`)
		case p == "/ocs/v2.php/cloud/apps":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"apps":`+jsonArray(ncRequiredNativeApps)+`}}}`)
		case p == "/ocs/v2.php/cloud/users/"+ncRecordingsOwner:
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"id":"`+ncRecordingsOwner+`"}}}`)
		case p == "/ocs/v2.php/cloud/groups":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"groups":`+jsonArray([]string{ncRecordingsEveryoneGroup})+`}}}`)
		case p == "/index.php/apps/groupfolders/folders":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":`+data+`}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	probe, err := testExAppConfig(srv.URL).probeNCStorage(context.Background(), &http.Client{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("probeNCStorage() error = %v", err)
	}
	if !probe.FolderProbed || !probe.FolderPresent || !probe.FolderMounted {
		t.Fatalf("probe = %+v, want the Cassini folder found beside its sibling", probe)
	}
	if ready, step, detail := probe.accessControlReady(); !ready {
		t.Fatalf("access control not ready: %s — %s", step, detail)
	}
}

// An empty instance answers `[]`, and that IS an answer: no folders.
func TestProbeAcceptsAnEmptyFolderList(t *testing.T) {
	mock := &storageMock{apps: nil, serviceAccount: true}
	cfg, _ := runStoragePreflight(t, mock, io.Discard)
	_ = cfg
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate = %+v, want usable: an empty folder list means no folder is in the way", snap)
	}
}

// D-541/D-669: a bare container restart must not leave the substrate at
// `unknown` with publishing refused until somebody disables and re-enables the
// app. The recorded mode is what makes the startup run both safe and possible.
func TestPreflightOnRestartProvesARecordedMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	if err := SaveStorageSettings(path, false, storageModeSourceEnv); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}

	// A healthy default-mode instance, and NO enabled edge — only the restart.
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	cfg := testExAppConfig(mock.server(t).URL)
	cfg.preflightOnRestart(context.Background(), log.New(io.Discard, "", 0))

	waitForSubstrate(t, func(snap statusRecordingsAccess) bool { return snap.OK })
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.State == string(ncSubstrateUnknown) {
		t.Fatalf("substrate = %+v, want it proven without an enable edge", snap)
	}
	if snap.Mode != storageModeDefault {
		t.Fatalf("mode = %q, want %q", snap.Mode, storageModeDefault)
	}
}

// The other half of the condition: an install with NO recorded mode must not
// preflight at startup. AppAPI rejects act-as-user calls during registration, so
// a startup run there deterministically 401s — it would log a failure on every
// new install, for a record the enabled edge is about to write anyway.
func TestPreflightOnRestartStaysOutOfTheWayOfAFirstRegistration(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))

	mock := &storageMock{apps: []string{}, serviceAccount: true}
	cfg := testExAppConfig(mock.server(t).URL)
	cfg.preflightOnRestart(context.Background(), log.New(io.Discard, "", 0))

	// Nothing should have been asked of Nextcloud at all.
	time.Sleep(50 * time.Millisecond)
	if mock.saw(http.MethodGet, "/ocs/v2.php/cloud/apps") {
		t.Fatal("the startup preflight probed Nextcloud on an install with no recorded mode")
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.State != string(ncSubstrateUnknown) {
		t.Fatalf("substrate = %+v, want untouched", snap)
	}
}

// waitForSubstrate polls the substrate record, which preflightOnRestart writes
// from a goroutine so an unreachable Nextcloud cannot stop the operator serving
// /status.
func waitForSubstrate(t *testing.T, ok func(statusRecordingsAccess) bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok(ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("substrate never reached the expected state: %+v", ncAccessSubstrate.snapshot(publishSinkNextcloudFiles))
}
