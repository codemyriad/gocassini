package operator

import (
	"context"
	"encoding/base64"
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
func TestProvisionStopsWhenNoAdministratorCanBeResolved(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	mock := &provisionMock{
		folders:     `[]`,
		groups:      `[]`,
		roster:      []string{"alice"},
		adminActors: map[string]int{},
	}
	srv := httptest.NewServer(mock.handler(t))
	defer srv.Close()

	var logs strings.Builder
	testExAppConfig(srv.URL).provisionNCFilesAccess(context.Background(), log.New(&logs, "", 0))

	if _, ok := mock.find(http.MethodPost, "/ocs/v2.php/cloud/users"); ok {
		t.Fatal("provisioning created an account while acting as an unresolved administrator")
	}
	if _, ok := mock.find(http.MethodPost, "/index.php/apps/groupfolders/folders"); ok {
		t.Fatal("provisioning created a folder while acting as an unresolved administrator")
	}
	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.State != string(ncSubstrateUnavailable) || snap.Step != "administrator" {
		t.Fatalf("substrate = %+v, want unavailable/administrator", snap)
	}
	// The escape hatch has to be named where someone will read it. Before this
	// ticket the zero-candidates branch logged nothing at all.
	if !strings.Contains(logs.String(), envNCAdminUser) {
		t.Fatalf("the log must name the override: %s", logs.String())
	}
	if !strings.Contains(snap.Detail, envNCAdminUser) {
		t.Fatalf("/status must name the override: %q", snap.Detail)
	}
}

// A missing route is an instance fault, not a statement about its accounts, so
// it is degraded rather than unavailable — there is no app to install and no
// name to set.
func TestProvisionReportsDegradedWhenTheProvisioningRouteIsAbsent(t *testing.T) {
	resetProvisioningUser(t)
	resetSubstrateRecord(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/apps/app_api/api/v1/users") {
			io.WriteString(w, `{"ocs":{"meta":{"statuscode":200},"data":["admin"]}}`)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	testExAppConfig(srv.URL).provisionNCFilesAccess(context.Background(), log.New(io.Discard, "", 0))

	snap := ncAccessSubstrate.snapshot(publishSinkNextcloudFiles)
	if snap.State != string(ncSubstrateDegraded) || snap.Step != "administrator_probe" {
		t.Fatalf("substrate = %+v, want degraded/administrator_probe", snap)
	}
}

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
