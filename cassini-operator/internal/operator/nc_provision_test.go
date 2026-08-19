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
	// refuseFolderWrites makes the Group Folders writes answer the way a
	// password-confirmation-enforcing Nextcloud does: HTTP 200, with the failure
	// only in ocs.meta.
	refuseFolderWrites bool
	// ownerPrepared models an administrator having created the service account
	// and its group membership by hand — the documented recovery on a Nextcloud
	// that refuses those writes to an ExApp.
	ownerPrepared bool
	// confirmationRequired makes every user-administration write answer 403
	// "Password confirmation is required", the way a Nextcloud that enforces
	// #[PasswordConfirmationRequired] answers an ExApp — which can never satisfy
	// it, having no browser session to confirm a password in.
	confirmationRequired bool
	// failPath makes any request whose path ends with this suffix answer 500,
	// for the provisioning steps that must now abort rather than log on.
	failPath string
	// apps is ocs.data.apps for GET /cloud/apps?filter=enabled. Nil means both
	// native prerequisites are enabled, which is what almost every test wants.
	apps []string
	// adminActors maps an act-as identity to the status GET /cloud/groups/admin
	// answers it. Nil means "admin is an administrator", the conventional shape.
	// Unlisted actors get 401 — a live Nextcloud's answer for an account that
	// does not exist, and 403 for one that exists but is not an admin
	// (spike-x1).
	adminActors map[string]int
	// roster is ocs.data for GET /apps/app_api/api/v1/users, which answers
	// app-scoped and is what lets discovery enumerate rather than guess.
	roster []string
}

// actorOf decodes the act-as identity out of the AppAPI auth header, so the mock
// can answer per-identity the way a real Nextcloud does.
func actorOf(r *http.Request) string {
	raw, err := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
	if err != nil {
		return ""
	}
	actor, _, _ := strings.Cut(string(raw), ":")
	return actor
}

func jsonArray(items []string) string {
	quoted := make([]string, 0, len(items))
	for _, item := range items {
		quoted = append(quoted, `"`+item+`"`)
	}
	return "[" + strings.Join(quoted, ",") + "]"
}

func resetProvisioningUser(t *testing.T) {
	t.Helper()
	clear := func() {
		resolvedProvisioningUser.Store(nil)
		resolvedAdminRoster.Store(nil)
	}
	clear()
	t.Cleanup(clear)
}

// resetSubstrateRecord is mandatory in every test that calls
// provisionNCFilesAccess. ncAccessSubstrate is a package-level singleton and
// the whole package's tests run in one binary, so a test that deliberately
// ends in a failed state would otherwise make statusHandler answer 503 for
// every later test in status_test.go.
func resetSubstrateRecord(t *testing.T) {
	t.Helper()
	ncAccessSubstrate.reset()
	// Applicability is decided once at startup by run.go, not by the provisioner,
	// so a test that wants to read the recorded outcome has to declare it — the
	// deployment being modelled here is an ExApp delivering to Nextcloud Files.
	ncAccessSubstrate.markApplicable()
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
		case r.Method == http.MethodGet && p == "/ocs/v2.php/apps/app_api/api/v1/users":
			// Answers app-scoped (empty actor) on a live instance, which is what
			// makes it usable before any identity is known.
			roster := m.roster
			if roster == nil {
				roster = []string{"admin"}
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":`+jsonArray(roster)+`}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/apps":
			apps := m.apps
			if apps == nil {
				apps = ncRequiredNativeApps
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"apps":`+jsonArray(apps)+`}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"groups":`+m.groups+`}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups/admin":
			// Per-actor, because that is the discriminator admin discovery now
			// depends on: 2xx = this actor IS an admin, 403 = exists but is not,
			// 401 = no such act-as (verified live, spike-x1). A mock that
			// answered 200 to everyone is exactly why the circularity survived.
			actor := actorOf(r)
			want := http.StatusOK
			if m.adminActors != nil {
				code, listed := m.adminActors[actor]
				if !listed {
					code = http.StatusUnauthorized
				}
				want = code
			} else if actor != defaultNextcloudAdminUser {
				want = http.StatusUnauthorized
			}
			if want/100 != 2 {
				w.WriteHeader(want)
				io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":997},"data":[]}}`)
				return
			}
			admins := m.adminList
			if admins == "" {
				admins = `["admin"]`
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":`+admins+`}}}`)
		case m.ownerPrepared && r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/users/"+ncRecordingsOwner:
			// statuscode 200, as /ocs/v2.php really answers — not 100, which is
			// the v1 code. Getting this wrong in the mock is what let a check
			// that could never succeed against a live Nextcloud pass its test.
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"id":"`+ncRecordingsOwner+`"}}}`)
		case m.ownerPrepared && r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups/"+ncRecordingsOwnerGroup:
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":["`+ncRecordingsOwner+`"]}}}`)
		case m.confirmationRequired && r.Method == http.MethodPost &&
			(p == "/ocs/v2.php/cloud/groups" || p == "/ocs/v2.php/cloud/users" ||
				strings.HasSuffix(p, "/groups") && strings.HasPrefix(p, "/ocs/v2.php/cloud/users/")):
			w.WriteHeader(http.StatusForbidden)
			io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Password confirmation is required"},"data":[]}}`)
		case r.Method == http.MethodPost && p == "/ocs/v2.php/cloud/users" && m.userExists:
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":102,"message":"User already exists"},"data":[]}}`)
		case r.Method == http.MethodGet && p == "/index.php/apps/groupfolders/folders":
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":100},"data":`+m.folders+`}}`)
		case m.refuseFolderWrites && r.Method == http.MethodPost && strings.HasPrefix(p, "/index.php/apps/groupfolders/"):
			io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Password confirmation is required"},"data":[]}}`)
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

// With the preflight in place, a folder-create failure is no longer evidence
// that the Group folders app is missing — the preflight already established it
// is enabled. It is a distinct diagnosis, and the step says so.
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
	if snap.Step != "group_folder" {
		t.Fatalf("step = %q, want group_folder", snap.Step)
	}
	if !strings.Contains(snap.Detail, ncRecordingsMount) {
		t.Fatalf("substrate detail = %q, want it to name the Team folder", snap.Detail)
	}
}

// D-585 outcome 1: a NOT-INSTALLED app is actionable and must be named. Nextcloud
// AIO does not ship Team folders enabled, so this is the common case.
func TestProvisionNamesTheMissingNativeApp(t *testing.T) {
	for _, missing := range ncRequiredNativeApps {
		t.Run(missing, func(t *testing.T) {
			resetProvisioningUser(t)
			resetSubstrateRecord(t)
			var enabled []string
			for _, app := range ncRequiredNativeApps {
				if app != missing {
					enabled = append(enabled, app)
				}
			}
			mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`, apps: enabled}
			srv := httptest.NewServer(mock.handler(t))
			defer srv.Close()

			var logs strings.Builder
			testExAppConfig(srv.URL).provisionNCFilesAccess(context.Background(), log.New(&logs, "", 0))

			snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
			if snap.State != string(ncSubstrateUnavailable) || snap.Step != "app_missing:"+missing {
				t.Fatalf("substrate = %+v, want unavailable/app_missing:%s", snap, missing)
			}
			if !strings.Contains(logs.String(), "occ app:install "+missing) {
				t.Fatalf("the log must name the fix: %s", logs.String())
			}
			// Nothing may be attempted against a substrate that cannot exist.
			if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
				t.Fatal("a folder was created despite a missing prerequisite")
			}
			if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users"); ok {
				t.Fatal("the service account was created despite a missing prerequisite")
			}
			// And the per-app list must name which one, not merely that one is missing.
			var reported string
			for _, p := range snap.Prerequisites {
				if p.State == ncPrerequisiteMissing {
					reported = p.Name
				}
			}
			if reported != missing {
				t.Fatalf("prerequisites = %+v, want %s reported missing", snap.Prerequisites, missing)
			}
		})
	}
}

// ...and a FAILED CHECK is a different diagnosis, because nothing is installed
// to fix it. Conflating the two turns an `occ app:install` into an investigation.
func TestProvisionDistinguishesAFailedAppCheckFromAMissingApp(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{folders: `[]`, groups: `["admin","everyone"]`, failPath: "/cloud/apps"}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	testExAppConfig(srv.URL).provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.State != string(ncSubstrateDegraded) || snap.Step != "app_check_failed" {
		t.Fatalf("substrate = %+v, want degraded/app_check_failed", snap)
	}
	if strings.HasPrefix(snap.Step, "app_missing:") {
		t.Fatal("a failed check must not be reported as a missing app")
	}
	for _, p := range snap.Prerequisites {
		if p.State != ncPrerequisiteUnknown {
			t.Fatalf("an unanswerable check leaves every app unknown, got %+v", p)
		}
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
	mock := &provisionMock{
		folders:     `[]`,
		groups:      `["everyone"]`,
		adminList:   `["ops-root"]`,
		roster:      []string{"alice", "ops-root", "bob"},
		adminActors: map[string]int{"ops-root": http.StatusOK, "admin": http.StatusUnauthorized},
	}
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

// The enabled edge must provision on nothing more than "this is an ExApp".
//
// This is a regression guard, not a unit test of a predicate. The hook used to
// be installed only when the whole-archive uploader had been constructed, so
// deleting that uploader (D-613) would have silently detached provisioning:
// no group folder, no ACL topology, /status stuck at 503, and nothing in the
// logs pointing at the change that caused it. Asserting the callback exists
// AND actually reaches Nextcloud keeps the two independent.
func TestEnabledCallbackProvisionsWheneverAppAPIIsActive(t *testing.T) {
	// Provisioning writes the process-wide substrate record, which /status
	// reads; leaving this run's failure behind would fail unrelated tests.
	t.Cleanup(ncAccessSubstrate.reset)

	var mu sync.Mutex
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		called = true
		mu.Unlock()
		// Fail every provisioning call: this test is about whether provisioning
		// was ATTEMPTED from the edge, and provisioning is deliberately
		// non-fatal, so the unhappy path keeps it fast and hermetic.
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	hook := cfg.enabledCallback(context.Background(), log.New(io.Discard, "", 0))
	if hook == nil {
		t.Fatal("no enabled callback for an AppAPI-active config: provisioning would never run")
	}

	// Disabled edges must not provision.
	hook(false)
	mu.Lock()
	if called {
		t.Error("provisioning ran on an enabled=false edge")
	}
	mu.Unlock()

	hook(true)
	mu.Lock()
	defer mu.Unlock()
	if !called {
		t.Fatal("enabled edge did not reach Nextcloud: provisioning is detached")
	}
}

func TestEnabledCallbackIsNilOutsideAppAPI(t *testing.T) {
	// A standalone operator has no Nextcloud to provision, and AppAPI's
	// lifecycle routes are not mounted at all.
	for name, cfg := range map[string]ExAppConfig{
		"empty":            {},
		"secret-only":      {AppSecret: "s", AppID: "gocassini"},
		"url-without-auth": {NextcloudURL: "https://nc.example.com"},
	} {
		if got := cfg.enabledCallback(context.Background(), log.New(io.Discard, "", 0)); got != nil {
			t.Errorf("%s: enabledCallback is non-nil outside an AppAPI deployment", name)
		}
	}
}

// A Nextcloud that enforces #[PasswordConfirmationRequired] refuses every write
// an ExApp makes to user administration — creating a group, creating an account,
// adding it to a group — because the middleware reads `last-password-confirm`
// out of a PHP session an act-as-user request does not have. There is no
// configuration that relaxes it: the exclusion list is a hardcoded private array
// and the timestamp is session state.
//
// The supported answer is for an administrator to create those three by hand,
// where password confirmation works as designed. That answer only works if
// provisioning LOOKS before it writes: found on a real Nextcloud 34 (AIO), where
// the account and group existed and provisioning still failed forever, because
// the 403 arrives before Nextcloud ever evaluates existence — so a prepared
// instance was indistinguishable from an empty one.
func TestProvisionAdoptsAManuallyPreparedOwnerWhenWritesAreRefused(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{
		folders: `[]`,
		// The administrator has already created both, by hand.
		groups:               `["admin","everyone","` + ncRecordingsOwnerGroup + `"]`,
		ownerPrepared:        true,
		confirmationRequired: true,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	// The substrate must come up: every write that follows the owner account —
	// the Team folder, its ACL topology — is NOT password-confirmation
	// protected, so nothing after this step is actually blocked.
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); !ok {
		t.Fatal("provisioning stopped at the owner account and never built the Team folder, " +
			"even though the group and account were already present")
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if !snap.OK {
		t.Fatalf("substrate reported not-ok on a correctly prepared instance: %+v", snap)
	}
}

// The converse: when the writes are refused AND nothing has been prepared, the
// run must still fail — and say what to do about it, rather than reporting a
// bare 403 the reader has to interpret.
func TestProvisionRefusedWritesWithNothingPreparedStillFailLoudly(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{
		folders:              `[]`,
		groups:               `["admin","everyone"]`, // no cassini group
		confirmationRequired: true,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	var logs strings.Builder
	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(&logs, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatal("substrate reported ok with no owner group and no way to create one")
	}
	if !strings.Contains(logs.String(), "occ group:add") {
		t.Errorf("the refusal must tell an administrator how to recover; got: %s", logs.String())
	}
}

// The Group Folders API answers a refused write with HTTP 200 and the failure
// only in ocs.meta. Checking the HTTP status alone therefore reads a refusal as
// a success — and provisioning did, then tried to decode the empty `data: []`
// as a folder and reported `json: cannot unmarshal array into ... gfFolder`,
// naming neither the status nor the reason.
//
// Found on the sandbox (Nextcloud 34, AIO), where every groupfolders write is
// #[PasswordConfirmationRequired] and an ExApp cannot satisfy it. Reproduced by
// hand against the live server:
//
//	HTTP=200
//	{"ocs":{"meta":{"status":"failure","statuscode":403,
//	                "message":"Password confirmation is required"},"data":[]}}
func TestOCSRefusalSeesAFailureWearingAnHTTP200(t *testing.T) {
	refused := []byte(`{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Password confirmation is required"},"data":[]}}`)
	got := ocsRefusal(http.StatusOK, refused)
	if got == "" {
		t.Fatal("an OCS failure carried by an HTTP 200 was read as success")
	}
	if !strings.Contains(got, "403") || !strings.Contains(got, "Password confirmation") {
		t.Errorf("the refusal must name the status and the reason, got %q", got)
	}

	// Both OCS success codes: 100 is v1, 200 is v2.
	for _, ok := range [][]byte{
		[]byte(`{"ocs":{"meta":{"status":"ok","statuscode":100},"data":{}}}`),
		[]byte(`{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{}}}`),
	} {
		if refusal := ocsRefusal(http.StatusOK, ok); refusal != "" {
			t.Errorf("a success envelope was read as a refusal: %q (%s)", refusal, ok)
		}
	}

	// A non-2xx is a refusal whatever the body says.
	if ocsRefusal(http.StatusForbidden, []byte(`{}`)) == "" {
		t.Error("a non-2xx must always be a refusal")
	}
}

// The substrate build must not report success when the Team folder was never
// created, and must say what an administrator can do about it.
func TestProvisionSurfacesARefusedFolderCreate(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{
		folders:              `[]`,
		groups:               `["admin","everyone","` + ncRecordingsOwnerGroup + `"]`,
		ownerPrepared:        true,
		confirmationRequired: true,
		refuseFolderWrites:   true,
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	var logs strings.Builder
	cfg := testExAppConfig(srv.URL)
	cfg.provisionNCFilesAccess(context.Background(), log.New(&logs, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.OK {
		t.Fatal("substrate reported ok though the Team folder was never created")
	}
	out := logs.String()
	if strings.Contains(out, "cannot unmarshal") {
		t.Errorf("the failure is reported as a JSON decode error rather than the refusal: %s", out)
	}
	if !strings.Contains(out, "Password confirmation") {
		t.Errorf("the refusal's reason is not reported: %s", out)
	}
	if !strings.Contains(out, "groupfolders:create") {
		t.Errorf("the refusal must name the recovery; got: %s", out)
	}
}
