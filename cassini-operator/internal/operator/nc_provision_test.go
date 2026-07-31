package operator

import (
	"context"
	"encoding/base64"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestFirstPathSegmentAndMount(t *testing.T) {
	for in, want := range map[string]string{
		"Cassini/Recordings":  "Cassini",
		"/Cassini/Recordings": "Cassini",
		"Cassini":             "Cassini",
		"a/b/c":               "a",
	} {
		if got := firstPathSegment(in); got != want {
			t.Errorf("firstPathSegment(%q) = %q, want %q", in, got, want)
		}
	}
	if ncRecordingsMount != "Cassini" {
		t.Errorf("ncRecordingsMount = %q, want Cassini", ncRecordingsMount)
	}
}

func TestContainerACLRules(t *testing.T) {
	rules := containerACLRules()
	if len(rules) != 2 {
		t.Fatalf("containerACLRules = %+v, want owner + viewer group", rules)
	}
	if rules[0].Type != "user" || rules[0].ID != ncRecordingsOwner || rules[0].Permissions != aclMaskAll {
		t.Errorf("owner rule = %+v, want full-permission owner user", rules[0])
	}
	if rules[1].Type != "group" || rules[1].ID != ncRecordingsViewerGroup || rules[1].Permissions != aclPermRead {
		t.Errorf("viewer rule = %+v, want read-only viewer group", rules[1])
	}
}

func TestParseOCSUserListAndStatusCode(t *testing.T) {
	users, err := parseOCSUserList([]byte(`{"ocs":{"meta":{"statuscode":200},"data":{"users":["a","b"]}}}`))
	if err != nil || len(users) != 2 || users[0] != "a" || users[1] != "b" {
		t.Fatalf("parseOCSUserList = %v, %v", users, err)
	}
	if code := ocsStatusCode([]byte(`{"ocs":{"meta":{"statuscode":102,"message":"group exists"}}}`)); code != 102 {
		t.Errorf("ocsStatusCode = %d, want 102", code)
	}
	if code := ocsStatusCode([]byte(`not json`)); code != -1 {
		t.Errorf("ocsStatusCode(garbage) = %d, want -1", code)
	}
}

func TestWithFormatJSONAndBoolForm(t *testing.T) {
	if got := withFormatJSON("http://x/y"); got != "http://x/y?format=json" {
		t.Errorf("withFormatJSON no-query = %q", got)
	}
	if got := withFormatJSON("http://x/y?limit=1"); got != "http://x/y?limit=1&format=json" {
		t.Errorf("withFormatJSON with-query = %q", got)
	}
	if boolForm(true) != "1" || boolForm(false) != "0" {
		t.Errorf("boolForm mismatch")
	}
}

type recordedReq struct {
	method string
	path   string
	auth   string
	body   string
}

// provisionMock is a fake Nextcloud that records requests and serves the OCS /
// Group Folders responses the provisioner depends on. `folders` seeds the
// GET /folders reply so a test can start with or without an existing folder.
type provisionMock struct {
	mu      sync.Mutex
	reqs    []recordedReq
	folders string // ocs.data body for GET /folders
	members string // ocs.data.users for the viewer group
	users   string // ocs.data.users for GET /cloud/users
	// adminList is ocs.data.users for GET /cloud/groups/admin — who the
	// operator discovers as an administrator to act as. Falls back to members.
	adminList string
	// userExists makes POST /cloud/users answer "user already exists" (102),
	// as it does on every enable after the first.
	userExists bool
}

func (m *provisionMock) record(r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	m.mu.Lock()
	m.reqs = append(m.reqs, recordedReq{method: r.Method, path: r.URL.Path, auth: r.Header.Get("AUTHORIZATION-APP-API"), body: string(body)})
	m.mu.Unlock()
}

func (m *provisionMock) find(method, pathSuffix string) (recordedReq, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reqs {
		if r.method == method && strings.HasSuffix(r.path, pathSuffix) {
			return r, true
		}
	}
	return recordedReq{}, false
}

func (m *provisionMock) count(method, contains string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.reqs {
		if r.method == method && strings.Contains(r.path, contains) {
			n++
		}
	}
	return n
}

func (m *provisionMock) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.record(r)
		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/index.php/apps/groupfolders/folders":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":`+m.folders+`}}`)
		case r.Method == http.MethodPost && p == "/index.php/apps/groupfolders/folders":
			// The create body (default-deny + mountpoint) is asserted from the
			// recorded request in the test, not here (record() consumed the body).
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":{"id":9,"mount_point":"Cassini","manage":[]}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups/admin" && m.adminList != "":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":`+m.adminList+`}}}`)
		case r.Method == http.MethodGet && strings.HasPrefix(p, "/ocs/v2.php/cloud/groups/"):
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":`+m.members+`}}}`)
		case r.Method == http.MethodPost && p == "/ocs/v2.php/cloud/users" && m.userExists:
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":102,"message":"User already exists"},"data":[]}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/users":
			// Serve the seeded page once; empty on any subsequent offset.
			if r.URL.Query().Get("offset") == "0" {
				io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":`+m.users+`}}}`)
			} else {
				io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":[]}}}`)
			}
		default:
			// addGroup, setPermissions, acl, manageACL, group create,
			// add-user-to-group, PROPPATCH, MKCOL — all succeed.
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
		}
	})
}

func TestProvisionFreshCreatesFolderGroupsACLAndReconciles(t *testing.T) {
	mock := &provisionMock{
		folders: `[]`,                      // no existing folder
		members: `["admin"]`,               // admin already in the viewer group
		users:   `["admin","alice","bob"]`, // alice + bob need adding
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg.provisionNCFilesAccess(ctx, log.New(io.Discard, "", 0))

	// All calls act as the recordings owner (admin).
	// Provisioning acts as the administrator; only the recordings writes act as
	// the service account (D-532).
	ownerAuth := base64.StdEncoding.EncodeToString([]byte(defaultNextcloudAdminUser + ":" + cfg.AppSecret))

	if r, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); !ok {
		t.Error("folder was not created")
	} else {
		if r.auth != ownerAuth {
			t.Errorf("create folder auth = %q, want act-as-owner", r.auth)
		}
		if !strings.Contains(r.body, "acl_default_no_permission=1") {
			t.Errorf("create folder body = %q, want default-deny", r.body)
		}
		if !strings.Contains(r.body, "mountpoint=Cassini") {
			t.Errorf("create folder body = %q, want mountpoint=Cassini", r.body)
		}
	}

	// Group assignments: viewer group read + owner group full.
	if r, ok := mock.find(http.MethodPost, "/folders/9/groups/recording-viewers"); !ok || !strings.Contains(r.body, "permissions=1") {
		t.Errorf("viewer group permissions not set to read: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/groups/"+ncRecordingsOwnerGroup); !ok || !strings.Contains(r.body, "permissions=31") {
		t.Errorf("owner group permissions not set to full: %+v ok=%v", r, ok)
	}
	// Advanced ACL enabled + owner delegated as manager (fresh folder has none).
	if r, ok := mock.find(http.MethodPost, "/folders/9/acl"); !ok || !strings.Contains(r.body, "acl=1") {
		t.Errorf("advanced ACL not enabled: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/manageACL"); !ok || !strings.Contains(r.body, "mappingId="+ncRecordingsOwner) {
		t.Errorf("owner not delegated as ACL manager: %+v ok=%v", r, ok)
	}
	// Root container ACL via PROPPATCH on the mount root, then the collections.
	if r, ok := mock.find("PROPPATCH", "/remote.php/dav/files/"+ncRecordingsOwner+"/Cassini"); !ok {
		t.Error("root container ACL PROPPATCH missing")
	} else if !strings.Contains(r.body, "recording-viewers") || !strings.Contains(r.body, "<nc:acl-permissions>1</nc:acl-permissions>") {
		t.Errorf("root container ACL body missing viewer read grant: %s", r.body)
	}
	if mock.count("MKCOL", "/Cassini/Recordings") < 2 {
		t.Errorf("expected MKCOL of Recordings and meetings, got %d", mock.count("MKCOL", "/Cassini/Recordings"))
	}
	// The viewer-group reconcile runs off this synchronous path (in the
	// background sweep, gated by provisioningActive) — covered by
	// TestReconcileViewerGroupMembers, not asserted here.
}

func TestReconcileViewerGroupMembers(t *testing.T) {
	mock := &provisionMock{
		members: `["admin"]`,               // admin already in the viewer group
		users:   `["admin","alice","bob"]`, // alice + bob need adding
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	cfg := testExAppConfig(srv.URL)

	added, total, err := cfg.reconcileViewerGroupMembers(context.Background(), &http.Client{}, log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if added != 2 || total != 3 {
		t.Errorf("added=%d total=%d, want 2 of 3", added, total)
	}
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/alice/groups"); !ok {
		t.Error("alice not added to viewer group")
	}
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/bob/groups"); !ok {
		t.Error("bob not added to viewer group")
	}
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/admin/groups"); ok {
		t.Error("admin should not be re-added (already a viewer-group member)")
	}
}

func TestProvisionExistingFolderSkipsManagerAndCreate(t *testing.T) {
	// Reset the process-wide ticker guard so this test's provision is unaffected
	// by ordering; the guard only prevents a duplicate background goroutine.
	mock := &provisionMock{
		folders: `{"3":{"id":3,"mount_point":"Cassini","manage":[{"type":"user","id":"` + ncRecordingsOwner + `"}]}}`,
		members: `["admin","alice","bob"]`, // everyone already a member
		users:   `["admin","alice","bob"]`,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg.provisionNCFilesAccess(ctx, log.New(io.Discard, "", 0))

	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
		t.Error("folder should not be re-created when it already exists")
	}
	if _, ok := mock.find(http.MethodPost, "/folders/3/manageACL"); ok {
		t.Error("manageACL must be skipped when the owner is already a manager (re-adding is a 500)")
	}
	// Permissions + ACL are still (idempotently) reasserted on the existing id.
	if _, ok := mock.find(http.MethodPost, "/folders/3/acl"); !ok {
		t.Error("advanced ACL should still be reasserted on the existing folder")
	}
	// The reconcile is off this synchronous path, so no user-add calls happen here.
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/alice/groups"); ok {
		t.Error("user-group reconcile must not run on the synchronous provision path")
	}
}

func TestEnsureParticipantsInViewerGroupAddsOnlyUsers(t *testing.T) {
	mock := &provisionMock{folders: `[]`, members: `[]`, users: `[]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	cfg := testExAppConfig(srv.URL)

	cfg.ensureParticipantsInViewerGroup(context.Background(), &http.Client{}, []aclMapping{
		{Type: "user", ID: "alice"},
		{Type: "group", ID: "team"}, // groups covered by the all-users reconcile
		{Type: "circle", ID: "c1"},  // circles too
		{Type: "user", ID: ""},      // empty ids skipped
	}, log.New(io.Discard, "", 0))

	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/alice/groups"); !ok {
		t.Error("user participant alice was not added to the viewer group")
	}
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/team/groups"); ok {
		t.Error("group mapping must not be added as a user")
	}
	if mock.count(http.MethodPost, "/cloud/users/") != 1 {
		t.Errorf("expected exactly one user add, got %d", mock.count(http.MethodPost, "/cloud/users/"))
	}
}

func TestProvisionNoopWhenDisabled(t *testing.T) {
	mock := &provisionMock{folders: `[]`, members: `[]`, users: `[]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = false // disabled → provisioner must not touch Nextcloud
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	cfg.mu(t, mock)
}

// mu asserts no requests were made (helper kept tiny to read at the call site).
func (c ExAppConfig) mu(t *testing.T, mock *provisionMock) {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.reqs) != 0 {
		t.Errorf("access control disabled but %d requests were issued", len(mock.reqs))
	}
}

// The recordings tree is owned by a dedicated service account rather than the
// instance administrator (D-532). Two identities are in play and confusing them
// is the failure mode worth pinning: provisioning needs admin rights, ownership
// must not have them.

func TestProvisionCreatesTheRecordingsServiceAccount(t *testing.T) {
	mock := &provisionMock{
		folders: `[]`,
		members: `["admin"]`,
		users:   `["admin"]`,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	resolvedProvisioningUser.Store(nil)

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	cfg.provisionNCFilesAccess(context.Background(), log.New(ioDiscard{}, "", 0))

	create, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users")
	if !ok {
		t.Fatalf("the service account was never created")
	}
	if !strings.Contains(create.body, "userid="+ncRecordingsOwner) {
		t.Fatalf("create body = %q, want userid=%s", create.body, ncRecordingsOwner)
	}
	// It must land in the owner group, or it has no write-capable mount of the
	// group folder and every delivery fails.
	if !strings.Contains(create.body, "groups="+ncRecordingsOwnerGroup) {
		t.Fatalf("create body = %q, want groups=%s", create.body, ncRecordingsOwnerGroup)
	}
	// The account is created BY the administrator. Acting as the account we are
	// about to create would be circular.
	wantAdmin := base64.StdEncoding.EncodeToString([]byte(defaultNextcloudAdminUser + ":" + cfg.AppSecret))
	if create.auth != wantAdmin {
		t.Fatalf("create auth = %q, want the administrator", create.auth)
	}
	// The generated password must never reach the log or any record of the
	// request beyond Nextcloud itself; we only assert it is present and random-
	// looking, never what it is.
	if !strings.Contains(create.body, "password=") {
		t.Fatalf("create body has no password: %q", create.body)
	}
}

func TestProvisionResolvesTheAdministratorRatherThanAssumingAdmin(t *testing.T) {
	// "admin" is conventional, not guaranteed. An instance whose administrator
	// is called something else would otherwise have every privileged call
	// rejected.
	mock := &provisionMock{
		folders:   `[]`,
		members:   `["ops-root"]`,
		users:     `["ops-root"]`,
		adminList: `["ops-root"]`,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	resolvedProvisioningUser.Store(nil)
	t.Cleanup(func() { resolvedProvisioningUser.Store(nil) })

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	cfg.provisionNCFilesAccess(context.Background(), log.New(ioDiscard{}, "", 0))

	wantAuth := base64.StdEncoding.EncodeToString([]byte("ops-root:" + cfg.AppSecret))
	create, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users")
	if !ok {
		t.Fatalf("the service account was never created")
	}
	if create.auth != wantAuth {
		t.Fatalf("create auth = %q, want the discovered administrator ops-root", create.auth)
	}
}

func TestProvisionAcceptsAnExistingServiceAccount(t *testing.T) {
	// Re-enabling must not disturb an account that is already there — never
	// re-created, never re-passworded, never disabled.
	mock := &provisionMock{
		folders:    `[]`,
		members:    `["admin"]`,
		users:      `["admin","` + ncRecordingsOwner + `"]`,
		userExists: true,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()
	resolvedProvisioningUser.Store(nil)

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	cfg.provisionNCFilesAccess(context.Background(), log.New(ioDiscard{}, "", 0))

	// Provisioning must still have got as far as the folder work; an
	// "already exists" response is success, not a failure that aborts the run.
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); !ok {
		t.Fatalf("provisioning stopped at the existing account instead of continuing")
	}
	// Membership is re-asserted, because the group may have been created after
	// the account.
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/"+ncRecordingsOwner+"/groups"); !ok {
		t.Fatalf("existing account was not re-added to the owner group")
	}
}
