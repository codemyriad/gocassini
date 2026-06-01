package cassini

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDevPlayPrivateScaffoldCreatesUsersConversationsAndState(t *testing.T) {
	t.Setenv("CASSINI_PLAY_SCAFFOLD_PASSWORD", "test-scaffold-secret")
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "adminpass")

	fake := newDevPlayPrivateScaffoldFake(t)
	server := httptest.NewServer(fake)
	defer server.Close()

	captured := captureDevScriptExec(t)
	repoRoot := t.TempDir()
	createCompleteDevPlayPiedPiperFixture(t, repoRoot)

	var stdout strings.Builder
	var stderr strings.Builder
	code := runDevPlayPrivate(context.Background(), repoRoot, []string{"--nextcloud-host", server.URL, "--scaffold-only"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevPlayPrivate code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if len(captured.calls) != 0 {
		t.Fatalf("fixture prepare should not run for complete fixture: %#v", captured.calls)
	}
	if !fake.createdUsers["cassini-erlich"] || !fake.createdUsers["cassini-monica"] {
		t.Fatalf("expected synthetic users to be created: %#v", fake.createdUsers)
	}
	if got := fake.roomCreates["cassini-erlich->cassini-monica"]; got != 1 {
		t.Fatalf("synthetic room create count=%d want 1", got)
	}
	if got := fake.roomCreates["admin->cassini-erlich"]; got != 1 {
		t.Fatalf("admin room create count=%d want 1", got)
	}

	statePath := filepath.Join(repoRoot, devPlayPrivateScaffoldStateRel)
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read scaffold state: %v", err)
	}
	if strings.Contains(string(stateBytes), "test-scaffold-secret") {
		t.Fatalf("scaffold state should not contain raw synthetic password: %s", string(stateBytes))
	}
	var state devPlayPrivateScaffoldState
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatalf("decode scaffold state: %v", err)
	}
	if state.BaseURL != server.URL {
		t.Fatalf("baseURL=%q want %q", state.BaseURL, server.URL)
	}
	if state.CredentialSource != "env:CASSINI_PLAY_SCAFFOLD_PASSWORD" {
		t.Fatalf("credentialSource=%q", state.CredentialSource)
	}
	if state.Fixture.MediaLabel != devPlayPiedPiperMediaLabel {
		t.Fatalf("mediaLabel=%q", state.Fixture.MediaLabel)
	}
	if state.Users.Erlich.UserID != "cassini-erlich" || state.Users.Erlich.DisplayName != "Erlich Bachman" || state.Users.Erlich.SpeakerID != "erlich" {
		t.Fatalf("unexpected Erlich user state: %#v", state.Users.Erlich)
	}
	if state.Users.Monica.UserID != "cassini-monica" || state.Users.Monica.DisplayName != "Monica Hall" || state.Users.Monica.SpeakerID != "monica" {
		t.Fatalf("unexpected Monica user state: %#v", state.Users.Monica)
	}
	if got := state.Conversations[devPlayPrivateConversationSynthetic].Token; got != "synthetic-token" {
		t.Fatalf("synthetic token=%q", got)
	}
	if got := state.Conversations[devPlayPrivateConversationAdmin].Token; got != "admin-token" {
		t.Fatalf("admin token=%q", got)
	}
	if !strings.Contains(stdout.String(), "play-private scaffold -> users=cassini-erlich,cassini-monica") {
		t.Fatalf("stdout missing scaffold summary: %q", stdout.String())
	}
}

func TestDevPlayPrivateScaffoldIsIdempotentForExistingUsers(t *testing.T) {
	t.Setenv("ADMIN_USER", "admin")
	t.Setenv("ADMIN_PASSWORD", "adminpass")

	fake := newDevPlayPrivateScaffoldFake(t)
	fake.users["cassini-erlich"] = true
	fake.users["cassini-monica"] = true
	server := httptest.NewServer(fake)
	defer server.Close()

	repoRoot := t.TempDir()
	createCompleteDevPlayPiedPiperFixture(t, repoRoot)

	var stdout strings.Builder
	var stderr strings.Builder
	code := runDevPlayPrivate(context.Background(), repoRoot, []string{"--nextcloud-host", server.URL, "--scaffold-only"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevPlayPrivate code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}
	if len(fake.createdUsers) != 0 {
		t.Fatalf("existing users should not be recreated: %#v", fake.createdUsers)
	}
	if got := fake.userUpdates["cassini-erlich:displayname"]; got != 1 {
		t.Fatalf("erlich display update count=%d want 1", got)
	}
	if got := fake.userUpdates["cassini-erlich:password"]; got != 1 {
		t.Fatalf("erlich password update count=%d want 1", got)
	}
	if got := fake.userUpdates["cassini-monica:displayname"]; got != 1 {
		t.Fatalf("monica display update count=%d want 1", got)
	}
	if got := fake.userUpdates["cassini-monica:password"]; got != 1 {
		t.Fatalf("monica password update count=%d want 1", got)
	}

	statePath := filepath.Join(repoRoot, devPlayPrivateScaffoldStateRel)
	stateBytes, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("read scaffold state: %v", err)
	}
	var state devPlayPrivateScaffoldState
	if err := json.Unmarshal(stateBytes, &state); err != nil {
		t.Fatalf("decode scaffold state: %v", err)
	}
	if state.CredentialSource != "dev-fallback" {
		t.Fatalf("credentialSource=%q want dev-fallback", state.CredentialSource)
	}
	if strings.Contains(string(stateBytes), devPlayPrivateFallbackPassword) {
		t.Fatalf("scaffold state should not contain raw fallback password: %s", string(stateBytes))
	}
}

func TestDevPlayPrivateValidatesScaffoldOnlyAndConversationFlags(t *testing.T) {
	repoRoot := t.TempDir()
	captured := captureDevScriptExec(t)

	cases := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{name: "missing target", args: nil, wantStderr: "provide --scaffold-only or --conversation"},
		{name: "mixed target", args: []string{"--scaffold-only", "--conversation", "synthetic"}, wantStderr: "--scaffold-only cannot be combined"},
		{name: "invalid conversation", args: []string{"--conversation", "public"}, wantStderr: "--conversation must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout strings.Builder
			var stderr strings.Builder
			code := runDevPlayPrivate(context.Background(), repoRoot, tc.args, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("code=%d want 2 stderr=%q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.wantStderr) {
				t.Fatalf("stderr=%q want substring %q", stderr.String(), tc.wantStderr)
			}
		})
	}
	if len(captured.calls) != 0 {
		t.Fatalf("scripts should not run on validation failure: %#v", captured.calls)
	}
}

type devPlayPrivateScaffoldFake struct {
	t            *testing.T
	users        map[string]bool
	createdUsers map[string]bool
	userUpdates  map[string]int
	roomCreates  map[string]int
}

func newDevPlayPrivateScaffoldFake(t *testing.T) *devPlayPrivateScaffoldFake {
	t.Helper()
	return &devPlayPrivateScaffoldFake{
		t:            t,
		users:        map[string]bool{},
		createdUsers: map[string]bool{},
		userUpdates:  map[string]int{},
		roomCreates:  map[string]int{},
	}
}

func (f *devPlayPrivateScaffoldFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/ocs/v1.php/cloud/users") {
		f.handleUsers(w, r)
		return
	}
	if r.URL.Path == "/ocs/v2.php/apps/spreed/api/v4/room" {
		f.handleRoom(w, r)
		return
	}
	f.t.Fatalf("unexpected path: %s", r.URL.Path)
}

func (f *devPlayPrivateScaffoldFake) handleUsers(w http.ResponseWriter, r *http.Request) {
	user, password := requireBasicAuth(f.t, r)
	if user != "admin" || password != "adminpass" {
		f.t.Fatalf("user provisioning auth=%s:%s want admin:adminpass", user, password)
	}
	if r.URL.Path == "/ocs/v1.php/cloud/users" {
		if r.Method != http.MethodPost {
			f.t.Fatalf("users collection method=%s want POST", r.Method)
		}
		if err := r.ParseForm(); err != nil {
			f.t.Fatalf("parse create user form: %v", err)
		}
		userID := r.Form.Get("userid")
		if userID == "" || r.Form.Get("password") == "" || r.Form.Get("displayName") == "" {
			f.t.Fatalf("create user missing fields: %s", r.Form.Encode())
		}
		f.users[userID] = true
		f.createdUsers[userID] = true
		writeDevPlayOCSTestResponse(f.t, w, map[string]string{"id": userID})
		return
	}

	encodedID := strings.TrimPrefix(r.URL.Path, "/ocs/v1.php/cloud/users/")
	userID, err := url.PathUnescape(encodedID)
	if err != nil {
		f.t.Fatalf("unescape user path: %v", err)
	}
	switch r.Method {
	case http.MethodGet:
		if !f.users[userID] {
			writeDevPlayOCSTestFailure(f.t, w, http.StatusNotFound, 404, "user not found")
			return
		}
		writeDevPlayOCSTestResponse(f.t, w, map[string]string{"id": userID})
	case http.MethodPut:
		if !f.users[userID] {
			f.t.Fatalf("cannot update missing user %s", userID)
		}
		if err := r.ParseForm(); err != nil {
			f.t.Fatalf("parse update user form: %v", err)
		}
		key := r.Form.Get("key")
		value := r.Form.Get("value")
		if key == "" || value == "" {
			f.t.Fatalf("update user missing fields: %s", r.Form.Encode())
		}
		f.userUpdates[userID+":"+key]++
		writeDevPlayOCSTestResponse(f.t, w, map[string]string{"id": userID})
	default:
		f.t.Fatalf("unexpected user method: %s", r.Method)
	}
}

func (f *devPlayPrivateScaffoldFake) handleRoom(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		f.t.Fatalf("room method=%s want POST", r.Method)
	}
	user, password := requireBasicAuth(f.t, r)
	if err := r.ParseForm(); err != nil {
		f.t.Fatalf("parse room form: %v", err)
	}
	if got := r.Form.Get("roomType"); got != "1" {
		f.t.Fatalf("roomType=%q want 1", got)
	}
	invite := r.Form.Get("invite")
	key := user + "->" + invite
	f.roomCreates[key]++
	switch key {
	case "cassini-erlich->cassini-monica":
		if password != "test-scaffold-secret" && password != devPlayPrivateFallbackPassword {
			f.t.Fatalf("erlich auth password=%q", password)
		}
		writeDevPlayOCSTestResponse(f.t, w, map[string]string{"token": "synthetic-token"})
	case "admin->cassini-erlich":
		if password != "adminpass" {
			f.t.Fatalf("admin auth password=%q", password)
		}
		writeDevPlayOCSTestResponse(f.t, w, map[string]string{"token": "admin-token"})
	default:
		f.t.Fatalf("unexpected room creator/invite: %s", key)
	}
}

func requireBasicAuth(t *testing.T, r *http.Request) (string, string) {
	t.Helper()
	user, password, ok := r.BasicAuth()
	if !ok {
		t.Fatalf("missing basic auth for %s %s", r.Method, r.URL.Path)
	}
	return user, password
}

func writeDevPlayOCSTestFailure(t *testing.T, w http.ResponseWriter, status int, statusCode int, message string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	payload := map[string]any{
		"ocs": map[string]any{
			"meta": map[string]any{"status": "failure", "statuscode": statusCode, "message": message},
			"data": map[string]any{},
		},
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode failure response: %v", err)
	}
}
