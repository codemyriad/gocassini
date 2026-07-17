package cassini

import (
	"bytes"
	"context"
	"io"
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
	code := runDevStack(context.Background(), ".", []string{"plan", "--patch=none"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runDevStack code=%d stderr=%q", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"public:\n  mode: local-http",
		"cassini:\n  mode: none",
		"patch:\n  mode: none",
		"lifecycle:\n  existing_resources: fail",
		"validation: ok",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plan output missing %q: %s", want, out)
		}
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
	code := runDevStack(context.Background(), ".", []string{"up", "--services", "core"}, &stdout, &stderr)
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
