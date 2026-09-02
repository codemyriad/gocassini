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
	// moves records every MOVE as "from -> to".
	moves []string
	// unmapped records the groups removed from the Team folder.
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

// The opt-in. The administrator has already created the Team folder, so the
// server has already renamed the private tree to `Cassini (1)` — the archive
// has to be FOUND there, not looked for where it used to be.
func TestOptInMovesAStrandedArchiveIntoTheTeamFolder(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, false)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addDir(ncRecordingsMount, ncRecordingsRoot, ncRecordingsRoot+"/meetings")
	mock.addFile("Cassini (1)/Recordings/meetings/old-a.opus", "audio-a")
	mock.addFile("Cassini (1)/Recordings/meetings/old-b.opus", "audio-b")
	mock.addFile("Cassini (1)/Recordings/catalog.json", catalogWith("old-a", "old-b"))

	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.switchStorageMode(context.Background(), true, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode(true) error = %v", err)
	}
	if result.MeetingsMoved != 2 {
		t.Fatalf("moved %d recordings, want 2", result.MeetingsMoved)
	}
	if result.SourceRoot != "Cassini (1)/Recordings" {
		t.Fatalf("source root = %q, want the stranded tree the server renamed", result.SourceRoot)
	}
	for _, want := range []string{"Cassini/Recordings/meetings/old-a.opus", "Cassini/Recordings/meetings/old-b.opus"} {
		if !mock.has(want) {
			t.Errorf("%s never arrived in the Team folder", want)
		}
	}
	if mock.has("Cassini (1)/Recordings/meetings/old-a.opus") {
		t.Error("the recording is still in the stranded tree as well as the Team folder")
	}

	// Moved recordings are PUBLIC — the first pass does not infer an audience.
	mock.mu.Lock()
	proppatched := strings.Join(mock.proppatched, "\n")
	mock.mu.Unlock()
	for _, want := range []string{"Cassini/Recordings/meetings/old-a.opus", "Cassini/Recordings/meetings/old-b.opus"} {
		if !strings.Contains(proppatched, want) {
			t.Errorf("no ACL was written onto %s after the move:\n%s", want, proppatched)
		}
	}

	if got := idsIn(t, mock.files["Cassini/Recordings/catalog.json"]); len(got) != 2 {
		t.Fatalf("merged catalog ids = %v, want both meetings", got)
	}
	if !result.CatalogMoved {
		t.Error("the catalog was not reported as moved")
	}

	persisted := readPersistedMode(t, settings)
	if !persisted.Configured() || !persisted.AccessControlled() {
		t.Fatalf("%s = %+v, want access_control_enabled=true", storageSettingsFileName, persisted)
	}
	if accessControlled, resolved := ncStorage.mode(); !resolved || !accessControlled {
		t.Fatalf("mode() = (%t, %t) after opting in", accessControlled, resolved)
	}
}

// The opt-out. Recordings have to leave the Team folder BEFORE its groups are
// unmapped, because unmapping it removes the mount from the very account doing
// the moving.
func TestOptOutMovesTheArchiveBackAndUnmapsTheFolder(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	settings := setStorageMode(t, true)

	mock := newTransitionMock()
	mock.folder = mappedCassiniFolder()
	mock.mounted = true
	mock.addFile("Cassini/Recordings/meetings/m1.opus", "audio-1")
	mock.addFile("Cassini/Recordings/catalog.json", catalogWith("m1"))

	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.switchStorageMode(context.Background(), false, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode(false) error = %v", err)
	}
	if result.MeetingsMoved != 1 {
		t.Fatalf("moved %d recordings, want 1", result.MeetingsMoved)
	}
	if !mock.has("Cassini/Recordings/meetings/m1.opus") {
		t.Fatalf("the recording is not at the canonical path afterwards; files: %v", mock.files)
	}
	if mock.has(ncStorageStagingRoot + "/Recordings/meetings/m1.opus") {
		t.Error("the archive was left in the staging tree")
	}

	mock.mu.Lock()
	unmapped := strings.Join(mock.unmapped, ",")
	events := mock.events
	mock.mu.Unlock()
	for _, group := range []string{ncRecordingsEveryoneGroup, ncRecordingsOwnerGroup} {
		if !strings.Contains(unmapped, group) {
			t.Errorf("the %q mapping was left on the Team folder (unmapped: %s)", group, unmapped)
		}
	}

	// The order IS the safety property. The recording is made public and
	// carried out of the Team folder while the folder is still mounted; only
	// then are its groups unmapped; only then does it come back to the
	// canonical path. Unmapping first would take the mount away from the very
	// account doing the moving.
	want := []string{
		"proppatch Cassini/Recordings/meetings/m1.opus",
		"move Cassini/Recordings/meetings/m1.opus -> " + ncStorageStagingRoot + "/Recordings/meetings/m1.opus",
		"unmap " + ncRecordingsEveryoneGroup,
		"unmap " + ncRecordingsOwnerGroup,
		"move " + ncStorageStagingRoot + "/Recordings/meetings/m1.opus -> Cassini/Recordings/meetings/m1.opus",
	}
	at := 0
	for _, event := range events {
		if at < len(want) && event == want[at] {
			at++
		}
	}
	if at != len(want) {
		t.Fatalf("the opt-out did not do %q in order.\nwanted, in order:\n  %s\ngot:\n  %s",
			want[at], strings.Join(want, "\n  "), strings.Join(events, "\n  "))
	}

	persisted := readPersistedMode(t, settings)
	if !persisted.Configured() || persisted.AccessControlled() {
		t.Fatalf("%s = %+v, want access_control_enabled=false", storageSettingsFileName, persisted)
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
	mock.addFile(ncRecordingsRoot+"/meetings/m1.opus", "audio-1")

	cfg := testExAppConfig(mock.server(t).URL)
	_, err := cfg.switchStorageMode(context.Background(), true, log.New(io.Discard, "", 0))
	if err == nil {
		t.Fatal("switchStorageMode(true) succeeded on an instance with neither prerequisite app")
	}
	if !strings.Contains(err.Error(), ncAppGroupFolders) {
		t.Fatalf("error %q does not name the missing app", err)
	}
	mock.mu.Lock()
	moves := len(mock.moves)
	mock.mu.Unlock()
	if moves != 0 {
		t.Fatalf("a refused transition moved %d files", moves)
	}
	if accessControlled, _ := ncStorage.mode(); accessControlled {
		t.Fatal("a refused transition changed the recorded mode")
	}
}

// Nothing to move is a legitimate outcome, not a failure: opting out of a mode
// whose Team folder was never mounted only has to make the tree.
func TestOptOutWithNoMountedFolderJustMakesTheTree(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	setStorageMode(t, true)

	mock := newTransitionMock()
	cfg := testExAppConfig(mock.server(t).URL)
	result, err := cfg.switchStorageMode(context.Background(), false, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("switchStorageMode(false) error = %v", err)
	}
	if result.MeetingsMoved != 0 {
		t.Fatalf("moved %d recordings, want 0", result.MeetingsMoved)
	}
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if !mock.dirs[ncRecordingsRoot+"/meetings"] {
		t.Fatalf("the canonical collections were not created; dirs: %v", mock.dirs)
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
	for _, notCollision := range []string{ncRecordingsMount, "Cassini (x)", "CassiniX (1)"} {
		if ncCollisionSuffix.MatchString(notCollision) {
			t.Errorf("%q was mistaken for a server-renamed collision", notCollision)
		}
	}
}
