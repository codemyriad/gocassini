package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func defaultSharingPolicy() sharingPolicySettings {
	return sharingPolicySettings{APIEnabled: true, AllowGroupSharing: true}
}

// The verdict table D-885 was shaped around. A fail is a claim that no share
// can be made; anything less certain is a warning naming what is known.
func TestEvaluateSharingPolicyVerdicts(t *testing.T) {
	cases := []struct {
		name      string
		edit      func(*sharingPolicySettings)
		groups    []string
		state     string
		code      string
		shareable []string
		mentions  []string
	}{
		{name: "defaults pass", state: "passed", code: "sharing_permitted"},
		{name: "defaults pass with no groups at all", groups: nil, state: "passed", code: "sharing_permitted"},
		{name: "share API off refuses everything", edit: func(s *sharingPolicySettings) { s.APIEnabled = false },
			state: "needs_action", code: "sharing_api_disabled", mentions: []string{"Allow apps to use the Share API"}},
		{name: "excluded group blocks cassini", edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsBlocklist, []string{"robots", "guests"}
		}, groups: []string{"staff", "robots"}, state: "needs_action", code: "sharing_disabled_for_account", mentions: []string{"`robots`"}},
		{name: "excluded groups cassini is not in do not matter", edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsBlocklist, []string{"guests"}
		}, groups: []string{"staff"}, state: "passed", code: "sharing_permitted"},
		{name: "allowlist containing a cassini group passes", edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsAllowlist, []string{"sharers"}
		}, groups: []string{"sharers"}, state: "passed", code: "sharing_permitted"},
		{name: "allowlist without a cassini group blocks it", edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsAllowlist, []string{"sharers"}
		}, groups: []string{"staff"}, state: "needs_action", code: "sharing_disabled_for_account", mentions: []string{"`sharers`"}},
		{name: "allowlist naming nobody blocks everyone", edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode = excludeGroupsAllowlist
		}, groups: []string{"staff"}, state: "needs_action", code: "sharing_disabled_for_account", mentions: []string{"allows no group at all"}},
		{name: "group-members restriction with no groups shares with nobody", edit: func(s *sharingPolicySettings) {
			s.OnlyGroupMembers = true
		}, state: "needs_action", code: "sharing_no_common_group", mentions: []string{"Restrict users to only share with users in their groups", "in no group"}},
		{name: "ignored groups do not count as common groups", edit: func(s *sharingPolicySettings) {
			s.OnlyGroupMembers, s.IgnoredGroups = true, []string{"everyone"}
		}, groups: []string{"everyone"}, state: "needs_action", code: "sharing_no_common_group", mentions: []string{"`everyone`"}},
		{name: "group-members restriction with groups is a warning naming them", edit: func(s *sharingPolicySettings) {
			s.OnlyGroupMembers, s.IgnoredGroups = true, []string{"everyone"}
		}, groups: []string{"staff", "everyone", "engineering"}, state: "warn", code: "sharing_limited_to_groups",
			shareable: []string{"staff", "engineering"}, mentions: []string{"“staff”, “engineering”"}},
		{name: "the customer's configuration: cassini only in admin", edit: func(s *sharingPolicySettings) {
			s.OnlyGroupMembers = true
		}, groups: []string{"admin"}, state: "warn", code: "sharing_limited_to_groups",
			shareable: []string{"admin"}, mentions: []string{"Only administrators can receive recordings", "the administrators' group", "does not need to be an administrator"}},
		{name: "group sharing off is a warning", edit: func(s *sharingPolicySettings) { s.AllowGroupSharing = false },
			state: "warn", code: "group_sharing_disabled", mentions: []string{"Allow sharing with groups"}},
		{name: "the worst finding wins and every finding is said", edit: func(s *sharingPolicySettings) {
			s.AllowGroupSharing = false
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsBlocklist, []string{"robots"}
		}, groups: []string{"robots"}, state: "needs_action", code: "sharing_disabled_for_account",
			mentions: []string{"Allow sharing with groups", "`robots`"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			settings := defaultSharingPolicy()
			if tc.edit != nil {
				tc.edit(&settings)
			}
			got := evaluateSharingPolicy(settings, tc.groups)
			if got.State != tc.state || got.Code != tc.code {
				t.Fatalf("verdict = %s/%s, want %s/%s (%s)", got.State, got.Code, tc.state, tc.code, got.Message)
			}
			for _, want := range tc.mentions {
				if !strings.Contains(got.Message, want) && !stepsMention(got.Steps, want) {
					t.Errorf("finding does not mention %q: %s", want, got.Message)
				}
			}
			if tc.shareable != nil && !reflect.DeepEqual(got.ShareableGroups, tc.shareable) {
				t.Errorf("shareable groups = %v, want %v", got.ShareableGroups, tc.shareable)
			}
			if got.State == "passed" {
				if len(got.Steps) != 0 {
					t.Errorf("a passing finding carries steps: %+v", got.Steps)
				}
				return
			}
			if len(got.Steps) == 0 {
				t.Fatal("a finding that wants attention says nothing about what to do")
			}
			// Remedies are instructions for Nextcloud's own settings: Cassini
			// never changes sharing permissions or group memberships itself.
			// Each is a full sentence.
			for _, step := range got.Steps {
				if !strings.HasSuffix(step.Label, ".") {
					t.Errorf("step is not a full sentence: %q", step.Label)
				}
				if !strings.Contains(step.Label, "Administration settings › Sharing") && !strings.Contains(step.Label, "Users page") &&
					!strings.Contains(step.Label, "“Ignore the following groups") && !strings.Contains(step.Label, "add `cassini` to") {
					t.Errorf("step does not send the reader to Nextcloud's settings: %q", step.Label)
				}
			}
			// VERDICT, CAUSE, CONSEQUENCE: a failing verdict says nobody
			// receives recordings and that nothing new is published.
			if got.State == "needs_action" && (!strings.HasPrefix(got.Message, "Nobody can receive recordings. ") || !strings.Contains(got.Message, "no new meetings can be published.")) {
				t.Errorf("failing verdict does not read verdict/cause/consequence: %s", got.Message)
			}
		})
	}
}

func stepsMention(steps []readinessStep, want string) bool {
	for _, step := range steps {
		if strings.Contains(step.Label, want) {
			return true
		}
	}
	return false
}

// The exclude list as ShareDisableChecker reads it: json_decode($raw, true),
// and the comma form only where that is null. The object form is the one
// Nextcloud writes itself after a listed group is deleted.
func TestDecodeExcludeGroupsListMirrorsNextcloud(t *testing.T) {
	for raw, want := range map[string][]string{
		"":                      nil,
		`["a","b"]`:             {"a", "b"},
		`["a","a",""]`:          {"a"},
		`[]`:                    {},
		`{"1":"robots"}`:        {"robots"},
		`{"0":"a","2":"b"}`:     {"a", "b"},
		`{"10":"j","2":"b"}`:    {"b", "j"},
		`["group with, comma"]`: {"group with, comma"},
		// Not JSON: the comma-separated form older versions stored, split
		// as explode does — untrimmed.
		"a,b,,c": {"a", "b", "c"},
		"a, b":   {"a", " b"},
		"single": {"single"},
	} {
		if got := decodeExcludeGroupsList(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("decodeExcludeGroupsList(%q) = %#v, want %#v", raw, got, want)
		}
	}
}

// The ignore list as the share manager reads it: json_decode($raw, true) ?? [].
// A value that is not JSON ignores nothing — Nextcloud has no comma form here.
func TestDecodeIgnoredGroupsListIgnoresNothingThatIsNotJSON(t *testing.T) {
	for raw, want := range map[string][]string{
		"":               nil,
		"everyone":       nil,
		"a,b":            nil,
		`["everyone"]`:   {"everyone"},
		`{"1":"robots"}`: {"robots"},
	} {
		if got := decodeIgnoredGroupsList(raw); !reflect.DeepEqual(got, want) {
			t.Errorf("decodeIgnoredGroupsList(%q) = %#v, want %#v", raw, got, want)
		}
	}
}

// The verdicts those forms lead to, end to end through the settings.
func TestSharingPolicyReadsTheListsNextcloudActuallyEnforces(t *testing.T) {
	// A blocklist Nextcloud rewrote as an object after "guests" was deleted
	// still excludes cassini.
	excluded := evaluateSharingPolicy(sharingPolicySettings{APIEnabled: true, AllowGroupSharing: true,
		ExcludeGroupsMode: excludeGroupsBlocklist, ExcludeGroupsList: decodeExcludeGroupsList(`{"1":"robots"}`)}, []string{"robots"})
	if excluded.State != "needs_action" || excluded.Code != "sharing_disabled_for_account" {
		t.Fatalf("object-form blocklist = %s/%s", excluded.State, excluded.Code)
	}
	// An ignore list set by hand as plain text ignores nothing, so cassini can
	// still share through its own group.
	restricted := evaluateSharingPolicy(sharingPolicySettings{APIEnabled: true, AllowGroupSharing: true,
		OnlyGroupMembers: true, IgnoredGroups: decodeIgnoredGroupsList("staff")}, []string{"staff"})
	if restricted.State != "warn" || !reflect.DeepEqual(restricted.ShareableGroups, []string{"staff"}) {
		t.Fatalf("plain-text ignore list = %s/%s %v", restricted.State, restricted.Code, restricted.ShareableGroups)
	}
}

func TestExcludeGroupsModeFollowsNextcloud(t *testing.T) {
	for raw, want := range map[string]string{
		"": excludeGroupsOff, "no": excludeGroupsOff,
		"yes": excludeGroupsBlocklist, "allow": excludeGroupsAllowlist,
		// ShareDisableChecker treats anything but "no" and "allow" as a
		// blocklist, compared exactly.
		"1": excludeGroupsBlocklist, " allow": excludeGroupsBlocklist,
	} {
		if got := excludeGroupsMode(raw); got != want {
			t.Errorf("excludeGroupsMode(%q) = %q, want %q", raw, got, want)
		}
	}
}

// sharingPolicyServer answers the app-config and group reads, recording who
// asked for each.
type sharingPolicyServer struct {
	mu      sync.Mutex
	values  map[string]string // key -> raw JSON for ocs.data.data
	groups  []string
	refuse  map[string]int // path suffix -> HTTP status
	callers map[string]string
}

func (s *sharingPolicyServer) caller(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callers[path]
}

func (s *sharingPolicyServer) callerSnapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for path, actor := range s.callers {
		out[path] = actor
	}
	return out
}

func (s *sharingPolicyServer) serve(w http.ResponseWriter, r *http.Request) bool {
	const configPrefix = "/ocs/v2.php/apps/provisioning_api/api/v1/config/apps/core/"
	isConfig := strings.HasPrefix(r.URL.Path, configPrefix)
	isGroups := r.URL.Path == "/ocs/v2.php/cloud/users/cassini/groups"
	if !isConfig && !isGroups {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.callers == nil {
		s.callers = map[string]string{}
	}
	auth, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
	actor, _, _ := strings.Cut(string(auth), ":")
	s.callers[r.URL.Path] = actor
	for suffix, code := range s.refuse {
		if strings.HasSuffix(r.URL.Path, suffix) {
			w.WriteHeader(code)
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":`+fmt.Sprint(code)+`,"message":"refused"},"data":[]}}`)
			return true
		}
	}
	if isGroups {
		raw, _ := json.Marshal(s.groups)
		_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{"groups":`+string(raw)+`}}}`)
		return true
	}
	value, ok := s.values[strings.TrimPrefix(r.URL.Path, configPrefix)]
	if !ok {
		value = `""`
	}
	_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":{"data":`+value+`}}}`)
	return true
}

func TestReadSharingPolicyReadsAsTheResolvedAdministratorAndAppliesDefaults(t *testing.T) {
	fake := &sharingPolicyServer{
		values: map[string]string{
			ncOnlyGroupMembersKey:     `"yes"`,
			ncOnlyGroupMembersSkipKey: `"[\"everyone\"]"`,
			ncExcludeGroupsKey:        `"allow"`,
			ncExcludeGroupsListKey:    `"sharers,staff"`,
			// Unset keys come back empty or null: Nextcloud's defaults apply.
			ncAllowGroupSharingKey: `null`,
		},
		groups: []string{"staff", "everyone"},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !fake.serve(w, r) {
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	settings, groups, err := testExAppConfig(srv.URL).readSharingPolicy(context.Background(), srv.Client(), "sysop")
	if err != nil {
		t.Fatalf("readSharingPolicy: %v", err)
	}
	want := sharingPolicySettings{
		APIEnabled: true, OnlyGroupMembers: true, IgnoredGroups: []string{"everyone"},
		ExcludeGroupsMode: excludeGroupsAllowlist, ExcludeGroupsList: []string{"sharers", "staff"},
		AllowGroupSharing: true,
	}
	if !reflect.DeepEqual(settings, want) {
		t.Fatalf("settings = %+v\nwant       %+v", settings, want)
	}
	if !reflect.DeepEqual(groups, []string{"staff", "everyone"}) {
		t.Fatalf("groups = %v", groups)
	}
	callers := fake.callerSnapshot()
	if len(callers) != 7 {
		t.Fatalf("expected six settings and one group read, saw %d: %v", len(callers), callers)
	}
	for path, actor := range callers {
		if actor != "sysop" {
			t.Errorf("%s was read as %q, want the resolved administrator", path, actor)
		}
	}
}

// A read that failed establishes nothing: it is reported as not checked, never
// as a pass and never as a failure.
func TestSharingPolicyThatCannotBeReadIsNotVerified(t *testing.T) {
	for name, refuse := range map[string]map[string]int{
		"a setting":        {ncExcludeGroupsKey: http.StatusForbidden},
		"cassini's groups": {"/cassini/groups": http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			resetDirectSubstrate(t)
			fake := &sharingPolicyServer{groups: []string{"staff"}, refuse: refuse}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !fake.serve(w, r) {
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			testExAppConfig(srv.URL).checkSharingPolicy(context.Background(), srv.Client(), "admin", log.New(io.Discard, "", 0))
			finding, checked := ncAccessSubstrate.sharingPolicyFinding()
			if !checked || finding.State != "not_verified" || finding.Code != "sharing_policy_unreadable" || finding.CheckedAt == "" {
				t.Fatalf("finding = %+v (checked=%v)", finding, checked)
			}
			row := sharingPolicyRow(finding, checked)
			if row.Action != "recheck" || len(row.Steps) == 0 {
				t.Fatalf("an unreadable policy offers no way forward: %+v", row)
			}
		})
	}
}

// directPreflightServer is a Nextcloud just complete enough for
// preflightDirectShares to run end to end.
func directPreflightServer(t *testing.T, policy *sharingPolicyServer) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if policy.serve(w, r) {
			return
		}
		switch {
		case r.URL.Path == "/ocs/v2.php/apps/app_api/api/v1/users":
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":["admin","cassini"]}}`)
		case r.URL.Path == "/ocs/v2.php/cloud/groups/admin":
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":["admin"]}}}`)
		case r.URL.Path == "/ocs/v2.php/cloud/users/cassini":
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"id":"cassini"}}}`)
		case r.Method == "MKCOL":
			w.WriteHeader(http.StatusCreated)
		case r.Method == "PROPFIND":
			w.WriteHeader(http.StatusMultiStatus)
			_, _ = io.WriteString(w, `<?xml version="1.0"?><d:multistatus xmlns:d="DAV:" xmlns:oc="http://owncloud.org/ns" xmlns:nc="http://nextcloud.org/ns"><d:response><d:href>/</d:href><d:propstat><d:prop><oc:owner-id>cassini</oc:owner-id><nc:mount-type></nc:mount-type></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response></d:multistatus>`)
		case r.URL.Path == "/ocs/v2.php/apps/files_sharing/api/v1/shares":
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"status":"ok","statuscode":200},"data":[]}}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// The customer's report, end to end: sharing restricted to group members and
// `cassini` in no group. Storage itself is fine — the account exists and its
// folder is writable — so storage stays usable and recording stays admitted,
// while the new row fails and names the setting.
func TestPreflightReportsSharingPolicyWithoutGatingStorage(t *testing.T) {
	resetDirectSubstrate(t)
	resetProvisioningUser(t)
	t.Setenv(envNCAdminUser, "")
	policy := &sharingPolicyServer{values: map[string]string{ncOnlyGroupMembersKey: `"yes"`}, groups: []string{}}
	srv := directPreflightServer(t, policy)

	testExAppConfig(srv.URL).preflightDirectShares(context.Background(), log.New(io.Discard, "", 0))

	if !ncAccessSubstrate.usable() {
		t.Fatalf("a sharing policy made storage unusable: %+v", ncAccessSubstrate.snapshot(publishSinkNextcloudFiles))
	}
	if refusal := ncAccessSubstrate.recordingRefusal(); refusal != "" {
		t.Fatalf("a sharing policy blocked recording admission: %q", refusal)
	}
	finding, checked := ncAccessSubstrate.sharingPolicyFinding()
	if !checked || finding.State != "needs_action" || finding.Code != "sharing_no_common_group" {
		t.Fatalf("finding = %+v (checked=%v)", finding, checked)
	}
	if actor := policy.caller("/ocs/v2.php/apps/provisioning_api/api/v1/config/apps/core/" + ncOnlyGroupMembersKey); actor != "admin" {
		t.Fatalf("the policy was read as %q, want the resolved administrator", actor)
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.SharingPolicy == nil || snap.SharingPolicy.Code != "sharing_no_common_group" || snap.SharingPolicy.CheckedAt == "" {
		t.Fatalf("/status does not carry the finding: %+v", snap.SharingPolicy)
	}

	// A fixed policy is picked up by the next check.
	policy.mu.Lock()
	policy.values[ncOnlyGroupMembersKey] = `"no"`
	policy.mu.Unlock()
	testExAppConfig(srv.URL).preflightDirectShares(context.Background(), log.New(io.Discard, "", 0))
	if finding, _ := ncAccessSubstrate.sharingPolicyFinding(); finding.State != "passed" {
		t.Fatalf("a repaired policy still reports %+v", finding)
	}
}

func sharingRow(t *testing.T, rt *Runtime) (readinessCheck, readinessResponse) {
	t.Helper()
	report := rt.readiness(context.Background())
	for _, c := range report.Checks {
		if c.ID == sharingRowID {
			return c, report
		}
	}
	t.Fatalf("no %s row in %+v", sharingRowID, report.Checks)
	return readinessCheck{}, report
}

func TestReadinessReportsTheRecordingSharingRow(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ncAccessSubstrate.record(ncSubstrateProvisioned, "test preflight", nil)

	// Storage checked, sharing not yet: an absence, with a way to check.
	row, _ := sharingRow(t, rt)
	if row.State != "not_verified" || row.Code != "sharing_not_checked" || !row.Checkable || row.Probe != "storage" {
		t.Fatalf("unchecked sharing row = %+v", row)
	}

	ncAccessSubstrate.setSharingPolicy(evaluateSharingPolicy(sharingPolicySettings{APIEnabled: true, AllowGroupSharing: true, OnlyGroupMembers: true}, []string{"admin"}))
	row, report := sharingRow(t, rt)
	if row.State != "warn" || row.Code != "sharing_limited_to_groups" || len(row.Steps) == 0 || row.CheckedAt == "" || !row.Checkable {
		t.Fatalf("warning row = %+v", row)
	}
	if report.RecordingState != "warn" {
		t.Fatalf("a warning moved the recording verdict to %q", report.RecordingState)
	}

	// A proven total failure is the instance's verdict too — that is what tells
	// people without access why their meetings are not appearing — but it never
	// becomes an admission refusal.
	ncAccessSubstrate.setSharingPolicy(evaluateSharingPolicy(sharingPolicySettings{APIEnabled: true, AllowGroupSharing: true, OnlyGroupMembers: true}, nil))
	row, report = sharingRow(t, rt)
	if row.State != "needs_action" || report.RecordingState != "needs_action" {
		t.Fatalf("failing row = %+v, verdict %q", row, report.RecordingState)
	}
	if refusal := ncAccessSubstrate.recordingRefusal(); refusal != "" {
		t.Fatalf("recording admission refused on the sharing policy: %q", refusal)
	}

	// Ordered directly under the storage row it depends on.
	for i, c := range report.Checks {
		if c.ID == "storage" {
			if i+1 >= len(report.Checks) || report.Checks[i+1].ID != sharingRowID {
				t.Fatalf("sharing row is not directly under storage: %+v", report.Checks)
			}
		}
	}
}

func TestRecordingSharingWaitsOnAFailingStorageRow(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ncAccessSubstrate.setSharingPolicy(evaluateSharingPolicy(defaultSharingPolicy(), nil))
	ncAccessSubstrate.record(ncSubstrateUnavailable, "owner_account", nil)
	row, _ := sharingRow(t, rt)
	if row.Code != "check_blocked" || !strings.Contains(row.Message, "Recording storage") || row.Checkable {
		t.Fatalf("sharing row behind failing storage = %+v", row)
	}
}

func TestRecordingSharingRowIsAbsentWhereStorageIsNotNextcloud(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ncAccessSubstrate.mu.Lock()
	ncAccessSubstrate.applicable = false
	ncAccessSubstrate.mu.Unlock()
	ncAccessSubstrate.setSharingPolicy(evaluateSharingPolicy(defaultSharingPolicy(), nil))
	for _, c := range rt.readiness(context.Background()).Checks {
		if c.ID == sharingRowID {
			t.Fatalf("sharing row reported for a non-Nextcloud publish sink: %+v", c)
		}
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.SharingPolicy != nil {
		t.Fatalf("/status carries a sharing finding that does not apply: %+v", snap.SharingPolicy)
	}
}

// Where Recording sharing needs action no share can be made, not even to the
// administrator running a test recording, so the test is not offered. A
// warning leaves the test's own starter reachable, so it does not block.
func TestTestRecordingWaitsOnlyOnASharingPolicyThatShareNothing(t *testing.T) {
	for _, tc := range []struct {
		state, code string
		blocked     bool
	}{
		{"needs_action", "sharing_no_common_group", true},
		{"warn", "sharing_limited_to_groups", false},
		{"passed", "sharing_permitted", false},
	} {
		checks := []readinessCheck{
			{ID: "storage", State: "passed", Code: "storage_ready"},
			{ID: sharingRowID, State: tc.state, Code: tc.code},
			{ID: "talk.hpb", State: "passed", Code: "hpb_authenticated"},
			{ID: "talk.discovery", State: "passed", Code: "recording_auth_verified"},
			{ID: "test", State: "not_verified", Code: "test_not_run", Action: "test_recording"},
		}
		sortReadinessRows(checks)
		suppressBlockedRows(checks)
		for _, c := range checks {
			if c.ID != "test" {
				continue
			}
			if blocked := c.Code == "check_blocked"; blocked != tc.blocked {
				t.Fatalf("%s sharing: test row = %+v, want blocked=%v", tc.state, c, tc.blocked)
			}
			if tc.blocked && (c.Action != "" || !strings.Contains(c.Message, "Recording sharing")) {
				t.Fatalf("blocked test row does not name Recording sharing or still offers a test: %+v", c)
			}
		}
	}
}

// Running out of time is not a reachability or permissions fault, and is not
// reported as one.
func TestSharingPolicyReadThatTimesOutSaysSo(t *testing.T) {
	timedOut := unreadableSharingPolicy(fmt.Errorf("read %s: %w", ncExcludeGroupsKey, context.DeadlineExceeded))
	if timedOut.State != "not_verified" || timedOut.Code != "sharing_policy_check_timed_out" || strings.Contains(timedOut.Steps[0].Label, "administrator") {
		t.Fatalf("timed-out read = %+v", timedOut)
	}
	refused := unreadableSharingPolicy(fmt.Errorf("read %s: HTTP 403", ncExcludeGroupsKey))
	if refused.Code != "sharing_policy_unreadable" {
		t.Fatalf("refused read = %+v", refused)
	}
}

// The exact texts the Recording sharing row shows (D-885). The app's Doctor
// preview repeats them, so a change here is a change there.
func TestSharingPolicyWordingIsExact(t *testing.T) {
	const (
		lift    = "Turn off “Restrict users to only share with users in their groups” under Administration settings › Sharing."
		joinOne = "Or add `cassini` to a group everyone who records is in. It does not need to be an administrator."
		joinAll = "Or add `cassini` to every group whose members should receive recordings. A group everyone is in covers everybody; `cassini` does not need to be an administrator."
	)
	for name, tc := range map[string]struct {
		edit    func(*sharingPolicySettings)
		groups  []string
		message string
		steps   []string
	}{
		"share API off": {edit: func(s *sharingPolicySettings) { s.APIEnabled = false },
			message: "Nobody can receive recordings. Nextcloud's “Allow apps to use the Share API” is off, so every share Cassini makes is refused and no new meetings can be published.",
			steps:   []string{"Turn on “Allow apps to use the Share API” under Administration settings › Sharing."}},
		"excluded group": {edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsBlocklist, []string{"robots"}
		}, groups: []string{"robots"},
			message: "Nobody can receive recordings. Nextcloud's “Exclude some groups from sharing” lists “robots”, and Cassini shares from its cassini account, which is in that group. Every share is refused, so no new meetings can be published.",
			steps:   []string{"Remove `robots` from the excluded groups under Administration settings › Sharing.", "Or take `cassini` out of `robots` on Nextcloud's Users page."}},
		"several excluded groups": {edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsBlocklist, []string{"robots", "guests"}
		}, groups: []string{"robots", "guests"},
			message: "Nobody can receive recordings. Nextcloud's “Exclude some groups from sharing” lists “robots”, “guests”, and Cassini shares from its cassini account, which is in those groups. Every share is refused, so no new meetings can be published.",
			steps:   []string{"Remove `robots`, `guests` from the excluded groups under Administration settings › Sharing.", "Or take `cassini` out of `robots`, `guests` on Nextcloud's Users page."}},
		"allow list without cassini": {edit: func(s *sharingPolicySettings) {
			s.ExcludeGroupsMode, s.ExcludeGroupsList = excludeGroupsAllowlist, []string{"sharers", "staff"}
		}, groups: []string{"robots"},
			message: "Nobody can receive recordings. Nextcloud's “Limit sharing to some groups” allows only “sharers”, “staff”, and Cassini shares from its cassini account, which is in none of them. Every share is refused, so no new meetings can be published.",
			steps:   []string{"Add `cassini` to one of `sharers`, `staff` on Nextcloud's Users page.", "Or allow sharing for everyone under Administration settings › Sharing."}},
		"empty allow list": {edit: func(s *sharingPolicySettings) { s.ExcludeGroupsMode = excludeGroupsAllowlist }, groups: []string{"staff"},
			message: "Nobody can receive recordings. Nextcloud's “Limit sharing to some groups” allows no group at all, so no account may share. Every share is refused, so no new meetings can be published.",
			steps:   []string{"Choose the groups allowed to share under Administration settings › Sharing, including one `cassini` is in."}},
		"restricted, cassini in no group": {edit: func(s *sharingPolicySettings) { s.OnlyGroupMembers = true },
			message: "Nobody can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its cassini account, which is in no group. Every share is refused, so no new meetings can be published.",
			steps:   []string{lift, joinOne}},
		"restricted, every group ignored": {edit: func(s *sharingPolicySettings) { s.OnlyGroupMembers, s.IgnoredGroups = true, []string{"everyone"} }, groups: []string{"everyone"},
			message: "Nobody can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and every group the cassini account is in (“everyone”) is ignored when Nextcloud checks membership. Every share is refused, so no new meetings can be published.",
			steps:   []string{lift, joinOne, "Or stop ignoring `everyone` under “Ignore the following groups when checking group membership”."}},
		"restricted to some groups": {edit: func(s *sharingPolicySettings) { s.OnlyGroupMembers = true }, groups: []string{"staff", "engineering"},
			message: "Only members of “staff”, “engineering” can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its cassini account, which is only in “staff”, “engineering”. Anyone outside these groups cannot open the recordings of their meetings, and a recording they start is not published at all.",
			steps:   []string{lift, joinAll}},
		"restricted to administrators": {edit: func(s *sharingPolicySettings) { s.OnlyGroupMembers = true }, groups: []string{"admin"},
			message: "Only administrators can receive recordings. Nextcloud's “Restrict users to only share with users in their groups” is on, and Cassini shares from its cassini account, which is only in “admin”, the administrators' group. Anyone who is not an administrator cannot open the recordings of their meetings, and a recording they start is not published at all.",
			steps:   []string{lift, joinAll}},
		"group sharing off": {edit: func(s *sharingPolicySettings) { s.AllowGroupSharing = false },
			message: "Groups in a room do not receive recordings. Nextcloud's “Allow sharing with groups” is off, so a meeting whose room includes a group is published without access for that group's members, unless they are in the room themselves.",
			steps:   []string{"Turn on “Allow sharing with groups” under Administration settings › Sharing."}},
		"passed": {
			message: "Nextcloud's sharing settings let Cassini share recordings with everyone in a meeting. Whether each person has accepted the share is theirs to decide; anyone with recordings waiting sees them in Browse."},
	} {
		t.Run(name, func(t *testing.T) {
			settings := defaultSharingPolicy()
			if tc.edit != nil {
				tc.edit(&settings)
			}
			got := evaluateSharingPolicy(settings, tc.groups)
			if got.Message != tc.message {
				t.Errorf("message\n got: %s\nwant: %s", got.Message, tc.message)
			}
			labels := make([]string, len(got.Steps))
			for i, step := range got.Steps {
				labels[i] = step.Label
			}
			if (len(labels) != 0 || len(tc.steps) != 0) && !reflect.DeepEqual(labels, tc.steps) {
				t.Errorf("steps\n got: %q\nwant: %q", labels, tc.steps)
			}
		})
	}
}

// The row says what is currently unshared, in every state: a setting fixed
// today leaves yesterday's meetings unshared until they are re-shared.
func TestRecordingSharingRowCountsWhatIsUnshared(t *testing.T) {
	resetDirectSubstrate(t)
	rt, cleanup := readinessRuntime(t)
	defer cleanup()
	ctx := context.Background()
	ncAccessSubstrate.record(ncSubstrateProvisioned, "test preflight", nil)
	if row, _ := sharingRow(t, rt); row.Unshared != nil {
		t.Fatalf("nothing unshared yet, row carries %+v", row.Unshared)
	}
	record := func(id, status string, skipped ...skippedShare) {
		t.Helper()
		if err := rt.store.RecordJobShareOutcome(ctx, id, status, skipped); err != nil {
			t.Fatal(err)
		}
	}
	// J1 was recorded on 12 October and re-shared today: since is the
	// meeting's day, not the re-share's.
	seedJobRow(t, rt.store.db, seededJobRow{ID: "J1", Stage: "done", State: "succeeded", CreatedAt: "2026-10-12T09:00:00Z"})
	setJobTimes(t, rt, "J1", "2026-10-12T09:14:00.000000000Z", "2026-10-12T10:00:00.000000000Z")
	record("J1", shareOutcomePartial, skippedShare{Type: "user", ID: "bob"}, skippedShare{Type: "group", ID: "staff"})
	record("J2", shareOutcomePartial, skippedShare{Type: "user", ID: "bob"}, skippedShare{Type: "user", ID: "carol"}, skippedShare{Type: "circle", ID: "sales"})
	record("J3", shareOutcomeComplete)
	record("J4", shareOutcomeStarterRefused, skippedShare{Type: "user", ID: "dave"})
	// An expired meeting is nobody's any more.
	record("J5", shareOutcomePartial, skippedShare{Type: "user", ID: "erin"})
	retireMeeting(t, rt, "J5.opus")
	want := &unsharedSummary{Jobs: 2, Users: 2, Groups: 1, Since: "2026-10-12T09:14:00Z"}

	// Not checked yet.
	row, _ := sharingRow(t, rt)
	if row.Code != "sharing_not_checked" || !reflect.DeepEqual(row.Unshared, want) {
		t.Fatalf("unchecked row = %+v unshared %+v", row, row.Unshared)
	}
	// Passing, warning and failing alike.
	for _, groups := range [][]string{nil, {"admin"}, {}} {
		settings := defaultSharingPolicy()
		settings.OnlyGroupMembers = groups != nil
		ncAccessSubstrate.setSharingPolicy(evaluateSharingPolicy(settings, groups))
		if row, _ := sharingRow(t, rt); !reflect.DeepEqual(row.Unshared, want) {
			t.Fatalf("%s row carries %+v, want %+v", row.State, row.Unshared, want)
		}
	}
	// Even blocked behind a failing storage row.
	ncAccessSubstrate.record(ncSubstrateUnavailable, "owner_account", nil)
	if row, _ := sharingRow(t, rt); row.Code != "check_blocked" || !reflect.DeepEqual(row.Unshared, want) {
		t.Fatalf("blocked row = %+v unshared %+v", row, row.Unshared)
	}
}
