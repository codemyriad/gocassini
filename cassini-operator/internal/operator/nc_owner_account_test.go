package operator

import (
	"context"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// ownerAccountMock is a Nextcloud's user-administration surface and nothing
// else: the three writes the attempt makes, and the two reads that check them.
//
// It is separate from provisionMock and storageMock on purpose. Those model a
// whole instance; this one models the one thing that differs between Nextcloud
// releases here — whether `POST /cloud/users` is allowed at all, and which
// shape the refusal arrives in.
type ownerAccountMock struct {
	mu   sync.Mutex
	reqs []recordedReq

	// user and group are what the instance already has. The writes update them,
	// so a create followed by the attempt's own read-back tells the truth.
	user   bool
	group  bool
	member bool

	// refuse decides how EVERY user-administration write answers:
	//
	//	""          it works
	//	"envelope"  HTTP 200 carrying the failure only in ocs.meta — the shape
	//	            that reads as a success to anything checking the status
	//	"http"      HTTP 403 with the same envelope, which is what the
	//	            provisioning API answers an ExApp on Nextcloud 34.0.2+
	refuse string
	// refuseGroupWrite refuses only `POST /cloud/groups`, for the half-created
	// shapes where the account is there and its group is not.
	refuseGroupWrite bool
}

const passwordConfirmationEnvelope = `{"ocs":{"meta":{"status":"failure","statuscode":403,"message":"Password confirmation is required"},"data":[]}}`

func (m *ownerAccountMock) refuseWrite(w http.ResponseWriter, mode string) {
	if mode == "http" {
		w.WriteHeader(http.StatusForbidden)
	}
	io.WriteString(w, passwordConfirmationEnvelope)
}

func (m *ownerAccountMock) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		m.mu.Lock()
		m.reqs = append(m.reqs, recordedReq{method: r.Method, path: r.URL.Path, auth: r.Header.Get("AUTHORIZATION-APP-API"), body: string(body)})
		user, group, member, refuse, refuseGroup := m.user, m.group, m.member, m.refuse, m.refuseGroupWrite
		m.mu.Unlock()

		p := r.URL.Path
		switch {
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/users/"+ncRecordingsOwner:
			if !user {
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":998},"data":[]}}`)
				return
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"id":"`+ncRecordingsOwner+`"}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups":
			groups := []string{}
			if group {
				groups = append(groups, ncRecordingsOwnerGroup)
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"groups":`+jsonArray(groups)+`}}}`)
		case r.Method == http.MethodGet && p == "/ocs/v2.php/cloud/groups/"+ncRecordingsOwnerGroup:
			members := []string{}
			if member {
				members = append(members, ncRecordingsOwner)
			}
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":`+jsonArray(members)+`}}}`)

		// The writes. A refusal is answered BEFORE existence is evaluated,
		// which is what a real Nextcloud does (D-661) and the reason the
		// attempt reads back rather than believing the answer.
		case r.Method == http.MethodPost && p == "/ocs/v2.php/cloud/groups":
			if refuse != "" || refuseGroup {
				mode := refuse
				if mode == "" {
					mode = "http"
				}
				m.refuseWrite(w, mode)
				return
			}
			if group {
				io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":102,"message":"group exists"},"data":[]}}`)
				return
			}
			m.mu.Lock()
			m.group = true
			m.mu.Unlock()
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`)
		case r.Method == http.MethodPost && p == "/ocs/v2.php/cloud/users":
			if refuse != "" {
				m.refuseWrite(w, refuse)
				return
			}
			if user {
				io.WriteString(w, `{"ocs":{"meta":{"status":"failure","statuscode":102,"message":"User already exists"},"data":[]}}`)
				return
			}
			m.mu.Lock()
			m.user = true
			m.member = m.group
			m.mu.Unlock()
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`)
		case r.Method == http.MethodPost && p == "/ocs/v2.php/cloud/users/"+ncRecordingsOwner+"/groups":
			if refuse != "" {
				m.refuseWrite(w, refuse)
				return
			}
			m.mu.Lock()
			m.member = true
			m.mu.Unlock()
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`)
		default:
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":[]}}`)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (m *ownerAccountMock) count(method, path string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.reqs {
		if r.method == method && r.path == path {
			n++
		}
	}
	return n
}

func (m *ownerAccountMock) find(method, path string) (recordedReq, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.reqs {
		if r.method == method && r.path == path {
			return r, true
		}
	}
	return recordedReq{}, false
}

// runOwnerAccountAttempt runs one attempt against the mock, from the probe a
// preflight would have handed it, and returns everything the caller can see.
func runOwnerAccountAttempt(t *testing.T, mock *ownerAccountMock, probe ncStorageProbe) (ownerAccountOutcome, ncStorageProbe, string) {
	t.Helper()
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	var logs strings.Builder
	cfg := testExAppConfig(mock.server(t).URL)
	outcome := cfg.ensureServiceAccountOnEnable(context.Background(), &http.Client{}, &probe, log.New(&logs, "", 0))
	return outcome, probe, logs.String()
}

// The release family where this works: the account and its narrow owner group
// appear on enable and the install can record without anybody opening anything.
func TestOwnerAccountCreatedOnEnable(t *testing.T) {
	mock := &ownerAccountMock{}
	outcome, probe, logs := runOwnerAccountAttempt(t, mock, ncStorageProbe{AdminUser: "admin"})

	if outcome.Reason != ownerAccountCreated {
		t.Fatalf("outcome = %+v, want %q", outcome, ownerAccountCreated)
	}
	if !probe.ServiceAccount || !probe.OwnerGroup {
		t.Fatalf("probe was not amended with what the attempt created: account=%t group=%t", probe.ServiceAccount, probe.OwnerGroup)
	}
	if probe.ServiceAccountAttempt != "" {
		t.Errorf("a successful attempt left a refusal note: %q", probe.ServiceAccountAttempt)
	}
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Step == storageStepServiceAccount {
		t.Errorf("an account that was created is still reported missing: %+v", snap)
	}

	create, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users")
	if !ok {
		t.Fatal("the account was never created")
	}
	// "groups[]", not "groups": OCS decodes the field as a PHP array and a
	// scalar makes Nextcloud answer a bare 400, so the account is never made.
	if !strings.Contains(create.body, "groups%5B%5D="+ncRecordingsOwnerGroup) {
		t.Errorf("create body = %q, want the owner group in the PHP-array field", create.body)
	}

	// The password is minted, spent, and dropped. Nothing may carry it out of
	// the request that needed it.
	form, err := url.ParseQuery(create.body)
	if err != nil {
		t.Fatalf("parse create body: %v", err)
	}
	password := form.Get("password")
	if password == "" {
		t.Fatal("the account was created with no password at all")
	}
	for what, text := range map[string]string{
		"the log":            logs,
		"the outcome":        outcome.Reason + " " + outcome.Detail,
		"the probe":          probe.ServiceAccountAttempt,
		"the /status detail": ncAccessSubstrate.snapshot(publishSinkNextcloudFiles).Detail,
	} {
		if strings.Contains(text, password) {
			t.Errorf("the generated password reached %s", what)
		}
	}
}

// Nextcloud 34.0.2 and later refuse every user-administration write to an ExApp
// and say why, in two shapes: an HTTP 403, and an HTTP 200 whose ocs.meta
// carries the 403. Both are the same condition and both must read as a refusal
// — a create judged on the HTTP status alone reports the second as a success,
// and then every act-as-cassini call 401s for the life of the install.
func TestOwnerAccountRefusedWithPasswordConfirmation(t *testing.T) {
	for _, shape := range []string{"http", "envelope"} {
		t.Run(shape, func(t *testing.T) {
			mock := &ownerAccountMock{refuse: shape}
			outcome, probe, logs := runOwnerAccountAttempt(t, mock, ncStorageProbe{AdminUser: "admin"})

			if outcome.Reason != ownerAccountNeedsPassword {
				t.Fatalf("outcome = %+v, want %q", outcome, ownerAccountNeedsPassword)
			}
			if probe.ServiceAccount || probe.OwnerGroup {
				t.Fatalf("a refused create was recorded as having made something: account=%t group=%t", probe.ServiceAccount, probe.OwnerGroup)
			}
			// The remaining prerequisite, named the way the preflight already
			// names it, with the sentence that says who can finish it.
			snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
			if snap.Step != storageStepServiceAccount {
				t.Fatalf("step = %q, want %q", snap.Step, storageStepServiceAccount)
			}
			if !strings.Contains(snap.Detail, "browser") {
				t.Errorf("the detail does not say who can create it: %s", snap.Detail)
			}
			if !strings.Contains(snap.Detail, "Password confirmation") {
				t.Errorf("the detail does not name the refusal: %s", snap.Detail)
			}
			if !strings.Contains(logs, "Password confirmation") {
				t.Errorf("the log does not name the refusal: %s", logs)
			}
			// One attempt. A refusal is permanent on this release, and a retry
			// loop would put two writes per enable on an instance that will
			// never answer differently.
			if n := mock.count(http.MethodPost, "/ocs/v2.php/cloud/users"); n != 1 {
				t.Errorf("POST /cloud/users happened %d times, want exactly one attempt", n)
			}
			if n := mock.count(http.MethodPost, "/ocs/v2.php/cloud/groups"); n != 1 {
				t.Errorf("POST /cloud/groups happened %d times, want exactly one attempt", n)
			}
		})
	}
}

// The half-created state, which is the one this must never leave silently: the
// account is there and its owner group is not. The default model stores
// recordings perfectly well like that; the Team folder's write mount is onto
// the group, so access control does not.
func TestOwnerAccountReportsAnAccountWithoutItsGroup(t *testing.T) {
	// The account already exists (the probe could not confirm it, which is why
	// the attempt ran), and the group write is the one that is refused.
	mock := &ownerAccountMock{user: true, refuseGroupWrite: true}
	outcome, probe, logs := runOwnerAccountAttempt(t, mock, ncStorageProbe{AdminUser: "admin"})

	if outcome.Reason != ownerAccountGroupIncomplete {
		t.Fatalf("outcome = %+v, want %q", outcome, ownerAccountGroupIncomplete)
	}
	if !probe.ServiceAccount || probe.OwnerGroup {
		t.Fatalf("probe = account:%t group:%t, want the account present and the group missing", probe.ServiceAccount, probe.OwnerGroup)
	}
	if !strings.Contains(outcome.Detail, ncRecordingsOwnerGroup) || !strings.Contains(logs, ncRecordingsOwnerGroup) {
		t.Errorf("neither the outcome nor the log names the missing group: %s / %s", outcome.Detail, logs)
	}
	// An account that exists must never be reported missing, and the refusal
	// note belongs to the account, not the group.
	if snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles); snap.Step == storageStepServiceAccount {
		t.Errorf("an existing account was reported as the missing prerequisite: %+v", snap)
	}
	if probe.ServiceAccountAttempt != "" {
		t.Errorf("the account is there, so it carries no refusal note: %q", probe.ServiceAccountAttempt)
	}
	// This is how the two halves stay distinguishable everywhere else: the plan
	// asks for the group and nothing else.
	steps := storageSetupPlan(false, probe)
	if len(steps) != 1 || steps[0].Action != setupActionCreateGroup {
		t.Fatalf("setup plan = %+v, want just the create_group step", steps)
	}
}

// The same half, from the other side: both exist but the membership write is
// refused, so the account is in no group at all.
func TestOwnerAccountReportsARefusedMembership(t *testing.T) {
	mock := &ownerAccountMock{user: true, group: true, refuse: "http"}
	outcome, probe, _ := runOwnerAccountAttempt(t, mock, ncStorageProbe{AdminUser: "admin"})

	if outcome.Reason != ownerAccountGroupIncomplete {
		t.Fatalf("outcome = %+v, want %q", outcome, ownerAccountGroupIncomplete)
	}
	if !probe.ServiceAccount || !probe.OwnerGroup {
		t.Fatalf("probe = account:%t group:%t, want both present", probe.ServiceAccount, probe.OwnerGroup)
	}
	if !strings.Contains(outcome.Detail, "not a member") {
		t.Errorf("the outcome does not say which half is missing: %s", outcome.Detail)
	}
}

// An account that is already there is not created again, and nothing is written
// on a re-enable. The attempt costs one branch on every instance that has been
// set up, which is all of them after the first run.
func TestOwnerAccountWritesNothingWhenTheAccountExists(t *testing.T) {
	mock := &ownerAccountMock{user: true, group: true, member: true}
	outcome, probe, _ := runOwnerAccountAttempt(t, mock, ncStorageProbe{AdminUser: "admin", ServiceAccount: true, OwnerGroup: true})

	if outcome.Reason != ownerAccountPresent {
		t.Fatalf("outcome = %+v, want %q", outcome, ownerAccountPresent)
	}
	if !probe.ServiceAccount {
		t.Error("the attempt unset a fact it was not asked to change")
	}
	for _, path := range []string{"/ocs/v2.php/cloud/users", "/ocs/v2.php/cloud/groups"} {
		if n := mock.count(http.MethodPost, path); n != 0 {
			t.Errorf("POST %s happened %d times on an instance that is already set up", path, n)
		}
	}
}

// The account is what BOTH modes store recordings as, so the attempt happens
// before anything mode-dependent. A Nextcloud with neither native app and no
// recorded mode is the fresh install this exists for: it needs an account and
// nothing else, and waiting for a mode to be resolved first is how it would
// never get one.
func TestPreflightAttemptsTheServiceAccountBeforeTheModeIsResolved(t *testing.T) {
	mock := &storageMock{apps: []string{}}
	runStoragePreflight(t, mock, io.Discard)

	if !mock.saw(http.MethodPost, "/cloud/groups") {
		t.Error("the enabled edge never tried to create the owner group")
	}
	if !mock.saw(http.MethodPost, "/cloud/users") {
		t.Error("the enabled edge never tried to create the service account")
	}
}

// And it does not write on an instance whose account the probe already found,
// whichever mode that instance is in.
func TestPreflightDoesNotTouchAnExistingServiceAccount(t *testing.T) {
	mock := &storageMock{apps: []string{}, serviceAccount: true}
	runStoragePreflight(t, mock, io.Discard)

	if mock.saw(http.MethodPost, "/cloud/users") {
		t.Error("the enabled edge tried to create an account that was already there")
	}
}
