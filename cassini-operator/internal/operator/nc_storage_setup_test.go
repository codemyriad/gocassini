package operator

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func planIDs(steps []storageSetupStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.ID)
	}
	return out
}

func planStep(t *testing.T, steps []storageSetupStep, id string) storageSetupStep {
	t.Helper()
	for _, s := range steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no %q step in plan %v", id, planIDs(steps))
	return storageSetupStep{}
}

// A mode that is already available has nothing to set up. "Is there anything to
// do" and "what is there to do" have to be the same question, or the UI ends up
// offering a button that does nothing.
func TestSetupPlanIsEmptyForAModeThatIsReady(t *testing.T) {
	if steps := storageSetupPlan(true, readyProbe()); len(steps) != 0 {
		t.Fatalf("access-controlled plan = %v on a ready instance, want empty", planIDs(steps))
	}
	if steps := storageSetupPlan(false, readyProbe()); len(steps) != 0 {
		t.Fatalf("default plan = %v on a ready instance, want empty", planIDs(steps))
	}
}

// The default model needs exactly the two identity steps, and both are things
// the administrator's browser can do.
func TestSetupPlanForTheDefaultModeIsTheServiceAccount(t *testing.T) {
	probe := ncStorageProbe{AdminUser: "admin", FolderProbed: true}
	steps := storageSetupPlan(false, probe)

	if got, want := strings.Join(planIDs(steps), ","), "group,account"; got != want {
		t.Fatalf("plan = %q, want %q", got, want)
	}
	for _, step := range steps {
		if !step.Browser {
			t.Errorf("step %q is not marked browser-doable; both identity writes are non-strict", step.ID)
		}
		if step.Occ == "" || step.Title == "" {
			t.Errorf("step %q must carry both a command and a sentence: %+v", step.ID, step)
		}
	}
	if got := planStep(t, steps, "account").Args["user"]; got != ncRecordingsOwner {
		t.Fatalf("account step creates %q, want %q", got, ncRecordingsOwner)
	}
}

// The whole dance, in dependency order — identities, then the apps, then the
// folder, then what maps onto it. A plan that ordered these differently would
// fail halfway through against a real Nextcloud.
func TestSetupPlanForAccessControlIsOrderedByDependency(t *testing.T) {
	probe := ncStorageProbe{
		AdminUser: "admin",
		Prereqs: []ncPrerequisiteStatus{
			{Name: ncAppGroupFolders, State: ncPrerequisiteMissing},
			{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
		},
		FolderProbed: true,
	}
	steps := storageSetupPlan(true, probe)

	want := []string{
		"group", "account",
		"app:" + ncAppGroupFolders, "app:" + ncAppEveryoneGroup,
		"folder",
		"mount:" + ncRecordingsOwnerGroup, "mount:" + ncRecordingsEveryoneGroup,
		"acl", "manager",
	}
	if got := strings.Join(planIDs(steps), ","); got != strings.Join(want, ",") {
		t.Fatalf("plan order =\n  %s\nwant\n  %s", got, strings.Join(want, ","))
	}
}

// The line the browser cannot cross. Strict password confirmation wants the
// password on the request itself, so no session satisfies it — and marking an
// app step browser-doable would make the UI attempt something that always 403s.
func TestSetupPlanMarksOnlyTheAppInstallsAsBeyondTheBrowser(t *testing.T) {
	probe := ncStorageProbe{
		AdminUser: "admin",
		Prereqs: []ncPrerequisiteStatus{
			{Name: ncAppGroupFolders, State: ncPrerequisiteMissing},
			{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
		},
		FolderProbed: true,
	}
	for _, step := range storageSetupPlan(true, probe) {
		isApp := step.Action == setupActionEnableApp
		if isApp && step.Browser {
			t.Errorf("%q is strict; a browser session can never satisfy it", step.ID)
		}
		if !isApp && !step.Browser {
			t.Errorf("%q is non-strict and should be done by the browser", step.ID)
		}
		if isApp && step.AppURL == "" {
			t.Errorf("%q has nowhere to hand off to", step.ID)
		}
	}
}

// The folder-dependent steps are addressed by mount point, not by id: on a plan
// that also creates the folder there is no id yet, and baking in a stale one
// would map the groups onto somebody else's folder.
func TestSetupPlanAddressesTheFolderByMountWhenItDoesNotExistYet(t *testing.T) {
	probe := ncStorageProbe{AdminUser: "admin", ServiceAccount: true, FolderProbed: true}
	steps := storageSetupPlan(true, probe)

	for _, id := range []string{"mount:" + ncRecordingsOwnerGroup, "acl", "manager"} {
		step := planStep(t, steps, id)
		if step.Args["mount"] != ncRecordingsMount {
			t.Errorf("step %q does not name the mount point: %+v", id, step.Args)
		}
		if _, hasID := step.Args["id"]; hasID {
			t.Errorf("step %q carries a folder id that does not exist yet: %+v", id, step.Args)
		}
	}
	// With a folder already present, the printed commands can name its real id.
	withFolder := readyProbe()
	withFolder.ACLEnabled = false
	if got := planStep(t, storageSetupPlan(true, withFolder), "acl").Occ; !strings.Contains(got, " 7 ") {
		t.Fatalf("occ line %q does not use the folder's real id", got)
	}
}

// The printed recipe is derived from the plan rather than maintained beside it.
// They drifted before: the documentation promised an order the UI did not emit.
func TestPrintedInstructionsAreDerivedFromThePlan(t *testing.T) {
	probe := ncStorageProbe{
		AdminUser: "admin",
		Prereqs: []ncPrerequisiteStatus{
			{Name: ncAppGroupFolders, State: ncPrerequisiteMissing},
			{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
		},
		FolderProbed: true,
	}
	plan := storageSetupPlan(true, probe)
	lines := storageModeInstructions(true, probe)

	var wanted []string
	for _, step := range plan {
		if step.Occ != "" {
			wanted = append(wanted, step.Occ)
		}
	}
	for i, want := range wanted {
		if i >= len(lines) || lines[i] != want {
			t.Fatalf("instruction %d = %q, want the plan's %q\nplan: %v\nlines: %v", i, lines[min(i, len(lines)-1)], want, planIDs(plan), lines)
		}
	}
	// A plan that needs a folder id it cannot know must say what `<id>` is.
	if !strings.Contains(strings.Join(lines, "\n"), "<id> is the folder id") {
		t.Fatalf("instructions leave `<id>` unexplained:\n%s", strings.Join(lines, "\n"))
	}
}

// --- the backend's app-install attempt ---------------------------------------

// installOutcomeMock answers the app-enable route with one canned status.
func installOutcomeMock(t *testing.T, status int, body string) ExAppConfig {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/cloud/apps/") {
			w.WriteHeader(status)
			io.WriteString(w, body)
			return
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`)
	}))
	t.Cleanup(srv.Close)
	return testExAppConfig(srv.URL)
}

// The three outcomes an administrator does something different about. Reporting
// them as one "it failed" is what turned this into two consecutive dead ends
// discovered one at a time.
func TestAppInstallOutcomesAreDistinguished(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantOK     bool
		wantReason string
		wantInText string
	}{
		{"enabled", http.StatusOK, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`, true, appInstallEnabled, ""},
		{"password confirmation", http.StatusForbidden,
			`{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Required authorization header missing"},"data":[]}}`,
			false, appInstallNeedsPassword, "will not ask for"},
		{"app store", http.StatusNotFound,
			`{"ocs":{"meta":{"status":"failure","statuscode":998,"message":"The request app was not found"},"data":[]}}`,
			false, appInstallStoreProblem, "five minutes"},
		{"anything else", http.StatusInternalServerError, `boom`, false, appInstallFailed, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := installOutcomeMock(t, tc.status, tc.body)
			got := cfg.installApp(context.Background(), &http.Client{}, ncAppGroupFolders, log.New(io.Discard, "", 0))

			if got.OK != tc.wantOK || got.Reason != tc.wantReason {
				t.Fatalf("outcome = {ok:%t reason:%q}, want {ok:%t reason:%q}", got.OK, got.Reason, tc.wantOK, tc.wantReason)
			}
			if tc.wantInText != "" && !strings.Contains(got.Detail, tc.wantInText) {
				t.Fatalf("detail %q does not explain the situation (looking for %q)", got.Detail, tc.wantInText)
			}
		})
	}
}

// The App Store answers the same opaque 404 for four different causes, so the
// message must never claim to know which — and must not invite an immediate
// retry, which Nextcloud's own five-minute failure cache would fail identically.
func TestAppStoreFailureDoesNotClaimToKnowWhy(t *testing.T) {
	cfg := installOutcomeMock(t, http.StatusNotFound,
		`{"ocs":{"meta":{"status":"failure","statuscode":998,"message":"The request app was not found"},"data":[]}}`)
	got := cfg.installApp(context.Background(), &http.Client{}, ncAppGroupFolders, log.New(io.Discard, "", 0))

	lower := strings.ToLower(got.Detail)
	for _, forbidden := range []string{"unreachable store", "the app store is down", "network error"} {
		if strings.Contains(lower, forbidden) {
			t.Errorf("detail claims a cause it cannot know (%q): %s", forbidden, got.Detail)
		}
	}
	if !strings.Contains(lower, "cannot tell you which") {
		t.Errorf("detail does not admit the ambiguity: %s", got.Detail)
	}
}

// Only the missing ones are attempted: re-enabling an app that is already there
// is a wasted App Store round-trip on every run of the setup flow.
func TestInstallMissingAppsSkipsTheOnesThatAreAlreadyEnabled(t *testing.T) {
	var attempted []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/cloud/apps/") {
			attempted = append(attempted, r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:])
		}
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`)
	}))
	defer srv.Close()

	probe := ncStorageProbe{Prereqs: []ncPrerequisiteStatus{
		{Name: ncAppGroupFolders, State: ncPrerequisiteEnabled},
		{Name: ncAppEveryoneGroup, State: ncPrerequisiteMissing},
	}}
	out := testExAppConfig(srv.URL).installMissingApps(context.Background(), &http.Client{}, probe, log.New(io.Discard, "", 0))

	if len(out) != 1 || out[0].App != ncAppEveryoneGroup {
		t.Fatalf("outcomes = %+v, want only the missing app", out)
	}
	if len(attempted) != 1 || attempted[0] != ncAppEveryoneGroup {
		t.Fatalf("attempted %v, want only %q", attempted, ncAppEveryoneGroup)
	}
}
