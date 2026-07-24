package cassini

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

func testEnv(values map[string]string) envLookupFunc {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func TestResolveDevStackPlanDefaultsPreserveCompatibility(t *testing.T) {
	plan, rest, err := resolveDevStackPlan("plan", nil, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if len(rest) != 0 {
		t.Fatalf("unexpected rest args: %v", rest)
	}
	if plan.PublicMode != devStackPublicLocalHTTP {
		t.Fatalf("PublicMode = %q", plan.PublicMode)
	}
	if plan.ServiceMode != devStackServiceLegacyDefault {
		t.Fatalf("ServiceMode = %q", plan.ServiceMode)
	}
	if plan.SpreedProfile != "full" {
		t.Fatalf("SpreedProfile = %q, want full", plan.SpreedProfile)
	}
	if plan.CassiniMode != devStackCassiniNone {
		t.Fatalf("CassiniMode = %q", plan.CassiniMode)
	}
	if plan.PatchMode != devStackPatchAuto {
		t.Fatalf("PatchMode = %q", plan.PatchMode)
	}
	if len(plan.ValidationWarnings) != 0 {
		t.Fatalf("ValidationWarnings = %v, want none", plan.ValidationWarnings)
	}
}

func TestResolveDevStackPlanValidationWarnings(t *testing.T) {
	tests := []struct {
		name    string
		command string
		args    []string
		env     map[string]string
		want    []string
	}{
		{
			name:    "SPREED_PROFILE full overridden by appapi",
			command: "plan",
			args:    []string{"--services", "appapi", "--recording-backend", "none"},
			env:     map[string]string{"SPREED_PROFILE": "full"},
			want:    []string{"SPREED_PROFILE=full is ignored because service mode appapi forces SPREED_PROFILE=default."},
		},
		{
			name:    "SPREED_PROFILE default overridden by full",
			command: "plan",
			args:    []string{"--services", "full"},
			env:     map[string]string{"SPREED_PROFILE": "default"},
			want:    []string{"SPREED_PROFILE=default is ignored because service mode full forces SPREED_PROFILE=full."},
		},
		{
			name:    "explicit pull image mode without Cassini",
			command: "plan",
			args:    []string{"--exapp-image-mode", "pull"},
			want:    []string{"ExApp image mode pull is ignored because Cassini mode is none."},
		},
		{
			name:    "explicit reuse-local image mode without Cassini",
			command: "plan",
			args:    []string{"--exapp-image-mode", "reuse-local"},
			want:    []string{"ExApp image mode reuse-local is ignored because Cassini mode is none."},
		},
		{
			name:    "env image mode without Cassini",
			command: "plan",
			env:     map[string]string{"CASSINI_HARNESS_EXAPP_IMAGE_MODE": "pull"},
			want:    []string{"ExApp image mode pull is ignored because Cassini mode is none."},
		},
		{
			name:    "explicit force patch mode without Cassini",
			command: "plan",
			args:    []string{"--patch", "force"},
			want:    []string{"Patch mode force is ignored because Cassini mode is none; no AppAPI CSP patch will run."},
		},
		{
			name:    "explicit none patch mode without Cassini",
			command: "plan",
			args:    []string{"--patch", "none"},
			want:    []string{"Patch mode none is ignored because Cassini mode is none; no AppAPI CSP patch will run."},
		},
		{
			name:    "env patch mode without Cassini",
			command: "plan",
			env:     map[string]string{"CASSINI_HARNESS_PATCH_MODE": "force"},
			want:    []string{"Patch mode force is ignored because Cassini mode is none; no AppAPI CSP patch will run."},
		},
		{
			name:    "remote media inputs without media services",
			command: "plan",
			args: []string{
				"--public-mode", "remote-https",
				"--public-host", "remote.example",
				"--media-host", "100.85.120.118",
				"--services", "appapi",
				"--recording-backend", "none",
			},
			want: []string{"Media/signaling remote inputs are ignored because the resolved service topology does not start the Talk media stack."},
		},
		{
			name:    "installed ExApp bypassed by legacy backend",
			command: "plan",
			args:    []string{"--services", "full", "--cassini", "installed-exapp", "--recording-backend", "legacy"},
			want:    []string{"Cassini is installed as an ExApp, but Talk recording uses the legacy backend; the installed ExApp will not receive recording callbacks."},
		},
		{
			name:    "installed ExApp bypassed by direct operator backend",
			command: "plan",
			args:    []string{"--services", "full", "--cassini", "installed-exapp", "--recording-backend", "direct-operator"},
			want:    []string{"Cassini is installed as an ExApp, but Talk recording uses the direct-operator backend; the installed ExApp will not receive recording callbacks."},
		},
		{
			name:    "private media IP in remote mode",
			command: "plan",
			args: []string{
				"--public-mode", "remote-https",
				"--public-host", "remote.example",
				"--media-host", "192.168.1.10",
				"--services", "full-remote",
			},
			want: []string{"remote-https media host 192.168.1.10 is private/RFC1918; browsers outside that private network will not reach WebRTC media."},
		},
		{
			name:    "up reset",
			command: "up",
			args:    []string{"--reset"},
			want:    []string{"Existing-resource mode reset will remove and recreate the resolved stack, including Docker Compose volumes."},
		},
		{
			name:    "plan reset from env with installed ExApp",
			command: "plan",
			args:    []string{"--services", "full", "--cassini", "installed-exapp", "--recording-backend", "installed-exapp"},
			env:     map[string]string{"CASSINI_HARNESS_EXISTING": "reset"},
			want:    []string{"Existing-resource mode reset will remove and recreate the resolved stack, including Docker Compose volumes and installed ExApp state."},
		},
		{
			name:    "down volumes",
			command: "down",
			args:    []string{"--volumes"},
			want:    []string{"down --volumes will remove current-project containers and Docker Compose volumes, plus installed ExApp containers and state volumes if present."},
		},
		{
			name:    "down full",
			command: "down",
			args:    []string{"--full"},
			want:    []string{"down --full will remove all known harness-owned Compose and installed ExApp resources, including volumes."},
		},
		{
			name:    "explicit recording backend without media services",
			command: "plan",
			args:    []string{"--services", "appapi", "--recording-backend", "legacy"},
			want:    []string{"Recording backend legacy will not be configured because the resolved service topology does not start the Talk media stack; use recording backend none for install-only checks."},
		},
		{
			name:    "env recording backend without media services",
			command: "plan",
			args:    []string{"--services", "appapi"},
			env:     map[string]string{"CASSINI_HARNESS_RECORDING_BACKEND": "legacy"},
			want:    []string{"Recording backend legacy will not be configured because the resolved service topology does not start the Talk media stack; use recording backend none for install-only checks."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, _, err := resolveDevStackPlan(tt.command, tt.args, testEnv(tt.env))
			if err != nil {
				t.Fatalf("resolveDevStackPlan: %v", err)
			}
			if !reflect.DeepEqual(plan.ValidationWarnings, tt.want) {
				t.Fatalf("ValidationWarnings = %#v, want %#v", plan.ValidationWarnings, tt.want)
			}
		})
	}
}

func TestResolveDevStackPlanDoesNotWarnForDeferredScenarios(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{
			name: "default legacy backend without media services",
			args: []string{"--services", "appapi"},
		},
		{
			name: "explicit auto patch without Cassini",
			args: []string{"--patch", "auto"},
		},
		{
			name: "none patch with installed ExApp",
			args: []string{"--services", "full", "--cassini", "installed-exapp", "--recording-backend", "installed-exapp", "--patch", "none"},
		},
		{
			name: "explicit local mode masks remote env",
			args: []string{"--public-mode", "local-http"},
			env: map[string]string{
				"CASSINI_HARNESS_PUBLIC_URL":           "https://remote.example",
				"CASSINI_HARNESS_PUBLIC_HOST":          "remote.example",
				"CASSINI_HARNESS_MEDIA_HOST":           "100.85.120.118",
				"CASSINI_HARNESS_SIGNALING_PUBLIC_URL": "https://remote.example:8443",
			},
		},
		{
			name: "private public host with reachable media host",
			args: []string{
				"--public-mode", "remote-https",
				"--public-host", "192.168.1.20",
				"--media-host", "100.85.120.118",
				"--services", "full-remote",
			},
		},
		{
			name: "pull image for installed ExApp",
			args: []string{"--services", "full", "--cassini", "installed-exapp", "--recording-backend", "installed-exapp", "--exapp-image-mode", "pull"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan, _, err := resolveDevStackPlan("plan", tt.args, testEnv(tt.env))
			if err != nil {
				t.Fatalf("resolveDevStackPlan: %v", err)
			}
			if len(plan.ValidationWarnings) != 0 {
				t.Fatalf("ValidationWarnings = %v, want none", plan.ValidationWarnings)
			}
		})
	}
}

func TestResolveDevStackPlanWarningOrder(t *testing.T) {
	plan, _, err := resolveDevStackPlan("up", []string{
		"--public-mode", "remote-https",
		"--public-host", "remote.example",
		"--media-host", "192.168.1.10",
		"--services", "appapi",
		"--cassini", "installed-exapp",
		"--recording-backend", "legacy",
		"--reset",
	}, testEnv(map[string]string{"SPREED_PROFILE": "full"}))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}

	want := []string{
		"SPREED_PROFILE=full is ignored because service mode appapi forces SPREED_PROFILE=default.",
		"Media/signaling remote inputs are ignored because the resolved service topology does not start the Talk media stack.",
		"Cassini is installed as an ExApp, but Talk recording uses the legacy backend; the installed ExApp will not receive recording callbacks.",
		"Recording backend legacy will not be configured because the resolved service topology does not start the Talk media stack; use recording backend none for install-only checks.",
		"Existing-resource mode reset will remove and recreate the resolved stack, including Docker Compose volumes and installed ExApp state.",
	}
	if !reflect.DeepEqual(plan.ValidationWarnings, want) {
		t.Fatalf("ValidationWarnings = %#v, want %#v", plan.ValidationWarnings, want)
	}
}

func TestRFC1918IPv4Host(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{"10.0.0.1", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"192.168.1.10", true},
		{"172.15.255.254", false},
		{"172.32.0.1", false},
		{"100.85.120.118", false},
		{"203.0.113.10", false},
		{"remote.example", false},
		{"[::1]", false},
	}
	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			if got := isRFC1918IPv4Host(tt.host); got != tt.want {
				t.Fatalf("isRFC1918IPv4Host(%q) = %t, want %t", tt.host, got, tt.want)
			}
		})
	}
}

func TestResolveDevStackPlanHardFailureDoesNotCollectWarnings(t *testing.T) {
	plan, _, err := resolveDevStackPlan("plan", []string{
		"--services", "appapi",
		"--cassini", "installed-exapp",
		"--recording-backend", "direct-operator",
	}, testEnv(map[string]string{"SPREED_PROFILE": "full"}))
	if err == nil {
		t.Fatal("expected hard validation failure")
	}
	if len(plan.ValidationWarnings) != 0 {
		t.Fatalf("hard failure collected warnings: %v", plan.ValidationWarnings)
	}
}

func TestResolveDevStackPlanRejectsRemoteInputsWithoutRemoteMode(t *testing.T) {
	_, _, err := resolveDevStackPlan("plan", nil, testEnv(map[string]string{
		"CASSINI_HARNESS_PUBLIC_URL": "https://16a.tail.example",
	}))
	if err == nil {
		t.Fatal("expected remote input validation error")
	}
	if !strings.Contains(err.Error(), "remote harness inputs require --public-mode remote-https") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanFlagsOverrideEnv(t *testing.T) {
	plan, _, err := resolveDevStackPlan("plan", []string{
		"--services", "core",
		"--recording-backend", "none",
		"--patch", "force",
	}, testEnv(map[string]string{
		"CASSINI_HARNESS_SERVICE_MODE":      "full",
		"CASSINI_HARNESS_RECORDING_BACKEND": "direct-operator",
		"CASSINI_HARNESS_PATCH_MODE":        "none",
		"CASSINI_HARNESS_EXAPP_IMAGE_MODE":  "pull",
	}))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if plan.ServiceMode != devStackServiceCore {
		t.Fatalf("ServiceMode = %q, want flag value core", plan.ServiceMode)
	}
	if plan.RecordingBackend != devStackRecordingNone {
		t.Fatalf("RecordingBackend = %q, want flag value none", plan.RecordingBackend)
	}
	if plan.PatchMode != devStackPatchForce {
		t.Fatalf("PatchMode = %q, want flag value force", plan.PatchMode)
	}
	if plan.ExAppImageMode != devStackImagePull {
		t.Fatalf("ExAppImageMode = %q, want env value pull", plan.ExAppImageMode)
	}
}

func TestResolveDevStackPlanExplicitLocalModeMasksRemoteEnv(t *testing.T) {
	remoteEnv := map[string]string{
		"CASSINI_HARNESS_PUBLIC_URL":           "https://16a.tail.example",
		"CASSINI_HARNESS_PUBLIC_HOST":          "16a.tail.example",
		"CASSINI_HARNESS_MEDIA_HOST":           "100.127.22.64",
		"CASSINI_HARNESS_SIGNALING_PUBLIC_URL": "https://16a.tail.example:8443",
	}

	plan, _, err := resolveDevStackPlan("plan", []string{"--public-mode", "local-http"}, testEnv(remoteEnv))
	if err != nil {
		t.Fatalf("explicit local-http must override remote env vars: %v", err)
	}
	if plan.PublicURL != "" || plan.PublicHost != "" || plan.MediaHost != "" || plan.SignalingPublicURL != "" {
		t.Fatalf("remote inputs not masked: %+v", plan)
	}
	joined := strings.Join(plan.env(), "\n")
	for _, want := range []string{
		"CASSINI_HARNESS_PUBLIC_URL=\n",
		"CASSINI_HARNESS_PUBLIC_HOST=\n",
		"CASSINI_HARNESS_MEDIA_HOST=\n",
	} {
		if !strings.Contains(joined+"\n", want) {
			t.Fatalf("plan env must mask ambient %q, got %v", strings.TrimSpace(want), plan.env())
		}
	}

	// A remote input passed as a flag is still contradictory in local mode.
	_, _, err = resolveDevStackPlan("plan", []string{"--public-mode", "local-http", "--public-url", "https://x.example"}, testEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "remote harness inputs require") {
		t.Fatalf("expected flag contradiction error, got %v", err)
	}

	// Explicit remote mode still consumes env-provided remote inputs.
	plan, _, err = resolveDevStackPlan("plan", []string{"--public-mode", "remote-https"}, testEnv(remoteEnv))
	if err != nil {
		t.Fatalf("explicit remote-https with env inputs: %v", err)
	}
	if plan.PublicURL != "https://16a.tail.example" || plan.MediaHost != "100.127.22.64" {
		t.Fatalf("env remote inputs not consumed in remote mode: %+v", plan)
	}
}

func TestResolveDevStackPlanRemoteHTTPS(t *testing.T) {
	plan, _, err := resolveDevStackPlan("plan", []string{
		"--public-mode", "remote-https",
		"--public-url", "https://16a.tail.example/",
		"--media-host", "100.127.22.64",
		"--services", "full-remote",
	}, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if plan.PublicURL != "https://16a.tail.example" {
		t.Fatalf("PublicURL = %q", plan.PublicURL)
	}
	if plan.PublicHost != "16a.tail.example" {
		t.Fatalf("PublicHost = %q", plan.PublicHost)
	}
	if plan.SignalingPublicURL != "https://16a.tail.example:8443" {
		t.Fatalf("SignalingPublicURL = %q", plan.SignalingPublicURL)
	}
	if !plan.RemoteConfigRequested {
		t.Fatal("expected remote config to be requested")
	}
}

func TestResolveDevStackPlanLANHTTPRequiresExplicitConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "public URL",
			args:    []string{"--public-mode", "lan-http", "--services", "full"},
			wantErr: "requires --public-url",
		},
		{
			name:    "media host",
			args:    []string{"--public-mode", "lan-http", "--public-url", "http://127.0.0.1:28080", "--services", "full"},
			wantErr: "requires --media-host",
		},
		{
			name: "signaling URL",
			args: []string{
				"--public-mode", "lan-http",
				"--public-url", "http://127.0.0.1:28080",
				"--media-host", "192.168.1.67",
				"--services", "full",
			},
			wantErr: "requires --signaling-public-url",
		},
		{
			name: "non-loopback signaling URL",
			args: []string{
				"--public-mode", "lan-http",
				"--public-url", "http://127.0.0.1:28080",
				"--media-host", "192.168.1.67",
				"--signaling-public-url", "http://127.0.0.1:28082",
				"--services", "full",
			},
			wantErr: "must use a non-loopback host",
		},
		{
			name: "installed ExApp callback URL",
			args: []string{
				"--public-mode", "lan-http",
				"--public-url", "http://127.0.0.1:28080",
				"--media-host", "192.168.1.67",
				"--signaling-public-url", "http://192.168.1.67:28082",
				"--services", "full",
				"--cassini", "installed-exapp",
				"--recording-backend", "installed-exapp",
			},
			wantErr: "requires --talk-backend-url",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := resolveDevStackPlan("plan", tt.args, testEnv(nil))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("expected error containing %q, got %v", tt.wantErr, err)
			}
		})
	}
}

func TestResolveDevStackPlanLANHTTPInstalledExApp(t *testing.T) {
	plan, _, err := resolveDevStackPlan("plan", []string{
		"--public-mode", "lan-http",
		"--public-url", "http://127.0.0.1:28080",
		"--media-host", "192.168.1.67",
		"--signaling-public-url", "http://192.168.1.67:28082",
		"--talk-backend-url", "http://reverse-proxy",
		"--services", "full",
		"--cassini", "installed-exapp",
		"--recording-backend", "installed-exapp",
		"--build",
	}, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if plan.TalkBackendURL != "http://reverse-proxy" {
		t.Fatalf("TalkBackendURL = %q", plan.TalkBackendURL)
	}
	if !strings.Contains(strings.Join(plan.env(), "\n"), "CASSINI_TALK_BACKEND_URL=http://reverse-proxy") {
		t.Fatalf("plan env does not include Talk backend URL: %v", plan.env())
	}
}

func TestResolveDevStackPlanInstalledRecordingRequiresInstalledCassini(t *testing.T) {
	_, _, err := resolveDevStackPlan("up", []string{"--recording-backend", "installed-exapp"}, testEnv(nil))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires --cassini installed-exapp") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanInstalledCassiniRejectsCoreServices(t *testing.T) {
	_, _, err := resolveDevStackPlan("up", []string{"--cassini", "installed-exapp", "--services", "core"}, testEnv(nil))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires service mode appapi") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanRecordingBackendRequiresMediaServices(t *testing.T) {
	_, _, err := resolveDevStackPlan("up", []string{"--recording-backend", "direct-operator", "--services", "appapi"}, testEnv(nil))
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "requires service mode full") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestResolveDevStackPlanInstalledExAppRecordingShape(t *testing.T) {
	plan, _, err := resolveDevStackPlan("up", []string{"--cassini", "installed-exapp", "--recording-backend", "installed-exapp", "--services", "full", "--build"}, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if plan.ExAppImageMode != devStackImageBuild {
		t.Fatalf("ExAppImageMode = %q", plan.ExAppImageMode)
	}
}

func TestResolveDevStackPlanScopesLifecycleFlags(t *testing.T) {
	_, _, err := resolveDevStackPlan("down", []string{"--reset"}, testEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "apply only to stack up") {
		t.Fatalf("expected up-only lifecycle error, got %v", err)
	}

	_, _, err = resolveDevStackPlan("up", []string{"--full"}, testEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "apply only to stack down") {
		t.Fatalf("expected down-only full error, got %v", err)
	}

	_, _, err = resolveDevStackPlan("down", []string{"--suspend", "--volumes"}, testEnv(nil))
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("expected suspend/volumes conflict error, got %v", err)
	}
}

func TestResolveDevStackPlanDownFull(t *testing.T) {
	plan, _, err := resolveDevStackPlan("down", []string{"--full"}, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	if !plan.DownFull {
		t.Fatal("expected DownFull")
	}
}

func TestRunDevStackPlanPrintsResolvedPlan(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runDevStack(context.Background(), ".", []string{"plan", "--public-mode=local-http"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevStack code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"public:\n  mode: local-http",
		"cassini:\n  mode: none",
		"patch:\n  mode: auto",
		"lifecycle:\n  existing_resources: fail",
		"validation: ok",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q: %s", want, out)
		}
	}
}

func TestRunDevStackHardFailureDoesNotPrintPlan(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDevStack(context.Background(), ".", []string{
		"plan",
		"--public-mode=local-http",
		"--services=appapi",
		"--recording-backend=direct-operator",
	}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("runDevStack code=%d, want 2", code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("hard failure printed plan: %q", stdout.String())
	}
	if !strings.Contains(stderr.String(), "requires service mode full") {
		t.Fatalf("stderr missing hard validation error: %q", stderr.String())
	}
}

func TestPrintDevStackPlanValidationWarnings(t *testing.T) {
	plan, _, err := resolveDevStackPlan("plan", nil, testEnv(nil))
	if err != nil {
		t.Fatalf("resolveDevStackPlan: %v", err)
	}
	plan.ValidationWarnings = []string{"first warning", "second warning"}

	var output bytes.Buffer
	printDevStackPlan(&output, plan)

	want := "validation:\n  warnings:\n    - first warning\n    - second warning\n"
	if !strings.Contains(output.String(), want) {
		t.Fatalf("plan output missing warning block %q: %s", want, output.String())
	}
	if strings.Contains(output.String(), "validation: ok") {
		t.Fatalf("warning plan must not print validation ok: %s", output.String())
	}
}

func TestPrintDevStackCommandWarnings(t *testing.T) {
	var output bytes.Buffer
	printDevStackCommandWarnings(&output, "up", nil)
	if output.Len() != 0 {
		t.Fatalf("empty warning list printed %q", output.String())
	}

	printDevStackCommandWarnings(&output, "up", []string{"first warning", "second warning"})
	want := "dev stack up: validation warnings:\n  - first warning\n  - second warning\n"
	if output.String() != want {
		t.Fatalf("warning output = %q, want %q", output.String(), want)
	}
}

func TestRunDevStackDownFlagsMapToScript(t *testing.T) {
	prevExec := runDevScriptExec
	defer func() { runDevScriptExec = prevExec }()

	var gotScript string
	var gotArgs []string
	runDevScriptExec = func(_ context.Context, _ string, relativeScript string, args []string, _ []string, _ io.Writer, _ io.Writer) int {
		gotScript = relativeScript
		gotArgs = args
		return 0
	}
	var stdout, stderr bytes.Buffer

	cases := []struct {
		args []string
		want []string
	}{
		{[]string{"down"}, nil}, // bare: remove containers, keep volumes
		{[]string{"down", "--suspend"}, []string{"--suspend"}},
		{[]string{"down", "--volumes"}, []string{"--volumes"}},
		{[]string{"down", "--full"}, []string{"--full"}},
	}
	for _, tc := range cases {
		gotArgs = nil
		if code := runDevStack(context.Background(), ".", tc.args, &stdout, &stderr); code != 0 {
			t.Fatalf("runDevStack %v code=%d stderr=%q", tc.args, code, stderr.String())
		}
		if gotScript != "harness/bin/down.sh" {
			t.Fatalf("%v script = %q", tc.args, gotScript)
		}
		if strings.Join(gotArgs, " ") != strings.Join(tc.want, " ") {
			t.Fatalf("%v -> down.sh args = %v, want %v", tc.args, gotArgs, tc.want)
		}
	}
}

func TestRunDevStackStopCommandRemoved(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runDevStack(context.Background(), ".", []string{"stop"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("expected non-zero exit for removed stop command")
	}
	if !strings.Contains(stderr.String(), "stack down") {
		t.Fatalf("stop error should point at down, got %q", stderr.String())
	}
}

func TestRunDevStackWarningsPreserveScriptExitCode(t *testing.T) {
	prevExec := runDevScriptExec
	defer func() { runDevScriptExec = prevExec }()

	for _, scriptCode := range []int{0, 17} {
		t.Run(fmt.Sprintf("script code %d", scriptCode), func(t *testing.T) {
			called := false
			runDevScriptExec = func(_ context.Context, _ string, relativeScript string, _ []string, _ []string, _ io.Writer, _ io.Writer) int {
				called = true
				if relativeScript != "harness/bin/up.sh" {
					t.Fatalf("script = %q", relativeScript)
				}
				return scriptCode
			}

			var stdout, stderr bytes.Buffer
			code := runDevStack(context.Background(), ".", []string{"up", "--reset", "--public-mode=local-http"}, &stdout, &stderr)
			if code != scriptCode {
				t.Fatalf("runDevStack code=%d, want script code %d", code, scriptCode)
			}
			if !called {
				t.Fatal("up script was not called")
			}
			if !strings.Contains(stderr.String(), "dev stack up: validation warnings:") || !strings.Contains(stderr.String(), "Existing-resource mode reset") {
				t.Fatalf("stderr missing reset warning: %q", stderr.String())
			}
		})
	}
}

func TestRunDevStackDownPrintsDestructiveWarnings(t *testing.T) {
	prevExec := runDevScriptExec
	defer func() { runDevScriptExec = prevExec }()

	runDevScriptExec = func(_ context.Context, _ string, relativeScript string, args []string, _ []string, _ io.Writer, _ io.Writer) int {
		if relativeScript != "harness/bin/down.sh" {
			t.Fatalf("script = %q", relativeScript)
		}
		if !reflect.DeepEqual(args, []string{"--full"}) {
			t.Fatalf("args = %v, want [--full]", args)
		}
		return 0
	}

	var stdout, stderr bytes.Buffer
	code := runDevStack(context.Background(), ".", []string{"down", "--full", "--public-mode=local-http"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevStack code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "dev stack down: validation warnings:") || !strings.Contains(stderr.String(), "down --full will remove all known harness-owned") {
		t.Fatalf("stderr missing full-down warning: %q", stderr.String())
	}
}

func TestRunDevStackUpPassesResolvedEnv(t *testing.T) {
	prevExec := runDevScriptExec
	defer func() { runDevScriptExec = prevExec }()

	var gotScript string
	var gotEnv []string
	runDevScriptExec = func(_ context.Context, _ string, relativeScript string, _ []string, extraEnv []string, _ io.Writer, _ io.Writer) int {
		gotScript = relativeScript
		gotEnv = extraEnv
		return 0
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := runDevStack(context.Background(), ".", []string{"up", "--public-mode=local-http", "--services", "core"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevStack code=%d stderr=%q", code, stderr.String())
	}
	if gotScript != "harness/bin/up.sh" {
		t.Fatalf("script = %q", gotScript)
	}
	joined := strings.Join(gotEnv, "\n")
	if !strings.Contains(joined, "CASSINI_HARNESS_SERVICE_MODE=core") || !strings.Contains(joined, "SPREED_PROFILE=default") {
		t.Fatalf("missing resolved env in %v", gotEnv)
	}
}
