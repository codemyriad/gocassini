package operator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The old discovery signed its request as the very id it was discovering, so on
// an instance whose administrator is not named `admin` it 401'd, fell back to
// `admin` anyway, and every later call was rejected — silently. These tests pin
// the switch that replaces it, using the status codes a live Nextcloud 34
// actually returns (spike-x1):
//
//	2xx  this actor IS an administrator
//	403  the actor exists but is not one   (`cassini` proves this)
//	401  no such actor, or the act-as was refused
//	404  the provisioning route is absent — an instance fault, not an answer

func TestResolveAdminIdentityAcceptsTheConventionalAdministrator(t *testing.T) {
	resetProvisioningUser(t)
	mock := &provisionMock{folders: `[]`, groups: `[]`}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	admin, err := cfg.resolveAdminIdentity(context.Background(), srv.Client(), log.New(io.Discard, "", 0))
	if err != nil || admin != defaultNextcloudAdminUser {
		t.Fatalf("resolveAdminIdentity = %q, %v; want %q", admin, err, defaultNextcloudAdminUser)
	}
	if got := cfg.provisioningUser(); got != defaultNextcloudAdminUser {
		t.Fatalf("provisioningUser = %q, want the resolved administrator", got)
	}
}

// The point of the whole change: an instance whose administrator is named
// something else still provisions, because the candidate list is READ from the
// instance rather than guessed.
func TestResolveAdminIdentityFindsANonAdminNamedAdministratorThroughTheRoster(t *testing.T) {
	resetProvisioningUser(t)
	mock := &provisionMock{
		folders:     `[]`,
		groups:      `[]`,
		roster:      []string{"alice", "sysop", "bob"},
		adminList:   `["sysop"]`,
		adminActors: map[string]int{"sysop": http.StatusOK},
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	cfg := testExAppConfig(srv.URL)
	admin, err := cfg.resolveAdminIdentity(context.Background(), srv.Client(), log.New(io.Discard, "", 0))
	if err != nil || admin != "sysop" {
		t.Fatalf("resolveAdminIdentity = %q, %v; want sysop", admin, err)
	}
	// The roster call is the one request in the flow that presupposes no
	// identity at all, so it must be made app-scoped (empty actor).
	roster, ok := mock.find(http.MethodGet, "/apps/app_api/api/v1/users")
	if !ok {
		t.Fatal("the account roster was never enumerated")
	}
	raw, err := base64.StdEncoding.DecodeString(roster.auth)
	if err != nil {
		t.Fatalf("decode roster auth: %v", err)
	}
	if actor, _, _ := strings.Cut(string(raw), ":"); actor != "" {
		t.Fatalf("roster was fetched as %q, want the app-scoped (empty) identity", actor)
	}
}

// An account that exists but is not an administrator must be skipped, not
// treated as a hard failure — `cassini` itself is exactly that account.
func TestResolveAdminIdentitySkipsAnExistingNonAdministrator(t *testing.T) {
	resetProvisioningUser(t)
	mock := &provisionMock{
		folders:   `[]`,
		groups:    `[]`,
		roster:    []string{ncRecordingsOwner, "ops"},
		adminList: `["ops"]`,
		adminActors: map[string]int{
			defaultNextcloudAdminUser: http.StatusUnauthorized,
			ncRecordingsOwner:         http.StatusForbidden,
			"ops":                     http.StatusOK,
		},
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	admin, err := testExAppConfig(srv.URL).resolveAdminIdentity(context.Background(), srv.Client(), log.New(io.Discard, "", 0))
	if err != nil || admin != "ops" {
		t.Fatalf("resolveAdminIdentity = %q, %v; want ops", admin, err)
	}
}

// The explicit override wins, and it is what the failure message tells an
// administrator to reach for.
func TestResolveAdminIdentityPrefersTheConfiguredAdministrator(t *testing.T) {
	resetProvisioningUser(t)
	t.Setenv(envNCAdminUser, "chosen")
	mock := &provisionMock{
		folders:     `[]`,
		groups:      `[]`,
		roster:      []string{"admin", "chosen"},
		adminList:   `["chosen","admin"]`,
		adminActors: map[string]int{"chosen": http.StatusOK, "admin": http.StatusOK},
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	admin, err := testExAppConfig(srv.URL).resolveAdminIdentity(context.Background(), srv.Client(), log.New(io.Discard, "", 0))
	if err != nil || admin != "chosen" {
		t.Fatalf("resolveAdminIdentity = %q, %v; want the configured chosen", admin, err)
	}
}

// Exhausting every candidate is an answer about the instance's accounts, and it
// must be distinguishable from not having been able to ask.
func TestResolveAdminIdentityFailsLoudlyWhenNoCandidateIsAnAdministrator(t *testing.T) {
	resetProvisioningUser(t)
	mock := &provisionMock{
		folders:     `[]`,
		groups:      `[]`,
		roster:      []string{"alice", "bob"},
		adminActors: map[string]int{},
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	admin, err := testExAppConfig(srv.URL).resolveAdminIdentity(context.Background(), srv.Client(), log.New(io.Discard, "", 0))
	if admin != "" || !errors.Is(err, errNoAdminResolved) {
		t.Fatalf("resolveAdminIdentity = %q, %v; want errNoAdminResolved", admin, err)
	}
	if errors.Is(err, errAdminRouteMissing) {
		t.Fatal("an exhausted probe is not the same as an unreachable route")
	}
}

// Provisioning must NOT proceed as an account that may not exist — that is the
// behaviour this ticket exists to remove.
// A large instance must not turn one enabled edge into an unbounded OCS scan.
// The cap is logged when it bites, because silent truncation would look exactly
// like "no administrator exists".
func TestAdminCandidatesCapTheRosterSweepAndSaySo(t *testing.T) {
	resetProvisioningUser(t)
	roster := make([]string, 0, adminProbeMaxCandidates*3)
	for i := 0; i < cap(roster); i++ {
		roster = append(roster, "user"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	mock := &provisionMock{folders: `[]`, groups: `[]`, roster: roster}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	var logs strings.Builder
	got := testExAppConfig(srv.URL).adminCandidates(context.Background(), srv.Client(), log.New(&logs, "", 0))
	if len(got) > adminProbeMaxCandidates+1 {
		t.Fatalf("probing %d candidates, want at most %d", len(got), adminProbeMaxCandidates+1)
	}
	if !strings.Contains(logs.String(), envNCAdminUser) {
		t.Fatalf("a truncated sweep must name the direct override: %s", logs.String())
	}
}

// The roster is an optimisation, not a dependency: an instance where it cannot
// be read must still try the conventional id and the override.
func TestAdminCandidatesSurviveAnUnreadableRoster(t *testing.T) {
	resetProvisioningUser(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	got := testExAppConfig(srv.URL).adminCandidates(context.Background(), srv.Client(), log.New(io.Discard, "", 0))
	if len(got) != 1 || got[0] != defaultNextcloudAdminUser {
		t.Fatalf("candidates = %v, want just %q", got, defaultNextcloudAdminUser)
	}
}

type recordedReq struct{ method, path, auth string }
type provisionMock struct {
	folders, groups string
	roster          []string
	adminList       string
	adminActors     map[string]int
	reqs            []recordedReq
}

func resetProvisioningUser(t *testing.T) {
	t.Helper()
	resolvedProvisioningUser.Store(nil)
	resolvedAdminRoster.Store(nil)
	t.Cleanup(func() { resolvedProvisioningUser.Store(nil); resolvedAdminRoster.Store(nil) })
}
func (m *provisionMock) find(method, suffix string) (recordedReq, bool) {
	for _, req := range m.reqs {
		if req.method == method && strings.HasSuffix(req.path, suffix) {
			return req, true
		}
	}
	return recordedReq{}, false
}
func (m *provisionMock) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.reqs = append(m.reqs, recordedReq{r.Method, r.URL.Path, r.Header.Get("AUTHORIZATION-APP-API")})
		switch r.URL.Path {
		case "/ocs/v2.php/apps/app_api/api/v1/users":
			roster := m.roster
			if roster == nil {
				roster = []string{"admin"}
			}
			raw, _ := json.Marshal(roster)
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":`+string(raw)+`}}`)
		case "/ocs/v2.php/cloud/groups/admin":
			auth, _ := base64.StdEncoding.DecodeString(r.Header.Get("AUTHORIZATION-APP-API"))
			actor, _, _ := strings.Cut(string(auth), ":")
			code := http.StatusUnauthorized
			if m.adminActors == nil && actor == defaultNextcloudAdminUser {
				code = http.StatusOK
			}
			if m.adminActors != nil {
				if v, ok := m.adminActors[actor]; ok {
					code = v
				}
			}
			if code != http.StatusOK {
				w.WriteHeader(code)
				return
			}
			admins := m.adminList
			if admins == "" {
				admins = `["admin"]`
			}
			_, _ = io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":{"users":`+admins+`}}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}
