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

func TestGFFolderHasGroupAcceptsArrayAndObjectShapes(t *testing.T) {
	if (gfFolder{Groups: []byte(`[]`)}).hasGroup("everyone") {
		t.Fatal("empty-array groups shape reported a membership")
	}
	folder := gfFolder{Groups: []byte(`{"everyone":{"permissions":1}}`)}
	if !folder.hasGroup("everyone") || folder.hasGroup("recording-viewers") {
		t.Fatalf("object groups shape was not matched exactly: %s", folder.Groups)
	}
}

func TestContainerACLRules(t *testing.T) {
	rules := containerACLRules()
	if len(rules) != 2 {
		t.Fatalf("containerACLRules = %+v, want owner + everyone group", rules)
	}
	if rules[0].Type != "user" || rules[0].ID != ncRecordingsOwner || rules[0].Permissions != aclMaskAll {
		t.Errorf("owner rule = %+v, want full-permission owner user", rules[0])
	}
	if rules[1].Type != "group" || rules[1].ID != ncRecordingsEveryoneGroup || rules[1].Permissions != aclPermRead {
		t.Errorf("audience rule = %+v, want read-only everyone group", rules[1])
	}
}

func TestParseOCSGroupList(t *testing.T) {
	groups, err := parseOCSGroupList([]byte(`{"ocs":{"data":{"groups":["admin","everyone"]}}}`))
	if err != nil || len(groups) != 2 || groups[1] != "everyone" {
		t.Fatalf("parseOCSGroupList = %v, %v", groups, err)
	}
	if _, err := parseOCSGroupList([]byte(`not json`)); err == nil {
		t.Fatal("parseOCSGroupList accepted invalid JSON")
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
// Group Folders responses the provisioner depends on.
type provisionMock struct {
	mu      sync.Mutex
	reqs    []recordedReq
	folders string // ocs.data body for GET /folders
	groups  string // ocs.data.groups for GET /cloud/groups
	// adminList is ocs.data.users for GET /cloud/groups/admin.
	adminList string
	// userExists makes service-account creation return OCS 102.
	userExists bool
	// failPath makes any request whose path ends with this suffix answer 500,
	// for the provisioning steps that must now abort rather than log on.
	failPath string
}

func resetProvisioningUser(t *testing.T) {
	t.Helper()
	resolvedProvisioningUser.Store(nil)
	t.Cleanup(func() { resolvedProvisioningUser.Store(nil) })
}

// resetSubstrateRecord is mandatory in every test that calls
// provisionNCFilesAccess. ncAccessSubstrate is a package-level singleton and
// the whole package's tests run in one binary, so a test that deliberately
// ends in a failed state would otherwise make statusHandler answer 503 for
// every later test in status_test.go.
func resetSubstrateRecord(t *testing.T) {
	t.Helper()
	ncAccessSubstrate.reset()
	t.Cleanup(ncAccessSubstrate.reset)
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

func (m *provisionMock) findLast(method, pathSuffix string) (recordedReq, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.reqs) - 1; i >= 0; i-- {
		if m.reqs[i].method == method && strings.HasSuffix(m.reqs[i].path, pathSuffix) {
			return m.reqs[i], true
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
		case m.failPath != "" && strings.HasSuffix(p, m.failPath):
			w.WriteHeader(http.StatusInternalServerError)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"groups":`+m.groups+`}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups/admin":
			admins := m.adminList
			if admins == "" {
				admins = `["admin"]`
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":`+admins+`}}}`)
		case r.Method == http.MethodPost && p == "/ocs/v2.php/cloud/users" && m.userExists:
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":102,"message":"User already exists"},"data":[]}}`)
		case r.Method == http.MethodGet && p == "/index.php/apps/groupfolders/folders":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":`+m.folders+`}}`)
		case r.Method == http.MethodPost && p == "/index.php/apps/groupfolders/folders":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":{"id":9,"mount_point":"Cassini","groups":{},"manage":[]}}}`)
		case r.Method == "PROPFIND" && strings.HasSuffix(p, "/meetings"):
			w.WriteHeader(http.StatusNotFound) // fresh/no legacy leaves
		case r.Method == http.MethodGet && strings.HasSuffix(p, "/catalog.json"):
			w.WriteHeader(http.StatusNotFound) // fresh/no legacy catalog
		default:
			// add/set/remove Group-folder mapping, ACL, manageACL, PROPPATCH,
			// and MKCOL all succeed.
			w.WriteHeader(http.StatusOK)
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":[]}}`)
		}
	})
}

func TestProvisionFreshUsesEveryoneAndDedicatedOwner(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	adminAuth := base64.StdEncoding.EncodeToString([]byte(defaultNextcloudAdminUser + ":" + cfg.AppSecret))
	if r, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); !ok {
		t.Error("folder was not created")
	} else {
		if r.auth != adminAuth {
			t.Errorf("create folder auth = %q, want act-as-administrator", r.auth)
		}
		if !strings.Contains(r.body, "acl_default_no_permission=1") || !strings.Contains(r.body, "mountpoint=Cassini") {
			t.Errorf("create folder body = %q, want Cassini default-deny", r.body)
		}
	}

	if r, ok := mock.find(http.MethodPost, "/folders/9/groups/everyone"); !ok || !strings.Contains(r.body, "permissions=1") {
		t.Errorf("everyone permissions not set to read: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/groups/"+ncRecordingsOwnerGroup); !ok || !strings.Contains(r.body, "permissions=31") {
		t.Errorf("owner group permissions not set to full: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/acl"); !ok || !strings.Contains(r.body, "acl=1") {
		t.Errorf("advanced ACL not enabled: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/manageACL"); !ok || !strings.Contains(r.body, "mappingId="+ncRecordingsOwner) {
		t.Errorf("owner not delegated as ACL manager: %+v ok=%v", r, ok)
	}
	if r, ok := mock.findLast("PROPPATCH", "/remote.php/dav/files/"+ncRecordingsOwner+"/Cassini"); !ok {
		t.Error("root container ACL PROPPATCH missing")
	} else if !strings.Contains(r.body, ncRecordingsEveryoneGroup) || !strings.Contains(r.body, "<nc:acl-permissions>1</nc:acl-permissions>") {
		t.Errorf("root container ACL body missing everyone read grant: %s", r.body)
	}
	if mock.count("MKCOL", "/Cassini/Recordings") < 2 {
		t.Errorf("expected MKCOL of Recordings and meetings, got %d", mock.count("MKCOL", "/Cassini/Recordings"))
	}
	if create, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users"); !ok || create.auth != adminAuth {
		t.Fatalf("service account was not created by the administrator: %+v ok=%v", create, ok)
	}
	if group, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/groups"); !ok || !strings.Contains(group.body, "groupid="+ncRecordingsOwnerGroup) || strings.Contains(group.body, "groupid=everyone") {
		t.Fatalf("only the narrow owner group should be created: %+v ok=%v", group, ok)
	}
	if mock.count(http.MethodPost, "/cloud/users/"+ncRecordingsOwner+"/groups") != 1 {
		t.Fatal("service-account owner-group membership was not asserted exactly once")
	}
}

func TestProvisionRequiresUniversalGroup(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	var logs strings.Builder
	cfg.provisionNCFilesAccess(context.Background(), log.New(&logs, "", 0))

	if !strings.Contains(logs.String(), "group_everyone") {
		t.Fatalf("missing-group log must name the prerequisite: %s", logs.String())
	}
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
		t.Fatal("provisioning created a folder without the required universal group")
	}
	// The log is for whoever is tailing it; /status is for whoever is not.
	if detail := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles).Detail; !strings.Contains(detail, "group_everyone") {
		t.Fatalf("substrate detail = %q, want it to name the prerequisite", detail)
	}
}

func TestProvisionExistingFolderMigratesLegacyMountMapping(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{
		folders: `{"3":{"id":3,"mount_point":"Cassini","groups":{"admin":{"permissions":31},"recording-viewers":{"permissions":1}},"manage":[{"type":"user","id":"` + ncRecordingsOwner + `"},{"type":"user","id":"admin"}]}}`,
		groups:  `["admin","everyone","recording-viewers"]`,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
		t.Error("folder should not be re-created")
	}
	if r, ok := mock.find(http.MethodPost, "/folders/3/manageACL"); !ok || !strings.Contains(r.body, "mappingId=admin") || !strings.Contains(r.body, "manageAcl=0") {
		t.Errorf("legacy administrator ACL manager was not removed: %+v ok=%v", r, ok)
	}
	if _, ok := mock.find(http.MethodDelete, "/folders/3/groups/recording-viewers"); !ok {
		t.Error("legacy recording-viewers mount mapping was not removed")
	}
	if _, ok := mock.find(http.MethodDelete, "/folders/3/groups/admin"); !ok {
		t.Error("legacy administrator write mapping was not removed")
	}
	if r, ok := mock.find(http.MethodPost, "/folders/3/groups/everyone"); !ok || !strings.Contains(r.body, "permissions=1") {
		t.Errorf("everyone mount mapping was not installed first: %+v ok=%v", r, ok)
	}
}

func TestGroupExistsUsesExactID(t *testing.T) {
	mock := &provisionMock{folders: `[]`, groups: `["everyone-else","everyone"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	got, err := testExAppConfig(srv.URL).groupExists(context.Background(), srv.Client(), "everyone")
	if err != nil || !got {
		t.Fatalf("groupExists = %t, %v", got, err)
	}
	got, err = testExAppConfig(srv.URL).groupExists(context.Background(), srv.Client(), "missing")
	if err != nil || got {
		t.Fatalf("groupExists(missing) = %t, %v", got, err)
	}
}

// The identity tier runs before — and independently of — the topology tier, so
// a substrate that cannot be built must still leave behind the account every
// archive object is written as. Otherwise a later, fixed install would find
// itself acting as a user that does not exist.
func TestProvisionCreatesTheOwnerEvenWhenTheSubstrateCannotBeBuilt(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `[]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users"); !ok {
		t.Fatal("service account was not created")
	}
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
		t.Fatal("a group folder was provisioned without the required universal group")
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.Applicable || snap.OK {
		t.Fatalf("substrate = %+v, want applicable and not ok", snap)
	}
	if !strings.Contains(snap.Detail, "group_everyone") {
		t.Fatalf("substrate detail = %q, want it to name the missing app", snap.Detail)
	}
}

// The Group folders app is the other native prerequisite an ExApp cannot
// install for itself, and its absence surfaces as a failed folder creation.
// /status must name it rather than reporting an opaque failure.
func TestProvisionRecordsSubstrateFailureWhenTheGroupFolderCannotBeCreated(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`, failPath: "/apps/groupfolders/folders"}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	testExAppConfig(srv.URL).provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.Applicable || snap.OK {
		t.Fatalf("substrate = %+v, want applicable and not ok", snap)
	}
	if !strings.Contains(snap.Detail, "Group folders") {
		t.Fatalf("substrate detail = %q, want it to name the Group folders app", snap.Detail)
	}
}

// The root grant gives the virtual `everyone` group read on the whole mount.
// It is only safe under the default-deny floor that advanced ACL provides, and
// before D-554 this path was reachable only behind a flag that defaulted off.
// Failing to enable the ACL must therefore abort, not log and widen.
func TestProvisionAbortsBeforeTheBroadRootGrantWhenAdvancedACLCannotBeEnabled(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`, failPath: "/folders/9/acl"}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	testExAppConfig(srv.URL).provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	if r, ok := mock.findLast("PROPPATCH", "/remote.php/dav/files/"+ncRecordingsOwner+"/Cassini"); ok {
		if strings.Contains(r.body, ncRecordingsEveryoneGroup) {
			t.Fatalf("root was widened to %s without an ACL floor: %s", ncRecordingsEveryoneGroup, r.body)
		}
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK || !strings.Contains(snap.Detail, "advanced ACL") {
		t.Fatalf("substrate = %+v, want a recorded advanced-ACL failure", snap)
	}
}

// A stock install is the env AppAPI injects and nothing more — no opt-in, no
// extra variable. It must yield the whole substrate, which is the acceptance
// criterion D-554 exists for.
func TestProvisionBuildsTheWholeSubstrateForAStockInstall(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	testExAppConfig(srv.URL).provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	// The identity tier: the account every archive object is written as.
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users"); !ok {
		t.Fatal("the service account was not created")
	}
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/"+ncRecordingsOwner+"/groups"); !ok {
		t.Fatal("the service account was not added to the owner group")
	}
	// The topology tier, which used to be behind the flag.
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); !ok {
		t.Fatal("the group folder was not provisioned for a stock install")
	}
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders/9/acl"); !ok {
		t.Fatal("the group folder ACL floor was not enabled for a stock install")
	}
	// The audience: a universal read mount, and the matching root grant that
	// every leaf then overrides as private or public.
	if r, ok := mock.find(http.MethodPost, "/folders/9/groups/"+ncRecordingsEveryoneGroup); !ok || !strings.Contains(r.body, "permissions=1") {
		t.Fatalf("universal read mount not installed: %+v ok=%v", r, ok)
	}
	if r, ok := mock.findLast("PROPPATCH", "/remote.php/dav/files/"+ncRecordingsOwner+"/Cassini"); !ok {
		t.Fatal("root container ACL PROPPATCH missing")
	} else if !strings.Contains(r.body, ncRecordingsEveryoneGroup) || !strings.Contains(r.body, "<nc:acl-permissions>1</nc:acl-permissions>") {
		t.Fatalf("root container ACL body missing the everyone read grant: %s", r.body)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.Applicable || !snap.OK {
		t.Fatalf("substrate = %+v, want applicable and ok", snap)
	}
}

func TestProvisionNoopWithoutAppAPI(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `[]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AppSecret = ""
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.reqs) != 0 {
		t.Errorf("non-AppAPI provisioning issued %d requests", len(mock.reqs))
	}
}

func TestProvisionCreatesTheRecordingsServiceAccount(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	create, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users")
	if !ok {
		t.Fatal("service account was never created")
	}
	if !strings.Contains(create.body, "userid="+ncRecordingsOwner) {
		t.Fatalf("create body = %q, want userid=%s", create.body, ncRecordingsOwner)
	}
	// It must land in the owner group, or it has no write-capable mount of the
	// group folder and every delivery fails.
	//
	// The spelling is load-bearing and this assertion used to have it wrong.
	// OCS decodes this field as a PHP array: "groups[]" (url-encoded
	// "groups%5B%5D") works, a scalar "groups" makes Nextcloud answer a bare
	// 400 with an empty body. Against a mock that accepts anything the
	// difference is invisible, which is exactly how it reached a live instance
	// — where the account was never created and every act-as-cassini call 401d.
	if !strings.Contains(create.body, "groups%5B%5D="+ncRecordingsOwnerGroup) {
		t.Fatalf("create body = %q, want the array form groups%%5B%%5D=%s", create.body, ncRecordingsOwnerGroup)
	}
	if strings.Contains(create.body, "groups="+ncRecordingsOwnerGroup) {
		t.Fatalf("create body uses the scalar form, which Nextcloud rejects with 400: %q", create.body)
	}
	// The account is created BY the administrator. Acting as the account we are
	// about to create would be circular.
	wantAdmin := base64.StdEncoding.EncodeToString([]byte(defaultNextcloudAdminUser + ":" + cfg.AppSecret))
	if create.auth != wantAdmin {
		t.Fatalf("create auth = %q, want administrator %q", create.auth, wantAdmin)
	}
	// The generated password must never reach the log or any record of the
	// request beyond Nextcloud itself; we only assert it is present, never
	// what it is.
	if !strings.Contains(create.body, "password=") {
		t.Fatalf("create body has no password: %q", create.body)
	}
}

// "admin" is conventional, not guaranteed. An instance whose administrator is
// named something else must still provision, acting as whoever is actually in
// the admin group.
func TestProvisionResolvesTheAdministratorRatherThanAssumingAdmin(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["everyone"]`, adminList: `["ops-root"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	create, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users")
	if !ok {
		t.Fatal("service account was never created")
	}
	want := base64.StdEncoding.EncodeToString([]byte("ops-root:" + cfg.AppSecret))
	if create.auth != want {
		t.Fatalf("create auth = %q, want discovered administrator %q", create.auth, want)
	}
}

func TestProvisionAcceptsExistingServiceAccount(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`, userExists: true}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); !ok {
		t.Fatal("provisioning stopped at an existing account")
	}
	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users/"+ncRecordingsOwner+"/groups"); !ok {
		t.Fatal("existing account owner-group membership was not reasserted")
	}
}
