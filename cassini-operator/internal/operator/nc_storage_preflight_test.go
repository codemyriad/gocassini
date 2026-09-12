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
	// createsServiceAccount makes `POST /cloud/users` actually work, the way an
	// instance that has not adopted Nextcloud 34.0.2's password confirmation
	// answers it. Off by default, because the refusal is the expected answer on
	// a current Nextcloud and every other test here is about an instance whose
	// shape does not change under it (D-754).
	createsServiceAccount bool
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
		case r.Method == http.MethodPost && p == "/ocs/v2.php/cloud/users" && m.createsServiceAccount:
			// The account exists from here on, which is the whole point: every
			// later read in the same preflight run has to see it, including the
			// two archive PROPFINDs that are made AS it.
			m.mu.Lock()
			m.serviceAccount = true
			m.mu.Unlock()
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
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

// setDeliveredRecordings answers the probe's "has this install published
// anything" question with a fixed number, so a test can be an install with a
// past or an install without one. A non-nil err is a count nobody could take,
// which is a different answer from zero.
func setDeliveredRecordings(t *testing.T, count int, err error) {
	t.Helper()
	setDeliveredRecordingsCounter(func(context.Context) (int, error) { return count, err })
	t.Cleanup(func() { setDeliveredRecordingsCounter(nil) })
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

// sawMethod reports whether the instance was asked to do something of this
// kind at all, whatever the path. The resolution's whole claim is that it moves
// nothing, and COPY/MOVE/DELETE are the three verbs that could.
func (m *storageMock) sawMethod(method string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reqs {
		if strings.HasPrefix(r, method+" ") {
			return true
		}
	}
	return false
}

// The enable-time resolution table (D-753), one case per row.
//
// An install that has recorded no mode is no longer UNDECIDED — a state in
// which D-708 refused every recording on the instance until an administrator
// answered the setup wizard. The enabled edge reads the archive that is already
// there and answers for them, under one rule: Cassini never widens an existing
// archive on its own. Each row either keeps the audience the recordings already
// have or starts an empty archive open, and every one of them ADOPTS — the
// choice is recorded, nothing is moved.
func TestPreflightResolvesTheStorageModeOnEnable(t *testing.T) {
	completeACLInstance := func(aclArchive, defaultArchive []string) *storageMock {
		return &storageMock{
			serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(),
			recordingsRoot: true, aclArchive: aclArchive, defaultArchive: defaultArchive,
		}
	}
	cases := []struct {
		name         string
		mock         *storageMock
		setup        func(t *testing.T, path string)
		wantMode     string
		wantSource   string
		wantStranded int
		wantOK       bool
		wantRefusal  bool
	}{{
		// Row 1, as a fresh install actually arrives: no apps, no account,
		// nothing anywhere. The mode is resolved and recorded at once; the
		// account is the one prerequisite still missing, and it is what the
		// status names and what refuses a recording until it exists.
		name:        "nothing anywhere",
		mock:        &storageMock{apps: []string{}},
		wantMode:    storageModeDefault,
		wantSource:  storageModeSourceResolved,
		wantRefusal: true,
	}, {
		// Row 1 again, on an instance that has the whole access-controlled
		// substrate and has never used it — which is also what a completed
		// opt-out leaves behind, the emptied Team folder still mounted.
		name:       "nothing anywhere, with an empty Team folder mounted",
		mock:       completeACLInstance(nil, nil),
		wantMode:   storageModeDefault,
		wantSource: storageModeSourceResolved,
		wantOK:     true,
	}, {
		// Row 2. The upgrade this whole phase exists for: adopted silently, no
		// dialog, nothing moved.
		name:       "recordings in the Team folder only",
		mock:       completeACLInstance([]string{"m1.opus", "m2.opus"}, nil),
		wantMode:   storageModeAccessControlled,
		wantSource: storageModeSourceResolved,
		wantOK:     true,
	}, {
		// Row 3. Access control wins, and the recordings in the OTHER root are
		// reported through the fields that already exist for it rather than
		// being carried across on Cassini's own initiative.
		name:         "recordings in both roots",
		mock:         completeACLInstance([]string{"m1.opus"}, []string{"d1.opus", "d2.opus"}),
		wantMode:     storageModeAccessControlled,
		wantSource:   storageModeSourceResolved,
		wantStranded: 2,
		wantOK:       true,
	}, {
		// Row 4, on the instance that shape belongs to: no Team folder, no
		// third-party apps, one service account and a private archive.
		name:       "recordings in the default root only",
		mock:       &storageMock{apps: []string{}, serviceAccount: true, defaultArchive: []string{"d1.opus"}},
		wantMode:   storageModeDefault,
		wantSource: storageModeSourceResolved,
		wantOK:     true,
	}, {
		// Row 5. A mode an older build recorded on its own: kept as it is, and
		// no longer asked about. D-708 stopped here with
		// `storage_mode_unconfirmed` and published nothing.
		name: "a mode is recorded",
		mock: completeACLInstance([]string{"m1.opus", "m2.opus"}, nil),
		setup: func(t *testing.T, path string) {
			t.Helper()
			if err := SaveStorageSettings(path, false, storageModeSourceDefault, true); err != nil {
				t.Fatalf("SaveStorageSettings() error = %v", err)
			}
		},
		wantMode:   storageModeDefault,
		wantSource: storageModeSourceDefault,
		// The recordings the recorded mode does not read are the thing an
		// administrator most needs told — the symptom is "my recordings are
		// gone" — but they are still not moved.
		wantStranded: 2,
		wantOK:       true,
	}, {
		// Row 6. The deploy option keeps its precedence over the probe: it is
		// read before anything looks at the instance, and it is recorded with
		// its own provenance once the gates agree.
		name: "CASSINI_STORAGE_MODE declared",
		mock: completeACLInstance([]string{"m1.opus"}, nil),
		setup: func(t *testing.T, _ string) {
			t.Helper()
			t.Setenv(envStorageMode, storageModeAccessControlled)
		},
		wantMode:   storageModeAccessControlled,
		wantSource: storageModeSourceEnv,
		wantOK:     true,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetProvisioningUser(t)
			resetSubstrateRecord(t)
			resetStorageMode(t)
			path := filepath.Join(t.TempDir(), storageSettingsFileName)
			ncStorage.setPath(path)
			if tc.setup != nil {
				tc.setup(t, path)
			}
			cfg := testExAppConfig(tc.mock.server(t).URL)
			cfg.preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))

			snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
			if snap.Mode != tc.wantMode || snap.ModeSource != tc.wantSource {
				t.Fatalf("mode = (%q, %q), want (%q, %q)", snap.Mode, snap.ModeSource, tc.wantMode, tc.wantSource)
			}
			// A resolved mode is a settled one: nothing asks about it, and
			// nothing branches on it having been chosen by a person.
			if !snap.ModeConfirmed {
				t.Fatalf("mode_confirmed = false for a %s mode; the resolution is the decision", tc.wantSource)
			}
			if snap.OK != tc.wantOK {
				t.Fatalf("substrate = %+v, want ok=%t", snap, tc.wantOK)
			}
			// The publish gate: a resolved mode publishes exactly like a chosen
			// one. Nothing downstream asks who decided.
			if ncAccessSubstrate.usable() != tc.wantOK {
				t.Fatalf("usable() = %t, want %t — publishing must not wait on a mode that is settled", ncAccessSubstrate.usable(), tc.wantOK)
			}
			// Recording is refused only for a prerequisite that is genuinely
			// missing. "Nobody has chosen" is not one of those any more.
			if refused := ncAccessSubstrate.recordingRefusal() != ""; refused != tc.wantRefusal {
				t.Fatalf("recordingRefusal() = %q, want refused=%t", ncAccessSubstrate.recordingRefusal(), tc.wantRefusal)
			}
			settings := readPersistedMode(t, path)
			if !settings.Configured() || settings.Mode() != tc.wantMode || settings.Source != tc.wantSource {
				t.Fatalf("%s = %+v, want mode %q from %q", storageSettingsFileName, settings, tc.wantMode, tc.wantSource)
			}
			if !settings.Clean() {
				t.Fatalf("%s reports an unfinished migration; nothing was migrated", storageSettingsFileName)
			}

			rt, cleanup := newTestRuntime(t)
			defer cleanup()
			status := cfg.storageStatus(rt, nil)
			if status.AwaitingChoice {
				t.Fatal("awaiting_choice is true after the mode was resolved; nothing is waiting for an answer")
			}
			if status.StrandedRecordings != tc.wantStranded {
				t.Fatalf("stranded = %d at %q, want %d", status.StrandedRecordings, status.StrandedRoot, tc.wantStranded)
			}
			if tc.wantStranded > 0 && status.StrandedRoot != recordingsRootFor(tc.wantMode != storageModeAccessControlled) {
				t.Fatalf("stranded root = %q, want the root the resolved mode does not read", status.StrandedRoot)
			}
			// The adopt path: the choice is recorded and the archive is left
			// exactly where it is, in BOTH roots.
			for _, verb := range []string{"COPY", "MOVE", http.MethodDelete} {
				if tc.mock.sawMethod(verb) {
					t.Fatalf("the resolution issued a %s; it must record the mode and move nothing: %v", verb, tc.mock.reqs)
				}
			}
		})
	}
}

// The row that is not in the table because it is a spelling of another one: a
// pre-split archive at `Cassini/Recordings` with no Team folder mounted is the
// DEFAULT model's archive, not the access-controlled one. The two are the same
// path and only the folder list tells them apart — so the folder question is
// answered before the archive is read, and an unanswered one resolves nothing.
func TestStorageModeFromProbeReadsTheArchiveRatherThanThePath(t *testing.T) {
	probed := func(p ncStorageProbe) ncStorageProbe {
		p.FolderProbed = true
		p.ServiceAccount = true
		p.ACLArchive.Probed = true
		p.ACLArchive.Root = ncACLRecordingsRoot
		p.DefaultArchive.Probed = true
		p.DefaultArchive.Root = ncDefaultRecordingsRoot
		return p
	}
	entries := []davEntry{{Name: "m1.opus"}}

	cases := []struct {
		name            string
		probe           ncStorageProbe
		wantOK          bool
		wantAccessCtrl  bool
		wantWhyMentions string
	}{{
		name:            "a mounted Team folder holding recordings",
		probe:           probed(ncStorageProbe{FolderPresent: true, FolderMounted: true, ACLArchive: ncArchiveFacts{Entries: entries}}),
		wantOK:          true,
		wantAccessCtrl:  true,
		wantWhyMentions: ncRecordingsMount,
	}, {
		// The same path, the same listing, no Team folder: the service
		// account's own directory, which is where a pre-split install kept its
		// default-mode archive. Adopting it as access-controlled would claim an
		// audience for it that nothing enforces.
		name:            "the same path with no Team folder mounted",
		probe:           probed(ncStorageProbe{ACLArchive: ncArchiveFacts{Entries: entries}}),
		wantOK:          true,
		wantAccessCtrl:  false,
		wantWhyMentions: ncRecordingsOwner,
	}, {
		name:            "nobody could say whether a Team folder is mounted",
		probe:           ncStorageProbe{ServiceAccount: true},
		wantOK:          false,
		wantWhyMentions: ncRecordingsMount,
	}, {
		// No service account, but a Team folder that may hold an archive: both
		// roots are read AS that account, so there is no way to find out. Fail
		// closed and record nothing; the next edge, after the account exists,
		// can answer.
		name:            "no service account and a Team folder that might hold one",
		probe:           ncStorageProbe{FolderProbed: true, FolderPresent: true},
		wantOK:          false,
		wantWhyMentions: ncRecordingsOwner,
	}, {
		name:            "no service account and no Team folder",
		probe:           ncStorageProbe{FolderProbed: true},
		wantOK:          true,
		wantAccessCtrl:  false,
		wantWhyMentions: ncRecordingsOwner,
	}, {
		// The Team-folder app is off, so an access-controlled archive and an
		// empty install answer identically: the mount is gone and both roots
		// 404. One of those two answers is an archive, and `default` would
		// strand it and widen every recording made afterwards. This operator
		// has published before, so it is the install with a past.
		name:            "the Team-folder app is off and this install has delivered recordings",
		probe:           probed(ncStorageProbe{DeliveredRecordingsProbed: true, DeliveredRecordings: 3}),
		wantOK:          false,
		wantWhyMentions: ncRecordingsMount,
	}, {
		// The same shape, with the local history that tells it apart: this
		// operator has never delivered a recording, so there is no archive for
		// an open mode to strand. A deps-free Nextcloud is the ordinary fresh
		// install (AIO ships without `groupfolders`) and must not be held up.
		name:            "the Team-folder app is off and nothing has ever been delivered",
		probe:           probed(ncStorageProbe{DeliveredRecordingsProbed: true}),
		wantOK:          true,
		wantAccessCtrl:  false,
		wantWhyMentions: ncAppGroupFolders,
	}, {
		// The other tie-breaker, on its own: Cassini made the service account on
		// this very edge. Every recording in either model is written and read as
		// that account, so one that did not exist a minute ago owns nothing, and
		// the count is not even needed.
		name:            "the Team-folder app is off and the account was created on this edge",
		probe:           probed(ncStorageProbe{ServiceAccountCreated: true}),
		wantOK:          true,
		wantAccessCtrl:  false,
		wantWhyMentions: ncRecordingsOwner,
	}, {
		// Neither tie-breaker available: the count could not be taken, and an
		// absent answer is not a zero. Fail closed.
		name:            "the Team-folder app is off and the delivery count is unknown",
		probe:           probed(ncStorageProbe{}),
		wantOK:          false,
		wantWhyMentions: ncAppGroupFolders,
	}, {
		name:            "a root that could not be listed",
		probe:           ncStorageProbe{FolderProbed: true, ServiceAccount: true, DefaultArchive: ncArchiveFacts{Probed: true}},
		wantOK:          false,
		wantWhyMentions: ncACLRecordingsRoot,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accessControlled, ok, why := storageModeFromProbe(tc.probe)
			if ok != tc.wantOK || (ok && accessControlled != tc.wantAccessCtrl) {
				t.Fatalf("storageModeFromProbe() = (%t, %t), want (%t, %t): %s", accessControlled, ok, tc.wantAccessCtrl, tc.wantOK, why)
			}
			if !strings.Contains(why, tc.wantWhyMentions) {
				t.Fatalf("why = %q, which never mentions %q — the evidence is what makes the record honest", why, tc.wantWhyMentions)
			}
		})
	}
}

// The create and the resolution happen on the SAME enabled edge (D-754, D-753),
// and both recordings roots are read AS the account being created. A probe taken
// before the create skipped them, so believing it would answer
// `storage_mode_unresolved` for the most ordinary install there is: a fresh one
// that Cassini has just made able to record.
func TestPreflightResolvesTheModeAfterCreatingTheServiceAccount(t *testing.T) {
	mock := &storageMock{createsServiceAccount: true}
	cfg, path := runStoragePreflight(t, mock, io.Discard)

	if !mock.saw(http.MethodPost, "/ocs/v2.php/cloud/users") {
		t.Fatalf("the preflight never attempted the create; requests: %v", mock.reqs)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.Mode != storageModeDefault || snap.ModeSource != storageModeSourceResolved {
		t.Fatalf("mode = (%q, %q), want (%q, %q)", snap.Mode, snap.ModeSource, storageModeDefault, storageModeSourceResolved)
	}
	if !snap.OK || snap.Step == storageStepModeUnresolved {
		t.Fatalf("substrate = %+v, want a usable install: the account it needed exists now", snap)
	}
	settings := readPersistedMode(t, path)
	if !settings.Configured() || settings.Mode() != storageModeDefault || settings.Source != storageModeSourceResolved {
		t.Fatalf("%s = %+v, want mode %q from %q", storageSettingsFileName, settings, storageModeDefault, storageModeSourceResolved)
	}
	// The dialog is the only thing left between this install and a recording,
	// and it is not a gate: nothing refuses.
	if refusal := ncAccessSubstrate.recordingRefusal(); refusal != "" {
		t.Fatalf("a recording was refused after the account was created: %q", refusal)
	}
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	if status := cfg.storageStatus(rt, nil); !status.FirstRun {
		t.Fatal("first_run is false on an install nobody has acknowledged")
	}
}

// What the count above is counting. A delivery is a publish that finished and
// succeeded: a build that completed without publishing is not one, and neither
// is a publish that failed. Getting that wrong in either direction decides who
// can read an organisation's meetings on a deps-free install.
func TestCountDeliveredRecordingsCountsOnlyDeliveries(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "jobs.sqlite3"))
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()

	if n, err := store.CountDeliveredRecordings(ctx); err != nil || n != 0 {
		t.Fatalf("CountDeliveredRecordings() on a fresh store = (%d, %v), want (0, nil)", n, err)
	}

	insertJob(t, store.db, "delivered", "2026-06-12T10:00:00Z")
	if err := store.MarkPublishSucceeded(ctx, "delivered", "/site/m.opus", "/site/m.opus", nowUTCString()); err != nil {
		t.Fatalf("MarkPublishSucceeded() error = %v", err)
	}
	insertJob(t, store.db, "built-only", "2026-06-12T11:00:00Z")
	if err := store.MarkBuildSucceeded(ctx, "built-only", "/work/m.meeting", "/work/m.meeting", nowUTCString()); err != nil {
		t.Fatalf("MarkBuildSucceeded() error = %v", err)
	}
	insertJob(t, store.db, "publish-failed", "2026-06-12T12:00:00Z")
	if err := store.MarkPublishFailed(ctx, "publish-failed", "", "", "the sink refused it", nowUTCString()); err != nil {
		t.Fatalf("MarkPublishFailed() error = %v", err)
	}

	if n, err := store.CountDeliveredRecordings(ctx); err != nil || n != 1 {
		t.Fatalf("CountDeliveredRecordings() = (%d, %v), want (1, nil): only the delivered job counts", n, err)
	}
}

// The deps-free fresh install, end to end: a Nextcloud with neither native app
// (which is what Nextcloud AIO ships), the service account already made by hand,
// and nothing in either root. The Team folder is invisible there whether or not
// one exists, so the resolution leans on this operator's own delivery history —
// and the two answers it can give must lead to different places.
func TestPreflightResolvesADepsFreeInstallFromItsOwnDeliveryHistory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		delivered  int
		wantMode   string
		wantSource string
	}{
		{name: "nothing ever delivered", delivered: 0, wantMode: storageModeDefault, wantSource: storageModeSourceResolved},
		{name: "recordings delivered before", delivered: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setDeliveredRecordings(t, tc.delivered, nil)
			mock := &storageMock{apps: []string{}, serviceAccount: true}
			_, path := runStoragePreflight(t, mock, io.Discard)

			snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
			if snap.Mode != tc.wantMode || snap.ModeSource != tc.wantSource {
				t.Fatalf("mode = (%q, %q), want (%q, %q)", snap.Mode, snap.ModeSource, tc.wantMode, tc.wantSource)
			}
			settings := readPersistedMode(t, path)
			if tc.wantMode == "" {
				if settings.Configured() {
					t.Fatalf("%s recorded %+v for an install whose Team folder could be hiding an archive", storageSettingsFileName, settings)
				}
				if snap.Step != storageStepModeUnresolved {
					t.Fatalf("step = %q, want %q", snap.Step, storageStepModeUnresolved)
				}
				// Degraded, never unavailable: a call is not spent over this.
				if refusal := ncAccessSubstrate.recordingRefusal(); refusal != "" {
					t.Fatalf("a recording was refused while the mode was unresolved: %q", refusal)
				}
				return
			}
			if !settings.Configured() || settings.Mode() != tc.wantMode || settings.Source != tc.wantSource {
				t.Fatalf("%s = %+v, want mode %q from %q", storageSettingsFileName, settings, tc.wantMode, tc.wantSource)
			}
			if !snap.OK {
				t.Fatalf("substrate = %+v, want a usable deps-free install", snap)
			}
		})
	}
}

// A probe that could not read the archive records NOTHING, and says so as a
// degradation rather than as a missing prerequisite: nothing is absent, an
// answer simply did not arrive. Recording goes ahead — the alternative is
// losing a call to a WebDAV timeout — and publishing waits.
func TestPreflightRecordsNoModeWhenTheProbeCannotAnswer(t *testing.T) {
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), failPropfindAll: true}
	cfg, path := runStoragePreflight(t, mock, io.Discard)

	if _, resolved := ncStorage.mode(); resolved {
		t.Fatal("a mode was resolved from half the evidence")
	}
	if readPersistedMode(t, path).Configured() {
		t.Fatalf("%s recorded a mode the probe could not establish", storageSettingsFileName)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK || snap.State != string(ncSubstrateDegraded) || snap.Step != storageStepModeUnresolved {
		t.Fatalf("substrate = %+v, want degraded/%s", snap, storageStepModeUnresolved)
	}
	if ncAccessSubstrate.recordingRefusal() != "" {
		t.Fatalf("a recording was refused because a PROPFIND failed: %q", ncAccessSubstrate.recordingRefusal())
	}
	rt, cleanup := newTestRuntime(t)
	defer cleanup()
	if cfg.storageStatus(rt, nil).AwaitingChoice {
		t.Fatal("awaiting_choice is true; nobody is being asked for a decision here")
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
	// Nothing was scaffolded on the administrator's behalf. The service account
	// is the one prerequisite the preflight will create (D-754) and this
	// instance already has it, so on this fixture that write must not happen
	// either: an account that is there is never written to.
	for _, forbidden := range []string{"/ocs/v2.php/cloud/users", "/ocs/v2.php/cloud/groups", "/index.php/apps/groupfolders/folders"} {
		if mock.saw(http.MethodPost, forbidden) {
			t.Errorf("the preflight POSTed to %s on an instance that needed nothing created", forbidden)
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
	// The step is the missing prerequisite, because sanity runs first and a
	// named prerequisite is the more actionable of the two things wrong here.
	// The mode itself is not a question: nothing asks an administrator to
	// confirm a file Cassini could not read, it simply keeps the safe model and
	// says so in the log (D-753).
	// The fixture has the service account and neither native app, and access
	// control is what the unreadable file keeps: the missing app is the
	// prerequisite, and it is the only one reachable here.
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !strings.HasPrefix(snap.Step, "app_missing:") {
		t.Fatalf("step = %q, want app_missing:<app>: %+v", snap.Step, snap)
	}
}

// The same file, on an instance the assumed mode CAN run: it publishes.
//
// D-708 refused here, with `storage_mode_unconfirmed`, until an administrator
// confirmed the mode in the Setup tab. A mode that is recorded is a mode that is
// settled now (D-753), whatever wrote it — including a file this operator could
// not parse, where the recorded model is the safe one by construction.
func TestPreflightPublishesUnderAModeNobodyChose(t *testing.T) {
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
	if !snap.OK || snap.Mode != storageModeAccessControlled {
		t.Fatalf("substrate = %+v, want a usable %s install", snap, storageModeAccessControlled)
	}
	if !snap.ModeConfirmed {
		t.Fatal("a mode in force was reported as one still awaiting an answer")
	}
	if ncAccessSubstrate.recordingRefusal() != "" {
		t.Fatalf("a recording was refused because nobody had confirmed the mode: %q", ncAccessSubstrate.recordingRefusal())
	}
	if !mock.saw("PROPFIND", "/"+ncACLRecordingsRoot+"/meetings") {
		t.Fatal("the probe did not read the archive the mode names")
	}
	// The unreadable file is not overwritten: writing a mode over one nobody
	// could read is how a bad byte becomes a decision.
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "{broken" {
		t.Fatalf("%s = %q (err %v), want the unreadable file untouched", storageSettingsFileName, raw, err)
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
	// And it is not asked about either (D-753): a mode an older build recorded
	// on its own still governs, and confirming it would be asking an
	// administrator to agree with a fact.
	if !snap.ModeConfirmed || snap.Step != "" {
		t.Fatalf("substrate = %+v, want a settled install with nothing outstanding", snap)
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

// A misspelt deploy option decides nothing and says so loudly — the value is
// the operator's typo, not a mode. The install is then resolved exactly as one
// that declared nothing: from the archive it already has (D-753), which is what
// this mock's mounted, populated Team folder is.
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
	// would, and the provenance recorded says so: the instance answered, the
	// deploy option did not.
	if !strings.Contains(logs.String(), "acl_enabld") {
		t.Fatalf("the rejected value was not named in the log:\n%s", logs.String())
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.ModeSource != storageModeSourceResolved {
		t.Fatalf("mode source = %q, want %q — a rejected value must not read back as a declaration:\n%s", snap.ModeSource, storageModeSourceResolved, logs.String())
	}
	if snap.Mode != storageModeAccessControlled {
		t.Fatalf("mode = %q, want the archive in the Team folder adopted", snap.Mode)
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

// The upgrade path, end to end: an access-controlled install with nothing
// recorded comes up on this build, keeps its mode, and is then left alone.
//
// The second half is the part worth pinning. The resolution records a settled,
// clean instance, so the switch an administrator can still reach — the same PUT
// the Settings section uses — treats a request for the mode already in force as
// the no-op it is. D-708 reached a CONFIRMATION there instead, because the mode
// it had resolved was not a decision yet; a second copy of an archive is not a
// thing to leave one enable edge away.
func TestAnUnrecordedInstanceIsResolvedAndThenLeftAlone(t *testing.T) {
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

	// Run 1: nothing recorded, nothing declared. The Team folder holds the
	// archive, so that is the mode, recorded on the spot.
	cfg.preflightNCStorage(context.Background(), log.New(io.Discard, "", 0))
	if accessControlled, resolved := ncStorage.mode(); !resolved || !accessControlled {
		t.Fatalf("mode() = (%t, %t), want the Team-folder archive adopted", accessControlled, resolved)
	}
	settings := readPersistedMode(t, path)
	if settings.Source != storageModeSourceResolved || !settings.Confirmed() || !settings.Clean() {
		t.Fatalf("recorded settings = %+v, want a clean %q resolution", settings, storageModeSourceResolved)
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); !snap.OK {
		t.Fatalf("substrate = %+v, want usable once the mode is resolved", snap)
	}

	// The administrator opens Settings and asks for the mode it is already in.
	result, err := cfg.switchStorageMode(context.Background(), true, true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode() error = %v", err)
	}
	if result.Mode != "" {
		t.Fatalf("transition = %+v, want the zero result: there is nothing to do", result)
	}
	if got := readPersistedMode(t, path); got.Source != storageModeSourceResolved {
		t.Fatalf("recorded source = %q after a no-op, want %q untouched", got.Source, storageModeSourceResolved)
	}
	for _, verb := range []string{"COPY", "MOVE", http.MethodDelete} {
		if mock.sawMethod(verb) {
			t.Fatalf("the upgrade issued a %s; an adopted archive is not moved: %v", verb, mock.reqs)
		}
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
