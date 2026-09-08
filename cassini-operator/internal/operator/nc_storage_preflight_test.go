package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	// aclArchive lists the recordings in the Team folder's meetings/ collection.
	// It is what the upgrade latch keys on: a mounted but EMPTY Team folder is
	// what a completed opt-out leaves behind and must not be refused, while one
	// that still holds recordings under a fallback `default` is an install that
	// has never been told which model it is in.
	aclArchive []string
	// defaultArchive is the same for the default model's own root.
	defaultArchive []string
	// failAppList makes Nextcloud refuse to say which apps are enabled, which
	// is a different answer from "that app is off" and must not be read as one.
	failAppList bool
	// failPropfindAll makes every PROPFIND answer 500 — a failed LOOK, which is
	// a different answer from "there is nothing here" and must never render as
	// "nothing to move".
	failPropfindAll bool

	// homeChildren and dirs give the service account a filesystem, for the
	// tests that care what is IN the archive rather than only whether its root
	// exists. homeChildren lists the account's home root; dirs maps a
	// home-relative directory to its children. Absent from dirs means 404,
	// which is how "no such collection" reads on the wire.
	homeChildren []string
	dirs         map[string][]string
}

// propfindMultistatus renders a Depth-1 listing the way davPropfindChildren
// parses it: the collection lists itself first, then each child.
func propfindMultistatus(selfPath string, children []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`)
	b.WriteString(`<d:response><d:href>` + selfPath + `/</d:href></d:response>`)
	for _, name := range children {
		b.WriteString(`<d:response><d:href>` + strings.TrimRight(selfPath, "/") + "/" + url.PathEscape(name) + `</d:href></d:response>`)
	}
	b.WriteString(`</d:multistatus>`)
	return b.String()
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
		case r.Method == "PROPFIND" && m.failPropfindAll:
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == "PROPFIND" && (m.homeChildren != nil || m.dirs != nil):
			// A modelled filesystem. The home root lists homeChildren; every
			// other collection lists dirs[rel], and anything absent is a 404.
			prefix := "/remote.php/dav/files/" + ncRecordingsOwner
			rel := strings.Trim(strings.TrimPrefix(p, prefix), "/")
			if decoded, err := url.PathUnescape(rel); err == nil {
				rel = decoded
			}
			children, known := m.dirs[rel], false
			if rel == "" {
				children, known = m.homeChildren, true
			} else {
				_, known = m.dirs[rel]
			}
			if !known {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, propfindMultistatus(p, children))
		case r.Method == "PROPFIND" && strings.HasSuffix(p, "/"+ncACLRecordingsRoot+"/meetings"):
			if !m.recordingsRoot {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, propfindMultistatus(p, m.aclArchive))
		case r.Method == "PROPFIND" && strings.HasSuffix(p, "/"+ncDefaultRecordingsRoot+"/meetings"):
			if m.defaultArchive == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, propfindMultistatus(p, m.defaultArchive))
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

// The mode is NEVER inferred from the instance, and since D-708 it is never
// fallen back to either. A Nextcloud carrying the entire access-controlled
// substrate, with nothing recorded and nothing declared, is UNDECIDED — it
// publishes nothing and it writes nothing down, until somebody says.
//
// The first pass fell back to the deps-free model here and recorded that on the
// first healthy enable. It is a quieter version of the inference it replaced:
// `default` is the model in which every account can read every recording, and
// nobody had asked for it. An upgrade latch caught the one shape where the
// mistake would have been obvious; nothing caught the rest.
func TestPreflightLeavesACompleteSubstrateUndecided(t *testing.T) {
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true, aclArchive: []string{"m1.opus", "m2.opus"}}
	_, path := runStoragePreflight(t, mock, io.Discard)

	if _, resolved := ncStorage.mode(); resolved {
		t.Fatal("a complete substrate decided the mode; nothing may choose who can read the archive")
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatalf("substrate = %+v, want unusable: nobody has chosen a storage model", snap)
	}
	if snap.Step != storageStepModeUndecided {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepModeUndecided)
	}
	// The detail has to name both models, because the whole remedy is a choice
	// between them.
	for _, want := range []string{storageModeDefault, storageModeAccessControlled, ncDefaultRecordingsRoot, ncRecordingsMount} {
		if !strings.Contains(snap.Detail, want) {
			t.Fatalf("detail %q never mentions %q", snap.Detail, want)
		}
	}
	if readPersistedMode(t, path).Configured() {
		t.Fatalf("%s recorded a mode nobody chose", storageSettingsFileName)
	}
	// Both roots are on the probe, under no mode at all — that symmetry is what
	// the setup wizard's first screen renders.
	probe, probed := ncAccessSubstrate.lastProbe()
	if !probed || probe.ACLArchive.Meetings() != 2 || !probe.DefaultArchive.Probed {
		t.Fatalf("probe did not describe both roots: %+v", probe)
	}
}

// A Nextcloud with neither prerequisite app and no service account is undecided
// too, and its status names the decision rather than the missing account: there
// is no mode yet whose prerequisites could be missing.
func TestPreflightLeavesADepsFreeInstanceUndecided(t *testing.T) {
	mock := &storageMock{apps: []string{}}
	var logs strings.Builder
	_, path := runStoragePreflight(t, mock, &logs)

	if _, resolved := ncStorage.mode(); resolved {
		t.Fatal("an empty instance decided its own mode")
	}
	if readPersistedMode(t, path).Configured() {
		t.Fatalf("%s recorded a mode nobody chose", storageSettingsFileName)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatal("substrate reported healthy with no storage model chosen")
	}
	if snap.Step != storageStepModeUndecided {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepModeUndecided)
	}
}

// The whole point of the deps-free model: neither third-party app, one service
// account, and the app works — once somebody has said that is what they want.
// Here the deployment says it, which is what the deploy option is for.
func TestPreflightAcceptsADepsFreeInstanceWithAServiceAccount(t *testing.T) {
	t.Setenv(envStorageMode, storageModeDefault)
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	_, path := runStoragePreflight(t, mock, io.Discard)

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate = %+v, want usable with only a service account", snap)
	}
	if snap.Mode != storageModeDefault || snap.ModeSource != storageModeSourceEnv {
		t.Fatalf("status mode = (%q, %q), want (%q, %q)", snap.Mode, snap.ModeSource, storageModeDefault, storageModeSourceEnv)
	}
	if !snap.ModeConfirmed {
		t.Fatal("a declared mode is not reported as confirmed; a deploy option is as explicit as a button")
	}
	// The declaration survived both gates, so it is written down — the
	// counterpart to the refusal tests below, where it is not.
	settings := readPersistedMode(t, path)
	if !settings.Configured() || settings.AccessControlled() {
		t.Fatalf("%s = %+v, want a recorded access_control_enabled=false", storageSettingsFileName, settings)
	}
	if settings.Source != storageModeSourceEnv || !settings.Confirmed() {
		t.Fatalf("recorded source = %q (confirmed %t), want %q", settings.Source, settings.Confirmed(), storageModeSourceEnv)
	}
	if !mock.saw("MKCOL", "/"+ncDefaultRecordingsRoot+"/meetings") {
		t.Fatalf("the canonical collections were never created; requests: %v", mock.reqs)
	}
	// Nothing was scaffolded on the administrator's behalf.
	for _, forbidden := range []string{"/ocs/v2.php/cloud/users", "/ocs/v2.php/cloud/groups", "/index.php/apps/groupfolders/folders"} {
		if mock.saw(http.MethodPost, forbidden) {
			t.Errorf("the preflight POSTed to %s — the first pass must create no prerequisites", forbidden)
		}
	}
}

// A recorded flag is obeyed and never re-derived, and an archive left in the
// mode it does NOT name is reported rather than being made into a failure.
//
// The first pass had to refuse here: both models wanted `Cassini/Recordings`, so
// a recorded `default` under a mapped Team folder meant every write landed in
// the shared folder. Since the split it is merely untidy — the default model
// writes and reads its own private root — and refusing would break the ordinary
// post-opt-out instance, where exactly this shape is the steady state.
func TestPreflightHonoursARecordedFlagAndReportsAStrandedArchive(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser, true); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	// The instance is still fully access-controlled — a Team folder is mapped
	// over the canonical path — so publishing under the recorded default model
	// would write into the shared folder, not the private home.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true, aclArchive: []string{"m1.opus", "m2.opus"}}
	cfg := testExAppConfig(mock.server(t).URL)
	cfg.preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("the recorded flag was overridden by a derivation")
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate = %+v; an administrator who recorded `default` can publish into their own private root whatever else is mounted", snap)
	}
	if snap.ModeSource != storageModeSourceUser || !snap.ModeConfirmed {
		t.Fatalf("mode_source = %q (confirmed %t), want %q — the recorded provenance is carried through, not flattened", snap.ModeSource, snap.ModeConfirmed, storageModeSourceUser)
	}
	// But the two recordings nobody is reading are said out loud, because the
	// symptom is "my recordings are gone" and the cause is a mode nobody switched.
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	status := cfg.storageStatus(rt, nil)
	if status.StrandedRecordings != 2 || status.StrandedRoot != ncACLRecordingsRoot {
		t.Fatalf("stranded = %d at %q, want 2 at %q", status.StrandedRecordings, status.StrandedRoot, ncACLRecordingsRoot)
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
	// And it is UNCONFIRMED, so the Setup tab offers a decision rather than
	// presenting one: writing a mode is how an administrator leaves this state.
	// (The step here is the missing prerequisite rather than the unconfirmed
	// mode, because sanity runs first — a named prerequisite is the more
	// actionable of the two, and the wizard reports both regardless.)
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.ModeConfirmed {
		t.Fatalf("a mode nobody could read was reported as one somebody chose: %+v", snap)
	}
}

// The same file, on an instance the assumed mode CAN run: the refusal is then
// the unconfirmed mode itself, which is what the Setup tab keys on.
func TestPreflightRefusesToPublishUnderAModeNobodyChose(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	ncStorage.setPath(path)

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatalf("substrate = %+v, want unusable: the mode governs but nobody chose it", snap)
	}
	if snap.Step != storageStepModeUnconfirmed {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepModeUnconfirmed)
	}
	if !mock.saw("PROPFIND", "/"+ncACLRecordingsRoot+"/meetings") {
		t.Fatal("the probe did not read the archive an administrator has to decide about")
	}
	// Nothing was arranged on the strength of a decision nobody took.
	if mock.saw("PROPPATCH", "/"+ncRecordingsMount) {
		t.Fatal("the container ACL was rewritten under an unconfirmed mode")
	}
}

// The preflight must not create anything when the access-controlled model is
// selected but incomplete — it reports the missing prerequisite instead.
func TestPreflightScaffoldsNothingWhenAccessControlIsIncomplete(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, true, storageModeSourceUser, true); err != nil {
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
	if err := SaveStorageSettings(path, true, storageModeSourceUser, true); err != nil {
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

// `group_everyone` off while `groupfolders` is on: the Team folder must still be
// looked at. Bundling the folder question with the Everyone Group app is what
// made an access-controlled archive read as an unmounted one in the first pass,
// and although the consequence has changed — the folder no longer shadows
// anything the default model touches — the answer is still what tells an
// administrator where their recordings went.
func TestPreflightStillSeesAMountedFolderWhenTheEveryoneAppIsOff(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser, true); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	mock := &storageMock{
		apps:           []string{ncAppGroupFolders},
		serviceAccount: true,
		folder:         mappedCassiniFolder(),
		recordingsRoot: true,
		aclArchive:     []string{"m1.opus"},
	}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	probe, probed := ncAccessSubstrate.lastProbe()
	if !probed || !probe.FolderMounted {
		t.Fatalf("probe did not see the mounted Team folder: %+v", probe)
	}
	if probe.ACLArchive.Meetings() != 1 {
		t.Fatalf("the Team folder's archive was not counted: %+v", probe)
	}
	// And the read proxy still serves the DEFAULT root as its owner, because
	// that root is not the one the Team folder is mounted over. Pairing the
	// owner identity with the access-controlled root is the disclosure; pairing
	// it with the private one is the model.
	if !ncStorageServesAsOwner() {
		t.Fatal("refused to serve the private default root as its owner merely because an unrelated Team folder is mounted")
	}
	if probe.DefaultRootShadowed {
		t.Fatal("a Team folder at Cassini was mistaken for one over the default root")
	}
}

// An unanswerable apps question splits the two halves of the default model
// apart, and that split is the whole point.
//
// WRITING refuses: the model's safety argument is that `CassiniNoACL/Recordings`
// is private, this check is the only thing that confirms it, and publishing
// under an unanswered question puts every recording into a folder that might be
// shared. READING carries on: serving that tree as its owner discloses only what
// the default mode is defined to disclose, and blanking a working archive every
// time one OCS call hiccups is the papercut this branch set out to remove.
func TestPreflightSplitsReadFromWriteWhenNextcloudWillNotSayWhichAppsAreOn(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser, true); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	mock := &storageMock{serviceAccount: true, failAppList: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatalf("substrate = %+v; publishing must not proceed while nobody can say whether the default root is private", snap)
	}
	if snap.Step != storageStepModeMismatch+":"+storageStepDefaultRootUnknown {
		t.Fatalf("step = %q, want the unknown-root mismatch", snap.Step)
	}
	// But reads keep working, which is the half that used to break on every
	// restart. Publishing is refused; the archive is still listed.
	if !ncStorageServesAsOwner() {
		t.Fatal("the read proxy stopped serving the private default root because an unrelated question went unanswered")
	}
	// The unanswered question is recorded as unanswered on BOTH folder axes.
	probe, _ := ncAccessSubstrate.lastProbe()
	if probe.FolderProbed || probe.DefaultRootProbed {
		t.Fatalf("an unanswerable apps question was recorded as an answered folder question: %+v", probe)
	}
	if ready, _, _ := probe.accessControlReady(); ready {
		t.Fatal("access control reported ready on an instance whose apps could not be listed")
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
	if err := SaveStorageSettings(path, false, storageModeSourceDefault, true); err != nil {
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
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.ModeSource != storageModeSourceDefault {
		t.Fatalf("mode source = %q, want %q — the recorded provenance is carried through, not flattened to \"configured\"", snap.ModeSource, storageModeSourceDefault)
	}
	// And a mode an older build recorded on its own is not a decision: it
	// governs, and it does not publish, until somebody confirms it (D-708).
	if snap.ModeConfirmed {
		t.Fatal("a fallback recorded by an earlier build was reported as an administrator's choice")
	}
	if snap.Step != storageStepModeUnconfirmed {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepModeUnconfirmed)
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
	if err := SaveStorageSettings(path, true, storageModeSourceEnv, true); err != nil {
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
// not overrule them.
func TestPreflightDoesNotOverruleAnAdministratorsChoiceOfDefault(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	if err := SaveStorageSettings(path, false, storageModeSourceUser, true); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}
	ncStorage.setPath(path)

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("an administrator's explicit choice of default was overridden")
	}
	// And it is usable. An empty Team folder left mounted is what a completed
	// opt-out looks like; the upgrade latch keys on the ARCHIVE, not the mount,
	// precisely so this instance is not refused.
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !snap.OK {
		t.Fatalf("substrate = %+v, want usable: an emptied Team folder left mounted is the ordinary post-opt-out shape", snap)
	}
}

// A declared mode on an instance it FITS is believed, written down, and then
// authoritative — the deploy option's whole job, for a harness or a CI stack
// that knows what it built.
func TestPreflightHonoursTheDeclaredInitialMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	t.Setenv(envStorageMode, "default")

	// A complete access-controlled substrate whose Team folder is EMPTY, which
	// is what a completed opt-out leaves. Nothing is stranded and nothing is
	// duplicated, so the declaration is coherent with the instance.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	cfg := testExAppConfig(mock.server(t).URL)
	cfg.preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("the instance overrode a declared mode")
	}
	persisted := readPersistedMode(t, path)
	if !persisted.Configured() || persisted.AccessControlled() || persisted.Source != storageModeSourceEnv {
		t.Fatalf("%s = %+v, want a recorded default with source %q", storageSettingsFileName, persisted, storageModeSourceEnv)
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !snap.OK {
		t.Fatalf("substrate = %+v; a declared mode that fits its instance is a decision", snap)
	}
	// Recorded, so the next run reads it back rather than consulting the
	// environment again — and reads back the provenance with it.
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))
	if _, source := ncStorage.snapshot(); source != storageModeSourceEnv {
		t.Fatalf("source = %q on the second run, want %q — the file is authoritative once written, provenance and all", source, storageModeSourceEnv)
	}
}

// A declared mode that would strand a live archive is REFUSED and not recorded
// (D-708).
//
// The first pass believed it and wrote it down immediately, on the argument that
// a deploy option is as explicit as a button. That is true of the intent and
// false of the outcome: the harness declares a mode because it knows what it
// built, so a declaration meeting recordings it was never told about is a bug in
// the stack. Recording it would be the worst of the two outcomes, because a
// recorded mode is never reconsidered.
func TestPreflightRefusesADeclaredModeThatWouldStrandAnArchive(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	t.Setenv(envStorageMode, "default")

	// The shape an access-controlled install upgrading into this build has: a
	// Team folder still holding recordings the declared model does not read.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true, aclArchive: []string{"m1.opus"}}
	var logs strings.Builder
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(&logs, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatalf("substrate = %+v, want unusable: the declaration disagrees with the instance", snap)
	}
	if snap.Step != storageStepDeclaredConflict {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepDeclaredConflict)
	}
	if !strings.Contains(snap.Detail, ncACLRecordingsRoot) {
		t.Fatalf("detail %q never names the root holding the recordings", snap.Detail)
	}
	if !strings.Contains(logs.String(), "ERROR") {
		t.Fatalf("the refusal was not loud:\n%s", logs.String())
	}
	if readPersistedMode(t, path).Configured() {
		t.Fatalf("%s recorded a declaration the conflict gate rejected", storageSettingsFileName)
	}
}

// The same gate on the other conflict the spec names: the SAME recording under
// both roots. Which copy is authoritative is not a question a deploy option can
// answer.
func TestPreflightRefusesADeclaredModeOverDuplicateRecordings(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	t.Setenv(envStorageMode, storageModeAccessControlled)

	mock := &storageMock{
		serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(),
		recordingsRoot: true,
		aclArchive:     []string{"m1.opus", "m2.opus"},
		defaultArchive: []string{"m2.opus"},
	}
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.Step != storageStepDeclaredConflict {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepDeclaredConflict)
	}
	if !strings.Contains(snap.Detail, "m2.opus") {
		t.Fatalf("detail %q does not name the duplicated recording", snap.Detail)
	}
	if readPersistedMode(t, path).Configured() {
		t.Fatalf("%s recorded a declaration the conflict gate rejected", storageSettingsFileName)
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

// A misspelt deploy option leaves the install undecided and says so loudly —
// the value is the operator's typo, not a mode.
func TestPreflightIgnoresAnUnrecognisedDeclaredMode(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	ncStorage.setPath(filepath.Join(t.TempDir(), storageSettingsFileName))
	t.Setenv(envStorageMode, "acl_enabld")

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true, aclArchive: []string{"m1.opus"}}
	var logs strings.Builder
	testExAppConfig(mock.server(t).URL).preflightNCStorage(context.Background(), log.New(&logs, "", 0))

	// A typo is not a mode. It leaves the install exactly as an unset variable
	// would — it does NOT become access control because the instance happens to
	// look access-controlled, which is the inference that was removed.
	if _, resolved := ncStorage.mode(); resolved {
		t.Fatalf("a rejected value still decided a mode:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), "acl_enabld") {
		t.Fatalf("the rejected value was not named in the log:\n%s", logs.String())
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Step != storageStepModeUndecided {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepModeUndecided)
	}
}

// Deferred persist, over two runs. A declaration is written down only once BOTH
// gates have agreed: the prerequisites are there, and it does not disagree with
// what is in the two roots.
//
// The order matters more than it looks. A recorded mode is never reconsidered,
// so recording a declaration this instance cannot run would outlive every fix —
// and unlike the first pass, where the same argument applied to a fallback and
// the escape hatch was the deploy option itself, the deploy option IS what is
// being written here. Checking late costs one probe per enable until the stack
// is coherent, which for a harness is the run that would have failed anyway.
func TestPreflightRecordsADeclaredModeOnlyOnceItSurvivesTheGates(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	t.Setenv(envStorageMode, storageModeDefault)

	// Run 1: the declared mode cannot run here — no service account.
	broken := &storageMock{apps: []string{}}
	testExAppConfig(broken.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("os.Stat(%s) = %v, want the file absent — a mode the gate rejected must not be recorded", storageSettingsFileName, err)
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Step != storageStepServiceAccount {
		t.Fatalf("step = %q, want %q — the prerequisite is the better message, so it is checked first", snap.Step, storageStepServiceAccount)
	}
	// The mode still governs this process, so the Setup tab can describe it.
	if _, source := ncStorage.snapshot(); source != storageModeSourceEnv {
		t.Fatalf("source = %q, want %q", source, storageModeSourceEnv)
	}

	// Run 2: the administrator created the account. Now it records.
	resetSubstrateRecord(t)
	fixed := &storageMock{apps: []string{}, serviceAccount: true}
	testExAppConfig(fixed.server(t).URL).preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

	settings := readPersistedMode(t, path)
	if !settings.Configured() || settings.AccessControlled() {
		t.Fatalf("%s = %+v, want a recorded default once the gates passed", storageSettingsFileName, settings)
	}
	if settings.Source != storageModeSourceEnv {
		t.Fatalf("recorded source = %q, want %q", settings.Source, storageModeSourceEnv)
	}
}

// An undecided install is not stuck: the Setup tab's switch is what decides, and
// it decides from a state where nothing is recorded at all.
//
// This is the recovery path an access-controlled install upgrading into this
// build walks. The first pass walked it by setting a deploy option, because the
// fallback had already recorded a mode that shadowed everything else; there is
// no fallback to shadow anything now.
func TestAnUndecidedInstanceIsDecidedByTheSwitch(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	resetStorageMode(t)
	path := filepath.Join(t.TempDir(), storageSettingsFileName)
	ncStorage.setPath(path)
	mock := &storageMock{
		serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(),
		recordingsRoot: true, aclArchive: []string{"m1.opus", "m2.opus"},
		dirs: map[string][]string{
			ncACLRecordingsRoot:                   {"meetings"},
			ncACLRecordingsRoot + "/meetings":     {"m1.opus", "m2.opus"},
			ncDefaultRecordingsRoot:               {"meetings"},
			ncDefaultRecordingsRoot + "/meetings": {},
		},
	}
	cfg := testExAppConfig(mock.server(t).URL)

	// Run 1: nothing recorded, nothing declared. Undecided, and nothing written.
	cfg.preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Step != storageStepModeUndecided {
		t.Fatalf("step = %q, want %q", snap.Step, storageStepModeUndecided)
	}

	// The administrator picks access control in the Setup tab.
	if _, err := cfg.switchStorageMode(context.Background(), true, defaultStorageMigrationPolicy(), log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("switchStorageMode() error = %v", err)
	}

	if accessControlled, resolved := ncStorage.mode(); !resolved || !accessControlled {
		t.Fatalf("mode() = (%t, %t), want access control chosen", accessControlled, resolved)
	}
	settings := readPersistedMode(t, path)
	if settings.Source != storageModeSourceUser || !settings.Confirmed() {
		t.Fatalf("recorded source = %q (confirmed %t), want %q", settings.Source, settings.Confirmed(), storageModeSourceUser)
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !snap.OK {
		t.Fatalf("substrate = %+v, want usable once a mode has been chosen", snap)
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
			// Which is what makes the ACCESS-CONTROLLED model refuse: its whole
			// substrate is that folder, and a list we could not read is not
			// evidence that the folder is set up.
			if ok, _, _ := probe.accessControlReady(); ok {
				t.Fatal("access control was accepted on a folder list the probe could not read")
			}
			// And the default model refuses to WRITE, because the same list is
			// the only thing that could have said whether anything is mounted
			// over its own root.
			if probe.DefaultRootProbed {
				t.Fatal("an unreadable folder list was recorded as an answer about the default root")
			}
			if ok, step, _ := probe.sanity(false); ok {
				t.Fatal("the default mode was accepted for writing on an unreadable folder list")
			} else if step != storageStepModeMismatch+":"+storageStepDefaultRootUnknown {
				t.Fatalf("step = %q, want the unknown-root mismatch", step)
			}
			// Reading is the half that carries on: nothing legitimately mounts a
			// group folder at CassiniNoACL, and an archive that stops listing on
			// every transient OCS error is the failure this branch removed.
			resetStorageMode(t)
			ncStorage.set(false, storageModeSourceConfigured, true)
			ncAccessSubstrate.setProbe(probe)
			if !ncStorageServesAsOwner() {
				t.Fatal("the read path stopped serving the private default root on an unreadable folder list")
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
	t.Setenv(envStorageMode, storageModeDefault)
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
	if err := SaveStorageSettings(path, false, storageModeSourceEnv, true); err != nil {
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
