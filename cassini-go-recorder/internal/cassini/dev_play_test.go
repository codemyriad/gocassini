package cassini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type capturedDevScript struct {
	repoRoot       string
	relativeScript string
	args           []string
	extraEnv       []string
}

func TestDevPlayDefaultsToFullModeAndUsesExistingRoom(t *testing.T) {
	server := newDevPlayTalkTestServer(t, []map[string]string{{"token": "room-token", "displayName": "Existing Room"}}, "")
	defer server.Close()

	captured := captureDevScriptExec(t)
	repoRoot := t.TempDir()

	var stdout strings.Builder
	var stderr strings.Builder
	code := runDevPlay(context.Background(), repoRoot, []string{"--nextcloud-host", server.URL, "--room", "Existing Room"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevPlay code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	wantScript := filepath.Join("harness", "bin", "stream-synthetic-meeting.sh")
	if captured.relativeScript != wantScript {
		t.Fatalf("script=%q want %q", captured.relativeScript, wantScript)
	}
	wantArgs := []string{
		"--call-url", server.URL + "/call/room-token",
		"--scenario", filepath.Join(repoRoot, devPlayShowcaseScenarioRel),
		"--output-dir", filepath.Join(repoRoot, devPlayShowcaseOutputDirRel),
		"--skip-prepare",
	}
	if !reflect.DeepEqual(captured.args, wantArgs) {
		t.Fatalf("args=%#v want %#v", captured.args, wantArgs)
	}
	assertEnvContains(t, captured.extraEnv, "CALL_URL="+server.URL+"/call/room-token")
	assertEnvContains(t, captured.extraEnv, "NEXTCLOUD_URL="+server.URL)
	assertEnvContains(t, captured.extraEnv, "PREPARE=0")
	assertFileContent(t, filepath.Join(repoRoot, "harness", "runtime", "last_room_token"), "room-token\n")
	assertFileContent(t, filepath.Join(repoRoot, "harness", "runtime", "last_call_url"), server.URL+"/call/room-token\n")
	if !strings.Contains(stdout.String(), "mode=full") || !strings.Contains(stdout.String(), "duration=whole") || !strings.Contains(stdout.String(), "room_state=existing") {
		t.Fatalf("launch line missing expected details: %q", stdout.String())
	}
}

func TestDevPlaySingleModeDurationUsesShowcaseMira(t *testing.T) {
	server := newDevPlayTalkTestServer(t, []map[string]string{{"token": "single-token", "displayName": "Single Room"}}, "")
	defer server.Close()

	captured := captureDevScriptExec(t)
	repoRoot := t.TempDir()

	var stdout strings.Builder
	var stderr strings.Builder
	code := runDevPlay(context.Background(), repoRoot, []string{"--nextcloud-host", server.URL, "--room", "Single Room", "--mode", "single", "--duration", "20"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevPlay code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	wantScript := filepath.Join("harness", "bin", "stream-video.sh")
	if captured.relativeScript != wantScript {
		t.Fatalf("script=%q want %q", captured.relativeScript, wantScript)
	}
	wantArgs := []string{
		"--call-url", server.URL + "/call/single-token",
		"--users", "1",
		"--duration", "20",
		"--media-prefix", filepath.Join(repoRoot, devPlayShowcaseOutputDirRel, devPlayShowcaseSingleID),
		"--names", devPlayDefaultSingleName,
		"--skip-prepare",
	}
	if !reflect.DeepEqual(captured.args, wantArgs) {
		t.Fatalf("args=%#v want %#v", captured.args, wantArgs)
	}
	if !strings.Contains(stdout.String(), "mode=single") || !strings.Contains(stdout.String(), "duration=20s") {
		t.Fatalf("launch line missing expected details: %q", stdout.String())
	}
}

func TestDevPlayCreatesMissingRoomAndWritesRuntimeState(t *testing.T) {
	server := newDevPlayTalkTestServer(t, nil, "created-token")
	defer server.Close()

	captured := captureDevScriptExec(t)
	repoRoot := t.TempDir()

	var stdout strings.Builder
	var stderr strings.Builder
	code := runDevPlay(context.Background(), repoRoot, []string{"--nextcloud-host", server.URL, "--room", "Created Room", "--duration", "20"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevPlay code=%d stderr=%q stdout=%q", code, stderr.String(), stdout.String())
	}

	if captured.relativeScript != filepath.Join("harness", "bin", "stream-synthetic-meeting.sh") {
		t.Fatalf("unexpected script: %q", captured.relativeScript)
	}
	if got, want := captured.args[1], server.URL+"/call/created-token"; got != want {
		t.Fatalf("call arg=%q want %q", got, want)
	}
	assertFileContent(t, filepath.Join(repoRoot, "harness", "runtime", "last_room_token"), "created-token\n")
	assertFileContent(t, filepath.Join(repoRoot, "harness", "runtime", "last_call_url"), server.URL+"/call/created-token\n")
	if !strings.Contains(stdout.String(), "room_state=created") {
		t.Fatalf("launch line should show room creation: %q", stdout.String())
	}
}

func TestDevPlayValidatesRequiredRoomAndMode(t *testing.T) {
	captured := captureDevScriptExec(t)
	repoRoot := t.TempDir()

	var stdout strings.Builder
	var stderr strings.Builder
	code := runDevPlay(context.Background(), repoRoot, []string{"--mode", "full"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("missing room code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "--room is required") {
		t.Fatalf("missing room stderr=%q", stderr.String())
	}
	if captured.relativeScript != "" {
		t.Fatalf("script should not run on validation failure: %#v", captured)
	}

	stderr.Reset()
	code = runDevPlay(context.Background(), repoRoot, []string{"--room", "Room", "--mode", "duet"}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("invalid mode code=%d want 2", code)
	}
	if !strings.Contains(stderr.String(), "--mode must be") {
		t.Fatalf("invalid mode stderr=%q", stderr.String())
	}
}

func TestNormalizeDevPlayBaseURL(t *testing.T) {
	cases := []struct {
		name      string
		flagValue string
		envValue  string
		want      string
	}{
		{name: "bare flag host", flagValue: "192.168.252.21", want: "http://192.168.252.21:28080"},
		{name: "bare flag host port", flagValue: "localhost:28080", want: "http://localhost:28080"},
		{name: "full flag URL", flagValue: "http://example.test:8080/", want: "http://example.test:8080"},
		{name: "env host fallback", envValue: "10.0.0.5", want: "http://10.0.0.5:28080"},
		{name: "default fallback", want: "http://127.0.0.1:28080"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeDevPlayBaseURL(tc.flagValue, tc.envValue)
			if err != nil {
				t.Fatalf("normalizeDevPlayBaseURL: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
}

func captureDevScriptExec(t *testing.T) *capturedDevScript {
	t.Helper()
	captured := &capturedDevScript{}
	prev := runDevScriptExec
	runDevScriptExec = func(_ context.Context, repoRoot string, relativeScript string, args []string, extraEnv []string, _ io.Writer, _ io.Writer) int {
		captured.repoRoot = repoRoot
		captured.relativeScript = relativeScript
		captured.args = append([]string{}, args...)
		captured.extraEnv = append([]string{}, extraEnv...)
		return 0
	}
	t.Cleanup(func() { runDevScriptExec = prev })
	return captured
}

func newDevPlayTalkTestServer(t *testing.T, rooms []map[string]string, createToken string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ocs/v2.php/apps/spreed/api/v4/room" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if _, _, ok := r.BasicAuth(); !ok {
			t.Fatalf("expected basic auth")
		}
		switch r.Method {
		case http.MethodGet:
			writeDevPlayOCSTestResponse(t, w, rooms)
		case http.MethodPost:
			if createToken == "" {
				t.Fatalf("unexpected room creation")
			}
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse form: %v", err)
			}
			if got := r.Form.Get("roomName"); got == "" {
				t.Fatalf("missing roomName form field")
			}
			if got := r.Form.Get("roomType"); got != "3" {
				t.Fatalf("roomType=%q want 3", got)
			}
			writeDevPlayOCSTestResponse(t, w, map[string]string{"token": createToken})
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
}

func writeDevPlayOCSTestResponse(t *testing.T, w http.ResponseWriter, data any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	payload := map[string]any{
		"ocs": map[string]any{
			"meta": map[string]any{"status": "ok", "statuscode": 200, "message": "OK"},
			"data": data,
		},
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func assertEnvContains(t *testing.T, env []string, want string) {
	t.Helper()
	for _, entry := range env {
		if entry == want {
			return
		}
	}
	t.Fatalf("env does not contain %q: %#v", want, env)
}

func assertFileContent(t *testing.T, path string, want string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(body) != want {
		t.Fatalf("%s=%q want %q", path, string(body), want)
	}
}
