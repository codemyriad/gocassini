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
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"groups":`+m.groups+`}}}`)
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

func TestProvisionFreshUsesEveryoneWithoutMembershipWrites(t *testing.T) {
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	ownerAuth := base64.StdEncoding.EncodeToString([]byte(ncRecordingsOwner + ":" + cfg.AppSecret))
	if r, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); !ok {
		t.Error("folder was not created")
	} else {
		if r.auth != ownerAuth {
			t.Errorf("create folder auth = %q, want act-as-owner", r.auth)
		}
		if !strings.Contains(r.body, "acl_default_no_permission=1") || !strings.Contains(r.body, "mountpoint=Cassini") {
			t.Errorf("create folder body = %q, want Cassini default-deny", r.body)
		}
	}

	if r, ok := mock.find(http.MethodPost, "/folders/9/groups/everyone"); !ok || !strings.Contains(r.body, "permissions=1") {
		t.Errorf("everyone permissions not set to read: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/groups/admin"); !ok || !strings.Contains(r.body, "permissions=31") {
		t.Errorf("owner group permissions not set to full: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/acl"); !ok || !strings.Contains(r.body, "acl=1") {
		t.Errorf("advanced ACL not enabled: %+v ok=%v", r, ok)
	}
	if r, ok := mock.find(http.MethodPost, "/folders/9/manageACL"); !ok || !strings.Contains(r.body, "mappingId=admin") {
		t.Errorf("owner not delegated as ACL manager: %+v ok=%v", r, ok)
	}
	if r, ok := mock.findLast("PROPPATCH", "/remote.php/dav/files/admin/Cassini"); !ok {
		t.Error("root container ACL PROPPATCH missing")
	} else if !strings.Contains(r.body, ncRecordingsEveryoneGroup) || !strings.Contains(r.body, "<nc:acl-permissions>1</nc:acl-permissions>") {
		t.Errorf("root container ACL body missing everyone read grant: %s", r.body)
	}
	if mock.count("MKCOL", "/Cassini/Recordings") < 2 {
		t.Errorf("expected MKCOL of Recordings and meetings, got %d", mock.count("MKCOL", "/Cassini/Recordings"))
	}
	if mock.count(http.MethodPost, "/cloud/groups") != 0 || mock.count(http.MethodPost, "/cloud/users/") != 0 {
		t.Fatal("provisioning must neither create the virtual group nor write user memberships")
	}
}

func TestProvisionRequiresUniversalGroup(t *testing.T) {
	mock := &provisionMock{folders: `[]`, groups: `["admin"]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	var logs strings.Builder
	cfg.provisionNCFilesAccess(context.Background(), log.New(&logs, "", 0))

	if !strings.Contains(logs.String(), "group_everyone") {
		t.Fatalf("missing-group log must name the prerequisite: %s", logs.String())
	}
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
		t.Fatal("provisioning created a folder without the required universal group")
	}
}

func TestProvisionExistingFolderMigratesLegacyMountMapping(t *testing.T) {
	mock := &provisionMock{
		folders: `{"3":{"id":3,"mount_point":"Cassini","groups":{"recording-viewers":{"permissions":1}},"manage":[{"type":"user","id":"admin"}]}}`,
		groups:  `["admin","everyone","recording-viewers"]`,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = true
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
		t.Error("folder should not be re-created")
	}
	if _, ok := mock.find(http.MethodPost, "/folders/3/manageACL"); ok {
		t.Error("existing ACL manager must not be re-added")
	}
	if _, ok := mock.find(http.MethodDelete, "/folders/3/groups/recording-viewers"); !ok {
		t.Error("legacy recording-viewers mount mapping was not removed")
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

func TestProvisionNoopWhenDisabled(t *testing.T) {
	mock := &provisionMock{folders: `[]`, groups: `[]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.AccessControl = false
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	mock.mu.Lock()
	defer mock.mu.Unlock()
	if len(mock.reqs) != 0 {
		t.Errorf("access control disabled but %d requests were issued", len(mock.reqs))
	}
}
