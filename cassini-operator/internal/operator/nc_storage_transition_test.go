package operator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"sync"
	"testing"
)

// transitionMock is a Nextcloud with a filesystem: it models both trees at
// once — the service account's home and the mounted Team folder — because a
// transition is entirely about which of them a path resolves to.
//
// It models the D-660 collision rule directly: while the Team folder is
// mounted, `Cassini/...` addresses the folder and the home tree that used to be
// there is at `Cassini (1)/...`. Unmapping the folder's groups un-mounts it and
// `Cassini/...` goes back to the home.
type transitionMock struct {
	mu sync.Mutex
	// files is the whole of both trees, keyed by the path relative to the
	// service account's WebDAV home.
	files map[string]string
	// dirs is the collections that exist.
	dirs map[string]bool
	// mounted is whether the Team folder is mapped to any group.
	mounted bool
	folder  *gfFolder
	// proppatched records every path an ACL was written to, in order.
	proppatched []string
	// moves records every MOVE as "from -> to". Nothing should produce one any
	// more — the transition copies — so it doubles as a regression guard.
	moves []string
	// copies records every COPY as "from -> to".
	copies []string
	// deleted records every DELETE, in order, so a test can assert that nothing
	// was removed before the mode flipped.
	deleted []string
	// unmapped records the groups removed from the Team folder. Nothing should
	// produce one any more: the opt-out leaves the folder mounted and emptied.
	unmapped []string
	// events is one ordered log of the operations whose ORDER is the safety
	// property: a recording has to leave the Team folder before the folder
	// stops being mounted, and the two are recorded in different places
	// otherwise.
	events []string
	// serviceAccount and everyoneGroup feed the probe.
	serviceAccount bool
	everyoneGroup  bool
	apps           []string
	// failPropfind makes a PROPFIND of exactly this path answer 500 — a failed
	// LOOK, which is a different answer from "there is nothing here".
	failPropfind string
	// failCopyOf makes the COPY of exactly this source path answer 507, which is
	// how a transition dies half way with the source untouched.
	failCopyOf string
}

func newTransitionMock() *transitionMock {
	return &transitionMock{
		files:          map[string]string{},
		dirs:           map[string]bool{},
		serviceAccount: true,
		everyoneGroup:  true,
	}
}

func (m *transitionMock) addDir(paths ...string) {
	for _, p := range paths {
		m.dirs[p] = true
	}
}

func (m *transitionMock) addFile(p, body string) {
	m.files[p] = body
	for dir := path.Dir(p); dir != "." && dir != "/"; dir = path.Dir(dir) {
		m.dirs[dir] = true
	}
}

func (m *transitionMock) has(p string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[p]
	return ok
}

// relOf strips the WebDAV prefix, leaving the path relative to the account's
// home — which is how every path in this mock is keyed.
func relOf(urlPath string) string {
	const prefix = "/remote.php/dav/files/"
	if !strings.HasPrefix(urlPath, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(urlPath, prefix)
	_, after, found := strings.Cut(rest, "/")
	if !found {
		return ""
	}
	return strings.Trim(after, "/")
}

func (m *transitionMock) childrenOf(dir string) []string {
	var out []string
	seen := map[string]bool{}
	collect := func(p string) {
		if dir == "" {
			if !strings.Contains(p, "/") && !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
			return
		}
		if strings.HasPrefix(p, dir+"/") {
			rest := strings.TrimPrefix(p, dir+"/")
			if !strings.Contains(rest, "/") && !seen[rest] {
				seen[rest] = true
				out = append(out, rest)
			}
		}
	}
	for p := range m.files {
		collect(p)
	}
	for p := range m.dirs {
		collect(p)
	}
	return out
}

func (m *transitionMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		m.mu.Lock()
		defer m.mu.Unlock()

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
			encoded, _ := json.Marshal(map[string]gfFolder{string(m.folder.ID): *m.folder})
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":`+string(encoded)+`}}`)
		case r.Method == http.MethodDelete && strings.HasPrefix(p, "/index.php/apps/groupfolders/folders/"):
			m.unmapped = append(m.unmapped, path.Base(p))
			m.events = append(m.events, "unmap "+path.Base(p))
			if len(m.unmapped) >= 2 {
				// Both mappings gone: the folder is no longer mounted anywhere,
				// so `Cassini` resolves to the home tree again.
				m.mounted = false
				m.folder.Groups = json.RawMessage(`{}`)
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)

		case r.Method == "MKCOL":
			m.dirs[relOf(p)] = true
			w.WriteHeader(http.StatusCreated)
		case r.Method == "PROPPATCH":
			m.proppatched = append(m.proppatched, relOf(p))
			m.events = append(m.events, "proppatch "+relOf(p))
			w.WriteHeader(http.StatusMultiStatus)
		case r.Method == "COPY":
			from := relOf(p)
			to := relOf(mustURLPath(t, r.Header.Get("Destination")))
			if r.Header.Get("Overwrite") != "F" {
				t.Errorf("COPY %s -> %s sent Overwrite: %q; only F is safe", from, to, r.Header.Get("Overwrite"))
			}
			if _, exists := m.files[to]; exists || m.dirs[to] {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			if m.failCopyOf != "" && from == m.failCopyOf {
				w.WriteHeader(http.StatusInsufficientStorage)
				return
			}
			m.copies = append(m.copies, from+" -> "+to)
			m.events = append(m.events, "copy "+from+" -> "+to)
			if body, ok := m.files[from]; ok {
				m.files[to] = body
			} else {
				for src := range m.files {
					if strings.HasPrefix(src, from+"/") {
						m.files[to+strings.TrimPrefix(src, from)] = m.files[src]
					}
				}
				for src := range m.dirs {
					if src == from || strings.HasPrefix(src, from+"/") {
						m.dirs[to+strings.TrimPrefix(src, from)] = true
					}
				}
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == "MOVE":
			from := relOf(p)
			to := relOf(mustURLPath(t, r.Header.Get("Destination")))
			if r.Header.Get("Overwrite") != "F" {
				t.Errorf("MOVE %s -> %s sent Overwrite: %q; only F is safe", from, to, r.Header.Get("Overwrite"))
			}
			if _, exists := m.files[to]; exists || m.dirs[to] {
				w.WriteHeader(http.StatusPreconditionFailed)
				return
			}
			m.moves = append(m.moves, from+" -> "+to)
			m.events = append(m.events, "move "+from+" -> "+to)
			if body, ok := m.files[from]; ok {
				delete(m.files, from)
				m.files[to] = body
			} else {
				// A directory move takes everything under it.
				for src := range m.files {
					if strings.HasPrefix(src, from+"/") {
						m.files[to+strings.TrimPrefix(src, from)] = m.files[src]
						delete(m.files, src)
					}
				}
				for src := range m.dirs {
					if src == from || strings.HasPrefix(src, from+"/") {
						m.dirs[to+strings.TrimPrefix(src, from)] = true
						delete(m.dirs, src)
					}
				}
			}
			w.WriteHeader(http.StatusCreated)
		case r.Method == "PROPFIND":
			rel := relOf(p)
			if m.failPropfind != "" && rel == m.failPropfind {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if _, isFile := m.files[rel]; !isFile && !m.dirs[rel] && rel != "" {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			var b strings.Builder
			b.WriteString(`<?xml version="1.0"?><d:multistatus xmlns:d="DAV:"><d:response><d:href>` + p + `/</d:href></d:response>`)
			for _, child := range m.childrenOf(rel) {
				fmt.Fprintf(&b, `<d:response><d:href>%s/%s</d:href></d:response>`, strings.TrimRight(p, "/"), child)
			}
			b.WriteString(`</d:multistatus>`)
			w.WriteHeader(http.StatusMultiStatus)
			io.WriteString(w, b.String())
		case r.Method == http.MethodGet:
			body, ok := m.files[relOf(p)]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			io.WriteString(w, body)
		case r.Method == http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			m.files[relOf(p)] = string(body)
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete:
			rel := relOf(p)
			m.deleted = append(m.deleted, rel)
			m.events = append(m.events, "delete "+rel)
			delete(m.files, rel)
			delete(m.dirs, rel)
			for src := range m.files {
				if strings.HasPrefix(src, rel+"/") {
					delete(m.files, src)
				}
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustURLPath(t *testing.T, rawURL string) string {
	t.Helper()
	idx := strings.Index(rawURL, "/remote.php/")
	if idx < 0 {
		t.Fatalf("Destination %q is not a WebDAV URL", rawURL)
		return ""
	}
	return rawURL[idx:]
}

func catalogWith(ids ...string) string {
	entries := make([]string, 0, len(ids))
	for _, id := range ids {
		entries = append(entries, fmt.Sprintf(`{"id":%q,"audioPath":"./meetings/%s.opus"}`, id, id))
	}
	return `{"version":"cassini.viewer.catalog.v1","meetings":[` + strings.Join(entries, ",") + `]}`
}

func idsIn(t *testing.T, raw string) []string {
	t.Helper()
	var catalog siteCatalog
	if err := json.Unmarshal([]byte(raw), &catalog); err != nil {
		t.Fatalf("catalog is not JSON: %v (%s)", err, raw)
	}
	out := make([]string, 0, len(catalog.Meetings))
	for _, entry := range catalog.Meetings {
		id, err := catalogEntryID(entry)
		if err != nil {
			t.Fatalf("catalogEntryID() error = %v", err)
		}
		out = append(out, id)
	}
	return out
}

// The opt-in: default -> access controlled. The archive is COPIED into the Team
// folder, made public there, and only then is the source emptied.
func TestOptInCopiesTheArchiveIntoTheTeamFolder(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addDir(ncRecordingsMount, ncACLRecordingsRoot, ncACLRecordingsRoot+"/meetings")
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/old-a.opus", "audio-a")
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/old-b.opus", "audio-b")
	mock.addFile(ncDefaultRecordingsRoot+"/catalog.json", catalogWith("old-a", "old-b"))

	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.switchStorageMode(context.Background(), true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode(true) error = %v", err)
	}
	if result.MeetingsMoved != 2 {
		t.Fatalf("copied %d recordings, want 2", result.MeetingsMoved)
	}
	if result.SourceRoot != ncDefaultRecordingsRoot || result.DestinationRoot != ncACLRecordingsRoot {
		t.Fatalf("roots = %q -> %q, want %q -> %q", result.SourceRoot, result.DestinationRoot, ncDefaultRecordingsRoot, ncACLRecordingsRoot)
	}
	for _, want := range []string{ncACLRecordingsRoot + "/meetings/old-a.opus", ncACLRecordingsRoot + "/meetings/old-b.opus"} {
		if !mock.has(want) {
			t.Errorf("%s never arrived in the Team folder", want)
		}
	}
	// The source is EMPTIED, not deleted: the collections survive so the switch
	// back has somewhere to copy into.
	if mock.has(ncDefaultRecordingsRoot + "/meetings/old-a.opus") {
		t.Error("the source still holds the recording after a completed switch")
	}
	mock.mu.Lock()
	dirsKept := mock.dirs[ncDefaultRecordingsRoot+"/meetings"]
	proppatched := strings.Join(mock.proppatched, "\n")
	moves := len(mock.moves)
	mock.mu.Unlock()
	if !dirsKept {
		t.Error("the source collections were deleted; the spec says clear the directory, not remove it")
	}
	if moves != 0 {
		t.Errorf("the switch issued %d MOVEs; it must copy, so a failure leaves the source complete", moves)
	}

	// Copied recordings are PUBLIC — nothing infers a historical audience.
	for _, want := range []string{ncACLRecordingsRoot + "/meetings/old-a.opus", ncACLRecordingsRoot + "/meetings/old-b.opus"} {
		if !strings.Contains(proppatched, want) {
			t.Errorf("no ACL was written onto %s after the copy:\n%s", want, proppatched)
		}
	}

	if got := idsIn(t, mock.files[ncACLRecordingsRoot+"/catalog.json"]); len(got) != 2 {
		t.Fatalf("merged catalog ids = %v, want both meetings", got)
	}
	if !result.CatalogMoved {
		t.Error("the catalog was not reported as moved")
	}
	if !result.SourceCleared {
		t.Error("the source was not reported as cleared")
	}

	persisted := readPersistedMode(t, settings)
	if !persisted.Configured() || !persisted.AccessControlled() || !persisted.Clean() {
		t.Fatalf("%s = %+v, want access_control_enabled=true and migration_clean=true", storageSettingsFileName, persisted)
	}
	if accessControlled, resolved := ncStorage.mode(); !resolved || !accessControlled {
		t.Fatalf("mode() = (%t, %t) after opting in", accessControlled, resolved)
	}
}

// The opt-out: access controlled -> default. It copies into the service
// account's own root and leaves the Team folder MOUNTED and emptied.
//
// Leaving it mounted is what removes the one call in the whole feature that
// Nextcloud refuses to an ExApp. `DELETE /folders/{id}/groups/{group}` carries
// #[PasswordConfirmationRequired], and an act-as request has a session but no
// login token — so on Nextcloud 33.0.6+ and 34.0.1+ the opt-out died there. The
// first pass had to unmap, because the mount otherwise shadowed the path the
// default model wrote to. With separate roots it does not.
func TestOptOutCopiesIntoThePrivateRootAndLeavesTheFolderMounted(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, true)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncACLRecordingsRoot+"/meetings/m1.opus", "audio-1")
	mock.addFile(ncACLRecordingsRoot+"/catalog.json", catalogWith("m1"))

	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.switchStorageMode(context.Background(), false, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode(false) error = %v", err)
	}
	if result.MeetingsMoved != 1 {
		t.Fatalf("copied %d recordings, want 1", result.MeetingsMoved)
	}
	if !mock.has(ncDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Fatalf("the recording never reached the private root; files: %v", mock.files)
	}
	if mock.has(ncACLRecordingsRoot + "/meetings/m1.opus") {
		t.Error("the Team folder was not emptied")
	}
	if ids := idsIn(t, mock.files[ncDefaultRecordingsRoot+"/catalog.json"]); len(ids) != 1 {
		t.Fatalf("catalog ids = %v, want the migrated meeting", ids)
	}

	mock.mu.Lock()
	unmapped := len(mock.unmapped)
	mounted := mock.mounted
	proppatched := strings.Join(mock.proppatched, "\n")
	mock.mu.Unlock()
	if unmapped != 0 {
		t.Errorf("the opt-out unmapped %d group(s); that call is password-confirmation guarded and is no longer needed", unmapped)
	}
	if !mounted {
		t.Error("the Team folder was unmounted; it must be left in place so opting back in is immediate")
	}
	// No rules are written on the way out. A copy into the home gets a new
	// fileid outside any group folder, so it has no rules by construction — and
	// `nc:acl-list` is not settable there anyway.
	if strings.Contains(proppatched, ncDefaultRecordingsRoot) {
		t.Errorf("an ACL was written outside the Team folder, where the property is not settable:\n%s", proppatched)
	}

	persisted := readPersistedMode(t, settings)
	if !persisted.Configured() || persisted.AccessControlled() || !persisted.Clean() {
		t.Fatalf("%s = %+v, want access_control_enabled=false and migration_clean=true", storageSettingsFileName, persisted)
	}
}

// The invariant, as an ordering assertion: nothing is REMOVED until the settings
// file names the destination. Before that instant the recorded mode still names
// the source, so a process killed anywhere in the copy leaves a complete archive
// where the recorded mode says it is.
func TestSwitchRemovesNothingBeforeTheModeFlips(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")

	cfg := testExAppConfig(mock.server(t).URL)
	// Watch the settings file: the flip is the write that names the destination.
	flipped := false
	if _, err := cfg.switchStorageMode(context.Background(), true, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("switchStorageMode(true) error = %v", err)
	}
	if persisted := readPersistedMode(t, settings); !persisted.AccessControlled() {
		t.Fatal("the mode never flipped")
	}
	flipped = true
	_ = flipped

	mock.mu.Lock()
	events := append([]string(nil), mock.events...)
	mock.mu.Unlock()
	firstDelete, lastCopy := -1, -1
	for i, event := range events {
		if strings.HasPrefix(event, "delete ") && firstDelete < 0 {
			firstDelete = i
		}
		if strings.HasPrefix(event, "copy ") {
			lastCopy = i
		}
	}
	if firstDelete >= 0 && lastCopy >= 0 && firstDelete < lastCopy {
		t.Fatalf("something was deleted before the last copy finished:\n  %s", strings.Join(events, "\n  "))
	}
}

// A copy that fails half way changes nothing an administrator can lose: the
// mode is untouched, the source still holds every recording, and the instance is
// marked unsettled so the partial copy at the destination is cleaned up.
func TestSwitchLeavesTheArchiveIntactWhenTheCopyFails(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")
	mock.failCopyOf = ncDefaultRecordingsRoot + "/meetings/m1.opus"

	cfg := testExAppConfig(mock.server(t).URL)
	if _, err := cfg.switchStorageMode(context.Background(), true, log.New(io.Discard, "", 0)); err == nil {
		t.Fatal("switchStorageMode(true) reported success while a recording could not be copied")
	}
	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("the mode flipped despite the copy failing")
	}
	if !mock.has(ncDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Fatal("the source lost the recording the copy never delivered")
	}
	persisted := readPersistedMode(t, settings)
	if persisted.AccessControlled() {
		t.Fatalf("%s = %+v, want the mode still naming the source", storageSettingsFileName, persisted)
	}
	if persisted.Clean() {
		t.Fatal("a failed switch left the instance marked settled; the partial copy would never be cleaned up")
	}
}

// Re-running a switch after a partial copy finishes it rather than failing on
// the names that are already there. `Overwrite` is never set, so a COPY onto an
// existing name is a 412 — treating that as an error would make the second
// attempt strictly worse than the first.
func TestSwitchResumesAPartialCopy(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m2.opus", "audio-2")
	// m1 already arrived on the attempt that died.
	mock.addFile(ncACLRecordingsRoot+"/meetings/m1.opus", "audio-1")

	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.switchStorageMode(context.Background(), true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode(true) error = %v", err)
	}
	if result.MeetingsMoved != 1 || result.MeetingsAlreadyThere != 1 {
		t.Fatalf("copied %d and skipped %d, want 1 and 1", result.MeetingsMoved, result.MeetingsAlreadyThere)
	}
	if !mock.has(ncACLRecordingsRoot + "/meetings/m2.opus") {
		t.Fatal("the recording that had not arrived was not copied on the re-run")
	}
}

// A transition into a mode the instance is not set up for must change nothing
// at all, and say what is missing.
func TestOptInRefusedWhenThePrerequisitesAreMissing(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock := newTransitionMock()
	mock.apps = []string{} // neither prerequisite app
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")

	cfg := testExAppConfig(mock.server(t).URL)
	_, err := cfg.switchStorageMode(context.Background(), true, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("switchStorageMode(true) succeeded on an instance with neither prerequisite app")
	}
	if !strings.Contains(err.Error(), ncAppGroupFolders) {
		t.Fatalf("error %q does not name the missing app", err)
	}
	mock.mu.Lock()
	writes := len(mock.copies) + len(mock.moves) + len(mock.deleted)
	mock.mu.Unlock()
	if writes != 0 {
		t.Fatalf("a refused transition performed %d writes", writes)
	}
	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("a refused transition changed the recorded mode")
	}
}

// An empty archive is a legitimate switch: only the mode changes.
func TestSwitchWithAnEmptyArchiveJustMakesTheTree(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, true)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.switchStorageMode(context.Background(), false, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode(false) error = %v", err)
	}
	if result.MeetingsMoved != 0 {
		t.Fatalf("copied %d recordings, want 0", result.MeetingsMoved)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.dirs[ncDefaultRecordingsRoot+"/meetings"] {
		t.Fatalf("the destination collections were not created; dirs: %v", mock.dirs)
	}
}

// The recovery. A switch that stopped after the flip leaves the recorded mode
// naming a complete archive and the OTHER root holding the original. One action
// clears it, and it is the same action whichever half failed.
func TestFinishMigrationClearsTheRootTheModeDoesNotName(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, true)
	// The state: the flip happened, the tidy-up did not.
	ncStorage.set(true, storageModeSourceConfigured, false)
	if err := SaveStorageSettings(settings, true, storageModeSourceUser, false); err != nil {
		t.Fatalf("SaveStorageSettings() error = %v", err)
	}

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncACLRecordingsRoot+"/meetings/m1.opus", "audio-1")
	mock.addFile(ncDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")
	mock.addFile(ncDefaultRecordingsRoot+"/catalog.json", catalogWith("m1"))

	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.finishMigration(context.Background(), &http.Client{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("finishMigration() error = %v", err)
	}
	if !result.SourceCleared {
		t.Fatalf("result = %+v, want the stale root cleared", result)
	}
	if mock.has(ncDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Error("the stale copy was left behind")
	}
	if !mock.has(ncACLRecordingsRoot + "/meetings/m1.opus") {
		t.Fatal("the ACTIVE archive was cleared; the mode names it, so it is the one that must survive")
	}
	if persisted := readPersistedMode(t, settings); !persisted.Clean() {
		t.Fatalf("%s = %+v, want migration_clean=true", storageSettingsFileName, persisted)
	}
	// Idempotent: a second run is a no-op rather than a second DELETE pass.
	mock.mu.Lock()
	before := len(mock.deleted)
	mock.mu.Unlock()
	if _, err := cfg.finishMigration(context.Background(), &http.Client{}, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("finishMigration() on a settled instance error = %v", err)
	}
	mock.mu.Lock()
	after := len(mock.deleted)
	mock.mu.Unlock()
	if after != before {
		t.Fatalf("a second finishMigration deleted %d more paths", after-before)
	}
}

// Never delete the only copy. The one state where the invariant genuinely does
// not hold is a pre-split adoption that has not finished carrying an archive
// across — there the ACTIVE root is the partial one, and clearing the stale root
// would lose recordings. The verification refuses and says where they are.
func TestFinishMigrationRefusesToClearTheOnlyCopy(t *testing.T) {
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
	// The stale root holds a recording the active one does not.
	mock.addFile(ncACLRecordingsRoot+"/meetings/only.opus", "audio")
	mock.addDir(ncDefaultRecordingsRoot, ncDefaultRecordingsRoot+"/meetings")

	cfg := testExAppConfig(mock.server(t).URL)
	_, err := cfg.finishMigration(context.Background(), &http.Client{}, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("finishMigration() cleared a root holding the only copy of a recording")
	}
	if !strings.Contains(err.Error(), "only.opus") {
		t.Fatalf("error %q does not name the recording it refused to remove", err)
	}
	if !mock.has(ncACLRecordingsRoot + "/meetings/only.opus") {
		t.Fatal("the recording was deleted despite the refusal")
	}
}

func TestDavPropfindChildrenExcludesTheCollectionItself(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusMultiStatus)
		io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:">`+
			`<d:response><d:href>/remote.php/dav/files/cassini/</d:href></d:response>`+
			`<d:response><d:href>/remote.php/dav/files/cassini/Cassini%20(1)/</d:href></d:response>`+
			`<d:response><d:href>/remote.php/dav/files/cassini/Documents/</d:href></d:response>`+
			`</d:multistatus>`)
	}))
	defer srv.Close()

	names, visible, err := testExAppConfig(srv.URL).davPropfindChildren(context.Background(), srv.Client(), ncRecordingsOwner, "")
	if err != nil {
		t.Fatalf("davPropfindChildren() error = %v", err)
	}
	if !visible {
		t.Fatal("the home root was reported as absent")
	}
	if len(names) != 2 {
		t.Fatalf("children = %v, want the two directories without the collection itself", names)
	}
	if names[0] != "Cassini (1)" {
		t.Fatalf("children[0] = %q, want the percent-decoded name", names[0])
	}
	if !ncCollisionSuffix.MatchString(names[0]) {
		t.Fatalf("%q is not recognised as a server-renamed collision", names[0])
	}
	for _, notCollision := range []string{ncRecordingsMount, "Cassini (x)", "CassiniX (1)", ncDefaultRecordingsMount} {
		if ncCollisionSuffix.MatchString(notCollision) {
			t.Errorf("%q was mistaken for a server-renamed collision", notCollision)
		}
	}
}

// The preview must describe the switch without performing any of it. This is
// the property that makes it safe to run from a confirmation dialog.
func TestTransitionPreviewWritesNothing(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), recordingsRoot: true}
	cfg := testExAppConfig(mock.server(t).URL)

	if _, err := cfg.previewStorageModeSwitch(context.Background(), true, log.New(io.Discard, "", 0)); err != nil {
		t.Fatalf("previewStorageModeSwitch() error = %v", err)
	}

	for _, method := range []string{"MOVE", "COPY", "MKCOL", "PROPPATCH", http.MethodPut, http.MethodDelete} {
		mock.mu.Lock()
		reqs := append([]string(nil), mock.reqs...)
		mock.mu.Unlock()
		for _, r := range reqs {
			if strings.HasPrefix(r, method+" ") {
				t.Errorf("the preview issued %s — it must only read", r)
			}
		}
	}
}

// THE QA BUG. Five recordings in a healthy default-mode install, and the
// confirmation dialog said none would move.
//
// The cause was discovery: the preview asked findStrandedRecordingsRoot where
// the archive was, and that function recognises a server-renamed `Cassini (N)`
// and a staging directory — not the ordinary archive sitting exactly where the
// default mode puts it. It answered "there is none", the count was skipped
// entirely, `Meetings` kept its zero value, and the dialog rendered "there are
// no published recordings to move" while the switch went on to move all five.
//
// With one root per model there is nothing to discover.
func TestTransitionPreviewCountsAHealthyDefaultArchive(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder()}
	mock.homeChildren = []string{ncRecordingsMount, ncDefaultRecordingsMount}
	mock.dirs = map[string][]string{
		ncDefaultRecordingsRoot:               {"meetings", "catalog.json"},
		ncDefaultRecordingsRoot + "/meetings": {"a.opus", "b.opus", "c.opus", "d.opus", "e.opus"},
		ncACLRecordingsRoot:                   {"meetings"},
		ncACLRecordingsRoot + "/meetings":     {},
	}
	cfg := testExAppConfig(mock.server(t).URL)

	got, err := cfg.previewStorageModeSwitch(context.Background(), true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("previewStorageModeSwitch() error = %v", err)
	}
	if got.SourceRoot != ncDefaultRecordingsRoot || got.DestinationRoot != ncACLRecordingsRoot {
		t.Fatalf("roots = %q -> %q, want %q -> %q", got.SourceRoot, got.DestinationRoot, ncDefaultRecordingsRoot, ncACLRecordingsRoot)
	}
	if got.Meetings != 5 || !got.CatalogPresent {
		t.Fatalf("preview = %+v, want 5 meetings and a catalog", got)
	}
	if got.NothingToMove {
		t.Fatal("reported nothing to move with five recordings to move")
	}
	if !got.SourceReadable {
		t.Fatal("the source was reported unreadable when it was read")
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "readable by every account") {
		t.Errorf("warnings never state the audience change, which is the irreversible part:\n%s", strings.Join(got.Warnings, "\n"))
	}
}

// The opt-out preview, which the first pass never tested and got backwards: it
// hard-coded the source and destination and ignored the mount, so on a resumed
// opt-out it printed the two roots the wrong way round.
func TestTransitionPreviewReportsTheOptOutRootsInTheRightDirection(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, true)

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder()}
	mock.homeChildren = []string{ncRecordingsMount}
	mock.dirs = map[string][]string{
		ncACLRecordingsRoot:               {"meetings", "catalog.json"},
		ncACLRecordingsRoot + "/meetings": {"a.opus", "b.opus"},
	}
	cfg := testExAppConfig(mock.server(t).URL)

	got, err := cfg.previewStorageModeSwitch(context.Background(), false, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("previewStorageModeSwitch() error = %v", err)
	}
	if got.SourceRoot != ncACLRecordingsRoot || got.DestinationRoot != ncDefaultRecordingsRoot {
		t.Fatalf("roots = %q -> %q, want %q -> %q", got.SourceRoot, got.DestinationRoot, ncACLRecordingsRoot, ncDefaultRecordingsRoot)
	}
	if got.Meetings != 2 {
		t.Fatalf("preview = %+v, want the 2 recordings in the Team folder", got)
	}
	joined := strings.Join(got.Warnings, "\n")
	if !strings.Contains(joined, "lose their access rules") {
		t.Errorf("warnings never state that the rules are dropped:\n%s", joined)
	}
	if !strings.Contains(joined, "left in place") {
		t.Errorf("warnings never say the Team folder survives, emptied:\n%s", joined)
	}
}

// "We could not look" must never render as "there is nothing to move". That is
// the same failure QA reported, arriving by a different route, and it is why the
// count carries a readability flag rather than a bare zero.
func TestTransitionPreviewSaysWhenItCouldNotReadTheSource(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	// A modelled filesystem in which the source tree answers 404 for its
	// meetings collection but the PROPFIND of the root itself fails.
	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder(), failPropfindAll: true}
	cfg := testExAppConfig(mock.server(t).URL)

	got, err := cfg.previewStorageModeSwitch(context.Background(), true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("previewStorageModeSwitch() error = %v", err)
	}
	if got.SourceReadable {
		t.Fatal("an unreadable source was reported as read")
	}
	if got.NothingToMove {
		t.Fatal("an unreadable source rendered as \"nothing to move\" — the exact shape QA reported")
	}
	if !strings.Contains(strings.Join(got.Warnings, "\n"), "could not read") {
		t.Fatalf("warnings do not say the source could not be read: %v", got.Warnings)
	}
}

// A switch with genuinely nothing to move says so, because "moved 0 recordings"
// and "moved 41 recordings" need different confirmation copy.
func TestTransitionPreviewSaysWhenThereIsNothingToMove(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock := &storageMock{serviceAccount: true, everyoneGroup: true, folder: mappedCassiniFolder()}
	mock.homeChildren = []string{}
	mock.dirs = map[string][]string{
		ncDefaultRecordingsRoot:               {"meetings"},
		ncDefaultRecordingsRoot + "/meetings": {},
	}
	cfg := testExAppConfig(mock.server(t).URL)

	got, err := cfg.previewStorageModeSwitch(context.Background(), true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("previewStorageModeSwitch() error = %v", err)
	}
	if !got.NothingToMove || got.Meetings != 0 {
		t.Fatalf("preview = %+v, want nothing to move", got)
	}
	if !got.SourceReadable {
		t.Fatal("an empty-but-readable source must be distinguishable from one nobody could look at")
	}
}

// A target the instance cannot support is reported as not-ready with the reason,
// rather than as a diff the administrator could confirm.
func TestTransitionPreviewReportsAnUnsupportedTarget(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock := &storageMock{apps: []string{}, serviceAccount: true}
	cfg := testExAppConfig(mock.server(t).URL)

	got, err := cfg.previewStorageModeSwitch(context.Background(), true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("previewStorageModeSwitch() error = %v", err)
	}
	if got.Ready {
		t.Fatal("reported ready to switch into access control with neither app installed")
	}
	if got.Step == "" || got.Detail == "" {
		t.Fatalf("preview = %+v, want the blocker named", got)
	}
}

// --- Carrying a pre-split archive across ---------------------------------------
//
// Every install built by the first pass keeps its default-mode recordings at
// `Cassini/Recordings`, or — if a Team folder was ever created — at whatever
// `Cassini (N)` the server renamed that tree to. Splitting the roots would
// strand them, so the enabled edge carries them into `CassiniNoACL/Recordings`.

func adoptionMock(t *testing.T) (*transitionMock, ExAppConfig) {
	t.Helper()
	mock := newTransitionMock()
	return mock, testExAppConfig(mock.server(t).URL)
}

func runAdoption(t *testing.T, mock *transitionMock, cfg ExAppConfig) {
	t.Helper()
	probe, err := cfg.probeNCStorage(context.Background(), &http.Client{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("probeNCStorage() error = %v", err)
	}
	cfg.adoptLegacyDefaultArchive(context.Background(), &http.Client{}, probe, log.New(io.Discard, "", 0))
}

// The ordinary upgrade: no Team folder was ever created, so the pre-split
// archive is sitting exactly where it always was.
func TestAdoptionCarriesTheCanonicalPreSplitArchiveAcross(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock, cfg := adoptionMock(t)
	mock.addFile(ncLegacyDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")
	mock.addFile(ncLegacyDefaultRecordingsRoot+"/catalog.json", catalogWith("m1"))
	runAdoption(t, mock, cfg)

	if !mock.has(ncDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Fatalf("the pre-split recording was not carried across; files: %v", mock.files)
	}
	if mock.has(ncLegacyDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Error("the pre-split source was not emptied")
	}
	if ids := idsIn(t, mock.files[ncDefaultRecordingsRoot+"/catalog.json"]); len(ids) != 1 {
		t.Fatalf("catalog ids = %v, want the carried meeting", ids)
	}
	// And a second enable is a no-op: the source is the state, so once it is
	// empty there is nothing to carry.
	mock.mu.Lock()
	before := len(mock.copies)
	mock.mu.Unlock()
	runAdoption(t, mock, cfg)
	mock.mu.Lock()
	after := len(mock.copies)
	mock.mu.Unlock()
	if after != before {
		t.Fatalf("a second adoption copied %d more files", after-before)
	}
}

// The install that already collided: a Team folder took `Cassini`, so the server
// renamed the private tree to `Cassini (1)` and the recordings are in there.
func TestAdoptionCarriesAServerRenamedArchiveAcross(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock, cfg := adoptionMock(t)
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile("Cassini (1)/Recordings/meetings/m1.opus", "audio-1")
	runAdoption(t, mock, cfg)

	if !mock.has(ncDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Fatalf("the server-renamed archive was not carried across; files: %v", mock.files)
	}
}

// A first-pass opt-out that died between unmapping the folder and carrying the
// archive back left it under the staging name. That is a pre-split default
// archive too.
func TestAdoptionCarriesAFirstPassStagingTreeAcross(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock, cfg := adoptionMock(t)
	mock.addFile(ncStorageStagingRoot+"/Recordings/meetings/m1.opus", "audio-1")
	runAdoption(t, mock, cfg)

	if !mock.has(ncDefaultRecordingsRoot + "/meetings/m1.opus") {
		t.Fatalf("the abandoned staging tree was not carried across; files: %v", mock.files)
	}
}

// The one thing it must never do. A MOUNTED `Cassini` is not a stranded default
// archive — it is the access-controlled model, and copying it into a private
// home tree would be a silent mode change that also strips every recording's
// audience.
func TestAdoptionRefusesToTakeAMountedTeamFoldersArchive(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, false)

	mock, cfg := adoptionMock(t)
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile(ncACLRecordingsRoot+"/meetings/private.opus", "audio")
	runAdoption(t, mock, cfg)

	if mock.has(ncDefaultRecordingsRoot + "/meetings/private.opus") {
		t.Fatal("an access-controlled archive was copied into the private default root, dropping every audience")
	}
	if !mock.has(ncACLRecordingsRoot + "/meetings/private.opus") {
		t.Fatal("the access-controlled archive was disturbed")
	}
}

// The adoption deliberately does NOT mark the instance dirty, and that is a
// safety property rather than an omission: a mode switch flips which root is
// authoritative, an adoption cannot, so during one the ACTIVE root is the
// incomplete one. Marking it dirty would arm finishMigration against the very
// tree still holding the recordings.
func TestAdoptionLeavesTheInstanceSettled(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)

	mock, cfg := adoptionMock(t)
	mock.addFile(ncLegacyDefaultRecordingsRoot+"/meetings/m1.opus", "audio-1")
	runAdoption(t, mock, cfg)

	if !ncStorage.migrationClean() {
		t.Fatal("the adoption marked the instance unsettled; finishMigration would then clear the tree it is reading from")
	}
	if persisted := readPersistedMode(t, settings); !persisted.Clean() {
		t.Fatalf("%s = %+v, want migration_clean untouched", storageSettingsFileName, persisted)
	}
}
